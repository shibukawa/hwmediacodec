package playback

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"
	"time"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/encoding/annexb"
)

// ErrNotSeekable is returned by Player.Seek when the source is not a
// hwmediacodec.PacketSeeker (a plain io.Reader, for example).
var ErrNotSeekable = errors.New("playback: the source cannot seek")

var errStopped = errors.New("playback: stopped")

// sourceTimeScale is the decoder time scale: microseconds, so that
// presentation times survive the trip through Packet.PTS and Frame.PTS.
const streamTimeScale = 1_000_000

// item is one decoded frame, or the end marker of a generation.
type item struct {
	frame   *hwmediacodec.Frame
	pts     time.Duration
	gen     int           // seek generation the frame belongs to
	loop    int           // loop iteration within the generation
	rebase  time.Duration // on the first frame of a new iteration: the previous iteration's length
	end     bool          // no frame: the generation reached the end of the stream
	release func()
}

type seekRequest struct {
	target time.Duration
	gen    int
}

// source decodes on its own goroutine and hands frames over in display
// order through a bounded channel, so that decoding runs ahead of playback
// by at most the prefetch depth. Seeks are requests the goroutine picks up
// between packets.
type source struct {
	dec    hwmediacodec.Decoder
	frames *hwmediacodec.DecodeReader
	src    hwmediacodec.PacketReader
	seeker hwmediacodec.PacketSeeker // nil when the source cannot seek
	scale  time.Duration             // PTS units per second
	stream *streamSource             // set for NewPlayer: frames are timed by output order
	loop   bool

	items chan item
	stop  chan struct{}
	done  chan struct{}
	wake  chan struct{} // a seek request is pending

	mu      sync.Mutex
	pending *seekRequest
	err     error
	closed  bool
}

func newSource(src hwmediacodec.PacketReader, prefetch int, loop, software bool, format hwmediacodec.PixelFormat) (*source, error) {
	seeker, _ := src.(hwmediacodec.PacketSeeker)
	if loop && seeker == nil {
		return nil, errors.New("playback: looping needs a seekable source (an io.ReadSeeker or a hwmediacodec.PacketSeeker)")
	}
	opts := []hwmediacodec.DecoderOption{
		hwmediacodec.WithOutputFormat(format),
		hwmediacodec.WithTimeScale(src.TimeScale()),
	}
	if software {
		opts = append(opts, hwmediacodec.WithSoftwareFallback())
	}
	dec, err := hwmediacodec.NewDecoder(context.Background(), src.Codec(), opts...)
	if err != nil {
		return nil, err
	}
	if prefetch < 1 {
		prefetch = 1
	}
	s := &source{
		dec:    dec,
		frames: hwmediacodec.NewDecodeReader(dec, src),
		src:    src,
		seeker: seeker,
		scale:  time.Duration(src.TimeScale()),
		stream: streamOf(src),
		loop:   loop,
		items:  make(chan item, prefetch),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
		wake:   make(chan struct{}, 1),
	}
	go s.run()
	return s, nil
}

// streamOf returns the raw-stream source behind src, if that is what it is.
func streamOf(src hwmediacodec.PacketReader) *streamSource {
	switch v := src.(type) {
	case *streamSource:
		return v
	case *seekableStream:
		return v.streamSource
	}
	return nil
}

// requestSeek asks the goroutine to restart at target. A newer request
// replaces an older one that has not been picked up yet.
func (s *source) requestSeek(req seekRequest) {
	s.mu.Lock()
	s.pending = &req
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *source) takeSeek() *seekRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	req := s.pending
	s.pending = nil
	return req
}

type decodeResult struct {
	seek *seekRequest  // a seek interrupted decoding
	end  time.Duration // at EOF: the stream length (last frame time plus one frame)
}

