// Package capture records what a game or any other renderer draws. A
// Recorder reads the pixels of a Source (an Ebitengine screen or any other
// *ebiten.Image, for example) on every frame, hands them to the hardware
// encoder on a background goroutine as RGBA frames, and pushes the
// resulting packets into a hwmediacodec.PacketWriteCloser: an MP4 file, an
// HLS segmenter or a WebRTC track.
//
// ReadPixels is a GPU read-back, so it costs a synchronisation per frame
// (around a millisecond at 1080p on Apple Silicon); the encode itself runs
// off the game loop and the loop never blocks on it. When the encoder falls
// behind, frames are dropped rather than slowing the game down, and the
// presentation timestamps come from the wall clock quantised to the frame
// rate, so dropped frames leave gaps instead of speeding the recording up.
package capture

import (
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shibukawa/hwmediacodec"
)

// TimeScale is the unit of the packets a Recorder produces.
const TimeScale = 90000

// Source is what Capture reads: *ebiten.Image satisfies it.
type Source interface {
	Bounds() image.Rectangle
	ReadPixels(pixels []byte)
}

// Options configure the encoder behind a Recorder. The zero value records
// H.264 at 60 frames per second with the encoder's default rate control, a
// keyframe every two seconds and a queue of four frames.
type Options struct {
	Codec            hwmediacodec.Codec
	FPS              float64
	Bitrate          int
	Quality          float64
	CBR              bool
	KeyframeInterval int // frames; 0 means two seconds' worth
	LowLatency       bool
	Profile          hwmediacodec.Profile
	Queue            int // frames buffered between the game loop and the encoder
	Software         bool
	// Extra encoder options appended last.
	Extra []hwmediacodec.EncoderOption
}

func (o *Options) defaults() {
	if o.Codec == 0 {
		o.Codec = hwmediacodec.H264
	}
	if o.FPS <= 0 {
		o.FPS = 60
	}
	if o.KeyframeInterval == 0 {
		o.KeyframeInterval = int(o.FPS * 2)
	}
	if o.Queue <= 0 {
		o.Queue = 4
	}
}

func (o Options) encoderOptions() []hwmediacodec.EncoderOption {
	opts := []hwmediacodec.EncoderOption{
		hwmediacodec.WithInputFormat(hwmediacodec.RGBA),
		hwmediacodec.WithTimeScale(TimeScale),
		hwmediacodec.WithFrameRate(o.FPS),
		hwmediacodec.WithKeyframeInterval(o.KeyframeInterval),
	}
	if o.Bitrate > 0 {
		opts = append(opts, hwmediacodec.WithBitrate(o.Bitrate))
		if o.CBR {
			opts = append(opts, hwmediacodec.WithRateControl(hwmediacodec.CBR))
		}
	}
	if o.Quality > 0 {
		opts = append(opts, hwmediacodec.WithQuality(o.Quality))
	}
	if o.LowLatency {
		opts = append(opts, hwmediacodec.WithLowLatency())
	}
	if o.Profile != hwmediacodec.ProfileDefault {
		opts = append(opts, hwmediacodec.WithProfile(o.Profile))
	}
	if o.Software {
		opts = append(opts, hwmediacodec.WithSoftwareFallback())
	}
	return append(opts, o.Extra...)
}

type frame struct {
	pix []byte
	pts int64
	key bool
}

// Recorder encodes captured frames into a packet writer.
type Recorder struct {
	opts   Options
	sink   hwmediacodec.PacketWriteCloser
	width  int
	height int
	step   int64 // PTS units per frame

	enc    hwmediacodec.Encoder
	free   chan []byte
	frames chan frame
	done   chan struct{}

	start    time.Time
	lastPTS  int64
	wantKey  atomic.Bool
	captured atomic.Int64
	dropped  atomic.Int64
	encoded  atomic.Int64

	mu     sync.Mutex
	err    error
	closed bool
}

// New opens the encoder for pictures of the given size. Capture must then
// be called with images of exactly that size.
func New(width, height int, sink hwmediacodec.PacketWriteCloser, opts Options) (*Recorder, error) {
	opts.defaults()
	ctx := context.Background()
	enc, err := hwmediacodec.NewEncoder(ctx, opts.Codec, width, height, opts.encoderOptions()...)
	if err != nil {
		return nil, err
	}
	r := &Recorder{
		opts:    opts,
		sink:    sink,
		width:   width,
		height:  height,
		step:    int64(float64(TimeScale)/opts.FPS + 0.5),
		enc:     enc,
		free:    make(chan []byte, opts.Queue+1),
		frames:  make(chan frame, opts.Queue),
		done:    make(chan struct{}),
		lastPTS: -1,
	}
	for range opts.Queue + 1 {
		r.free <- make([]byte, 4*width*height)
	}
	go r.run(ctx)
	return r, nil
}