func (s *source) run() {
	defer close(s.done)
	defer close(s.items)
	ctx := context.Background()
	gen, loop := 0, 0
	var rebase time.Duration
	dropBefore := time.Duration(-1)

	applySeek := func(req *seekRequest) error {
		// Throw away whatever the decoder still holds, then restart at the
		// keyframe. Frames between the keyframe and the target are decoded
		// and dropped, so the first frame shown is the one at the target.
		if err := s.discard(ctx); err != nil {
			return err
		}
		kt, err := s.seeker.SeekKeyframe(req.target)
		if err != nil {
			return fmt.Errorf("playback: seek: %w", err)
		}
		gen, loop, rebase = req.gen, 0, 0
		dropBefore = -1
		if req.target > kt {
			dropBefore = req.target
		}
		return nil
	}

	for {
		res, err := s.decode(ctx, gen, loop, rebase, dropBefore)
		if err != nil {
			if err != errStopped {
				s.setErr(err)
			}
			return
		}
		switch {
		case res.seek != nil:
			if err := applySeek(res.seek); err != nil {
				s.setErr(err)
				return
			}
		case s.loop:
			if _, err := s.seeker.SeekKeyframe(0); err != nil {
				s.setErr(fmt.Errorf("playback: rewind: %w", err))
				return
			}
			if err := s.frames.Reset(ctx); err != nil {
				s.setErr(err)
				return
			}
			loop++
			rebase, dropBefore = res.end, -1
		default:
			// The stream is over. Tell the player, then either finish or
			// wait for a seek that starts a new generation.
			if req, err := s.emit(item{end: true, gen: gen, loop: loop}); err != nil {
				return
			} else if req == nil {
				if s.seeker == nil {
					return
				}
				req = s.waitSeek()
				if req == nil {
					return
				}
				if err := applySeek(req); err != nil {
					s.setErr(err)
					return
				}
			} else if err := applySeek(req); err != nil {
				s.setErr(err)
				return
			}
		}
	}
}

// decode feeds packets from the source's current position until the end
// of the stream, a seek request or Close.
func (s *source) decode(ctx context.Context, gen, loop int, rebase, dropBefore time.Duration) (decodeResult, error) {
	var res decodeResult
	last, prev := time.Duration(-1), time.Duration(-1)
	// A raw elementary stream has no timestamps: its frames are fps apart
	// in display order, so they are timed as they come out of the decoder
	// (which reorders B-frames), counting from the keyframe a seek landed
	// on. Packets then only need distinct, increasing PTS.
	outIndex := 0
	if s.stream != nil {
		outIndex = s.stream.index
	}
	emitFrame := func(f *hwmediacodec.Frame) (*seekRequest, error) {
		pts := time.Duration(f.PTS) * time.Second / s.scale
		if s.stream != nil {
			pts = s.stream.frameTime(outIndex)
			outIndex++
		}
		if dropBefore >= 0 && pts < dropBefore {
			f.Release()
			return nil, nil
		}
		dropBefore = -1
		it := item{frame: f, pts: pts, gen: gen, loop: loop, rebase: rebase, release: f.Release}
		rebase = 0
		req, err := s.emit(it)
		if err == nil && req == nil {
			prev, last = last, pts
		}
		return req, err
	}
	for {
		select {
		case <-s.stop:
			return res, errStopped
		default:
		}
		if req := s.takeSeek(); req != nil {
			res.seek = req
			return res, nil
		}
		f, err := s.frames.ReadFrame(ctx)
		if err == io.EOF {
			break
		}
		if err != nil {
			return res, err
		}
		if req, err := emitFrame(f); req != nil || err != nil {
			res.seek = req
			return res, err
		}
	}
	switch {
	case s.seeker != nil && s.seeker.Length() > last:
		res.end = s.seeker.Length()
	case prev >= 0:
		res.end = last + (last - prev)
	case last >= 0:
		res.end = last
	}
	return res, nil
}

// discard flushes the decoder and releases everything it returns.
func (s *source) discard(ctx context.Context) error {
	return s.frames.Reset(ctx)
}

// emit hands an item to the player. It returns a seek request when one
// arrives while the channel is full (the item is released), or errStopped
// on Close.
func (s *source) emit(it item) (*seekRequest, error) {
	for {
		select {
		case s.items <- it:
			return nil, nil
		case <-s.stop:
			it.doRelease()
			return nil, errStopped
		case <-s.wake:
			if req := s.takeSeek(); req != nil {
				it.doRelease()
				return req, nil
			}
		}
	}
}

// waitSeek blocks after the end of the stream until a seek or Close.
func (s *source) waitSeek() *seekRequest {
	for {
		select {
		case <-s.stop:
			return nil
		case <-s.wake:
			if req := s.takeSeek(); req != nil {
				return req
			}
		}
	}
}

func (it item) doRelease() {
	if it.release != nil {
		it.release()
	}
}

// next returns the next item without blocking. ok is false when nothing is
// ready or the goroutine has finished.
func (s *source) next() (item, bool) {
	select {
	case it, ok := <-s.items:
		return it, ok
	default:
		return item{}, false
	}
}

func (s *source) setErr(err error) {
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
}

// Err returns the first decoding error, if any.
func (s *source) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// close stops the goroutine, releases undelivered frames and closes the
// decoder.
func (s *source) close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	close(s.stop)
	<-s.done
	for it := range s.items {
		it.doRelease()
	}
	return s.dec.Close()
}

// streamSource is the Source behind NewPlayer: a raw Annex-B elementary
// stream whose frames are fps apart. It seeks when the reader is an
// io.ReadSeeker, by scanning the stream once for keyframe offsets.
type streamSource struct {
	rd    io.Reader
	rs    io.ReadSeeker // nil when the reader cannot seek
	codec hwmediacodec.Codec
	fps   float64
	r     *annexb.Reader
	index int

	mu      sync.Mutex // guards the index below, read by Length from another goroutine
	scanned bool
	count   int
	keys    []streamKeyframe
}

type streamKeyframe struct {
	index  int
	offset int64
}

func newStreamSource(rd io.Reader, c hwmediacodec.Codec, fps float64) (*streamSource, error) {
	if c != hwmediacodec.H264 && c != hwmediacodec.HEVC {
		return nil, fmt.Errorf("playback: NewStream reads Annex-B streams only (%s needs a hwmediacodec.PacketReader, for example an MP4 demuxer)", c)
	}
	s := &streamSource{rd: rd, codec: c, fps: fps, r: annexb.NewReader(rd, c)}
	s.rs, _ = rd.(io.ReadSeeker)
	return s, nil
}

func (s *streamSource) frameTime(i int) time.Duration {
	return time.Duration(math.Round(float64(i) * float64(time.Second) / s.fps))
}

func (s *streamSource) Codec() hwmediacodec.Codec { return s.codec }

func (s *streamSource) TimeScale() int32 { return streamTimeScale }

func (s *streamSource) ReadPacket() (hwmediacodec.Packet, error) {
	au, err := s.r.Next()
	if err != nil {
		return hwmediacodec.Packet{}, err
	}
	pts := s.frameTime(s.index)
	s.index++
	return hwmediacodec.Packet{Data: au, PTS: int64(pts / time.Microsecond)}, nil
}

// seekable is the Seeker view of a streamSource, offered only when the
// reader can seek.
type seekableStream struct{ *streamSource }

func (s *seekableStream) SeekKeyframe(t time.Duration) (time.Duration, error) {
	offset, index := int64(0), 0
	if t > 0 {
		if err := s.scan(); err != nil {
			return 0, err
		}
		target := int(math.Floor(t.Seconds()*s.fps + 1e-9))
		for _, k := range s.keys {
			if k.index > target {
				break
			}
			offset, index = k.offset, k.index
		}
	}
	if _, err := s.rs.Seek(offset, io.SeekStart); err != nil {
		return 0, err
	}
	s.r = annexb.NewReader(s.rs, s.codec)
	s.index = index
	return s.frameTime(index), nil
}

func (s *seekableStream) Length() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.scanned {
		return 0
	}
	return s.frameTime(s.count)
}

// scan reads the whole stream once and records where the keyframes are.
func (s *streamSource) scan() error {
	s.mu.Lock()
	done := s.scanned
	s.mu.Unlock()
	if done {
		return nil
	}
	if _, err := s.rs.Seek(0, io.SeekStart); err != nil {
		return err
	}
	r := annexb.NewReader(s.rs, s.codec)
	var keys []streamKeyframe
	n := 0
	for {
		au, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		for _, nal := range annexb.Split(au) {
			if t := annexb.NALUnitType(s.codec, nal); annexb.IsVCL(s.codec, t) && annexb.IsKeyframe(s.codec, t) {
				keys = append(keys, streamKeyframe{index: n, offset: r.Offset()})
				break
			}
		}
		n++
	}
	s.mu.Lock()
	s.keys, s.count, s.scanned = keys, n, true
	s.mu.Unlock()
	return nil
}