// Capture reads the pixels of src and queues them for encoding. Call it
// from Draw after drawing the frame. It never blocks: when the queue is
// full the frame is dropped and counted in Dropped. A second capture
// within the same frame slot (a 120 Hz display showing a 60 fps game) is
// ignored.
func (r *Recorder) Capture(src Source) {
	r.CaptureAt(src, time.Now())
}

// CaptureAt is Capture with an explicit time stamp for the frame.
func (r *Recorder) CaptureAt(src Source, now time.Time) {
	if r.Err() != nil {
		return
	}
	b := src.Bounds()
	if b.Dx() != r.width || b.Dy() != r.height {
		r.fail(fmt.Errorf("capture: image is %dx%d, recorder is %dx%d", b.Dx(), b.Dy(), r.width, r.height))
		return
	}
	if r.start.IsZero() {
		r.start = now
	}
	slot := int64(now.Sub(r.start).Seconds()*r.opts.FPS + 0.5)
	pts := slot * r.step
	if pts <= r.lastPTS {
		return
	}
	var pix []byte
	select {
	case pix = <-r.free:
	default:
		r.dropped.Add(1)
		return
	}
	src.ReadPixels(pix)
	r.lastPTS = pts
	select {
	case r.frames <- frame{pix: pix, pts: pts, key: r.wantKey.Swap(false)}:
		r.captured.Add(1)
	default:
		// The queue filled up between the buffer check and now.
		r.dropped.Add(1)
		r.free <- pix
	}
}

func (r *Recorder) run(ctx context.Context) {
	defer close(r.done)
	drain := func() error {
		for {
			p, err := r.enc.Receive(ctx)
			if errors.Is(err, hwmediacodec.ErrAgain) || err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			r.encoded.Add(1)
			if err := r.sink.WritePacket(p); err != nil {
				return err
			}
		}
	}
	for c := range r.frames {
		if r.Err() != nil {
			r.free <- c.pix
			continue
		}
		f := &hwmediacodec.Frame{
			Width: r.width, Height: r.height, Format: hwmediacodec.RGBA,
			Planes: [][]byte{c.pix}, Strides: []int{4 * r.width}, PTS: c.pts,
			ForceKeyframe: c.key,
		}
		err := r.enc.Send(ctx, f) // the frame is copied before Send returns
		r.free <- c.pix
		if err == nil {
			err = drain()
		}
		if err != nil {
			r.fail(err)
		}
	}
	if r.Err() == nil {
		if err := r.enc.Flush(ctx); err != nil {
			r.fail(err)
		} else if err := drain(); err != nil {
			r.fail(err)
		}
	}
	r.enc.Close()
	if err := r.sink.Close(); err != nil {
		r.fail(err)
	}
}

func (r *Recorder) fail(err error) {
	r.mu.Lock()
	if r.err == nil {
		r.err = err
	}
	r.mu.Unlock()
}

// RequestKeyframe makes the next captured frame a keyframe: what a
// streaming sink does when a new viewer joins or a receiver reports a
// picture loss. Safe to call from any goroutine.
func (r *Recorder) RequestKeyframe() { r.wantKey.Store(true) }

// Err returns the first error from the encoder or the sink.
func (r *Recorder) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

// Captured is the number of frames handed to the encoder so far.
func (r *Recorder) Captured() int64 { return r.captured.Load() }

// Dropped is the number of frames skipped because the encoder was behind.
func (r *Recorder) Dropped() int64 { return r.dropped.Load() }

// Encoded is the number of packets delivered to the sink so far.
func (r *Recorder) Encoded() int64 { return r.encoded.Load() }

// Duration is the recording time covered so far.
func (r *Recorder) Duration() time.Duration {
	if r.lastPTS < 0 {
		return 0
	}
	return time.Duration(r.lastPTS+r.step) * time.Second / TimeScale
}

// Close stops capturing, flushes the encoder, closes the sink and returns
// the first error that occurred.
func (r *Recorder) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return r.err
	}
	r.closed = true
	r.mu.Unlock()
	close(r.frames)
	<-r.done
	return r.Err()
}
