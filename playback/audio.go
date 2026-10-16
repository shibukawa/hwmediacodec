package playback

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"
	"time"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/internal/codec"
)

// AudioSource hands out the packets of an audio track together with the
// description a decoder needs. *mp4.AudioPacketSource is one. A source
// that is also a hwmediacodec.PacketSeeker lets the sound follow Seek and
// looping.
type AudioSource interface {
	hwmediacodec.PacketReader
	// Format describes the stream (see hwmediacodec.AudioFormat).
	Format() hwmediacodec.AudioFormat
}

// AudioDevice is what plays a Player's AudioStream: an audio player of the
// platform or of a game library, wrapped for the Player to steer. With one
// attached (Player.AttachAudioDevice) the sound is the clock: the picture
// shown is the one for the sample at the loudspeaker.
type AudioDevice interface {
	// Ready reports whether the device takes samples. A device that is
	// not there yet (a browser waits for the user's first click before it
	// lets a page make sound) leaves the Player on the clock of Advance.
	Ready() bool
	// Position is the time, on the stream's own time line, of the sample
	// that is at the loudspeaker now: the bytes the device has read from
	// the stream, less the ones it still holds, as a duration. It follows
	// SetPosition and does not move while the device is paused.
	Position() time.Duration
	// SetPosition drops what the device holds and makes it go on from t:
	// it seeks the stream to the byte offset of t.
	SetPosition(t time.Duration) error
	// Play and Pause start and stop the device. A paused device keeps
	// its position and what it has read.
	Play()
	Pause()
}

// AudioStream is the sound of a Player as raw samples: interleaved, little
// endian, in the format, rate and channel count its methods report. It is
// an io.ReadSeeker laid out on the time line of the pictures, one second
// of samples per second of presentation: the byte at offset n belongs to
// the time n / (bytes per second), whatever happened before it. Where the
// track has nothing (before its start, after its end, while the decoder
// catches up after a jump) the stream holds silence, and a loop goes on
// behind the end of the presentation. Read never reports io.EOF.
//
// An audio device plays the stream and reports how far it has got; see
// AudioDevice. Read and Seek are safe to call from the device's
// goroutine.
type AudioStream struct {
	src    AudioSource
	seeker hwmediacodec.PacketSeeker // nil when the source cannot seek
	dec    hwmediacodec.AudioDecoder
	scale  int64 // PTS units per second

	format      hwmediacodec.SampleFormat
	rate        int
	channels    int
	srcChannels int
	frameBytes  int // one sample of every channel

	loop bool
	// length is the length of one pass through the presentation in
	// samples, and track the length of the audio track itself, which may
	// be shorter; 0 when not known.
	length int64
	track  int64

	mu sync.Mutex
	// chunks are decoded samples in the order of the time line; start is
	// the index of a chunk's first sample on it.
	chunks []audioChunk
	// pos is the sample the next Read starts at.
	pos int64
	// gen counts Seek calls: the decoding goroutine starts over at pos
	// when it changes.
	gen    uint64
	ended  bool // the track is over and the stream does not loop
	err    error
	closed bool

	wake  chan struct{} // for the decoding goroutine: a Seek, or room in the buffer
	ready chan struct{} // for Read: samples arrived
	done  chan struct{}
}

type audioChunk struct {
	start int64
	data  []byte
}

// audioAhead is how far decoding runs ahead of the reader, as a fraction
// of a second.
const audioAhead = 2

// audioWait is how long Read waits for the decoder before it hands out
// silence instead.
const audioWait = 30 * time.Millisecond

func newAudioStream(src AudioSource, format hwmediacodec.SampleFormat, channels int, loop bool) (*AudioStream, error) {
	f := src.Format()
	dec, err := hwmediacodec.NewAudioDecoder(context.Background(), f, hwmediacodec.WithSampleFormat(hwmediacodec.F32))
	if err != nil {
		return nil, err
	}
	if f.SampleRate <= 0 || f.Channels <= 0 || src.TimeScale() <= 0 {
		dec.Close()
		return nil, fmt.Errorf("playback: the audio source gives no sample rate, channel count or time scale (%d Hz, %d channels, %d)", f.SampleRate, f.Channels, src.TimeScale())
	}
	if channels <= 0 {
		channels = f.Channels
	}
	if channels != f.Channels && channels > 2 {
		dec.Close()
		return nil, fmt.Errorf("playback: %d channels cannot be made of %d; mixing goes to mono and stereo", channels, f.Channels)
	}
	s := &AudioStream{
		src:         src,
		dec:         dec,
		scale:       int64(src.TimeScale()),
		format:      format,
		rate:        f.SampleRate,
		channels:    channels,
		srcChannels: f.Channels,
		frameBytes:  channels * format.BytesPerSample(),
		loop:        loop,
		wake:        make(chan struct{}, 1),
		ready:       make(chan struct{}, 1),
		done:        make(chan struct{}),
	}
	s.seeker, _ = src.(hwmediacodec.PacketSeeker)
	return s, nil
}

// start sets the length of one pass and begins decoding.
func (s *AudioStream) start(length time.Duration) {
	s.length = int64(math.Round(length.Seconds() * float64(s.rate)))
	if s.length <= 0 || s.seeker == nil {
		s.loop = false
	}
	if s.seeker != nil {
		s.track = int64(math.Round(s.seeker.Length().Seconds() * float64(s.rate)))
	}
	go s.run()
}

// SampleRate is the number of samples per second and channel.
func (s *AudioStream) SampleRate() int { return s.rate }

// Channels is the number of interleaved channels.
func (s *AudioStream) Channels() int { return s.channels }

// Format is the sample format: F32 unless WithAudioFormat said otherwise.
func (s *AudioStream) Format() hwmediacodec.SampleFormat { return s.format }

// BytesPerSecond is the size of one second of the stream, which is what
// turns a byte offset into a time.
func (s *AudioStream) BytesPerSecond() int { return s.rate * s.frameBytes }

// Err returns the error that silenced the stream, if any.
func (s *AudioStream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// Read implements io.Reader. It hands out what the time line holds from
// the stream's position on: as much as is decoded, or, when nothing is
// and a moment's wait does not change that, a little silence. So a read
// may come back short, and samples that are not there in time are lost.
func (s *AudioStream) Read(p []byte) (int, error) {
	n := len(p) / s.frameBytes
	if n == 0 {
		return 0, nil
	}
	p = p[:n*s.frameBytes]
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.buffered() && !s.over() {
		// Just started, or just jumped: give the decoder a moment. (A
		// signal left from samples that are gone by now does not count.)
		select {
		case <-s.ready:
		default:
		}
		s.mu.Unlock()
		timer := time.NewTimer(audioWait)
		select {
		case <-s.ready:
		case <-timer.C:
		}
		timer.Stop()
		s.mu.Lock()
	}
	cur, out := s.pos, p
	for len(out) > 0 && s.buffered() {
		c := s.chunks[0]
		if c.start > cur {
			// Nothing of the track here: before its start, behind its
			// end, or lost at a jump.
			gap := min(int(c.start-cur)*s.frameBytes, len(out))
			clear(out[:gap])
			out = out[gap:]
			cur += int64(gap / s.frameBytes)
			continue
		}
		copied := copy(out, c.data[int(cur-c.start)*s.frameBytes:])
		out = out[copied:]
		cur += int64(copied / s.frameBytes)
		s.pos = cur
	}
	read := len(p) - len(out)
	if read == 0 || s.over() {
		// Nothing more to give: silence, all that is asked for once the
		// track is over and only a little while the decoder may still
		// deliver.
		if !s.over() {
			out = out[:min(len(out), s.rate/100*s.frameBytes)]
		}
		clear(out)
		read += len(out)
		cur += int64(len(out) / s.frameBytes)
	}
	s.pos = cur
	signal(s.wake)
	return read, nil
}

// buffered drops the chunks the reader has passed and reports whether
// one is left. The caller holds the lock.
func (s *AudioStream) buffered() bool {
	for len(s.chunks) > 0 && s.chunks[0].start+int64(len(s.chunks[0].data)/s.frameBytes) <= s.pos {
		s.chunks = s.chunks[1:]
	}
	return len(s.chunks) > 0
}

// over reports that no more samples will come: the track has ended, or
// the stream has failed or is closed. The caller holds the lock.
func (s *AudioStream) over() bool { return s.ended || s.closed || s.err != nil }

// Seek implements io.Seeker: the stream goes on from the given byte
// offset, which stands for a time (see AudioStream). Offsets are taken
// from the start or from the current position; the stream has no end to
// count from.
func (s *AudioStream) Seek(offset int64, whence int) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, errors.New("playback: the audio stream is closed")
	}
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		offset += s.pos * int64(s.frameBytes)
	default:
		return 0, errors.New("playback: an audio stream has no end to seek from")
	}
	if offset < 0 {
		return 0, errors.New("playback: seek before the start of the audio stream")
	}
	pos := offset / int64(s.frameBytes)
	if pos != s.pos {
		s.pos = pos
		s.gen++
		s.chunks = nil
		s.ended = false
		signal(s.wake)
	}
	return pos * int64(s.frameBytes), nil
}

func (s *AudioStream) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.chunks = nil
	s.mu.Unlock()
	signal(s.wake)
	<-s.done
	s.dec.Close()
}

// fail silences the stream for good: a film goes on without its sound.
func (s *AudioStream) fail(err error) {
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.chunks = nil
	s.mu.Unlock()
}

// run is the decoding goroutine: it keeps the time line filled a little
// ahead of the reader, and starts over where the reader is after a Seek.
func (s *AudioStream) run() {
	defer close(s.done)
	ctx := context.Background()
	var (
		handled uint64 // the Seek generation decoding follows
		pass    int64  // how many times the presentation has been played through
		started bool
	)
	for {
		s.mu.Lock()
		for !s.closed && s.err == nil && (started && s.gen == handled) && (s.ended || s.full()) {
			s.mu.Unlock()
			<-s.wake
			s.mu.Lock()
		}
		if s.closed || s.err != nil {
			s.mu.Unlock()
			return
		}
		restart, target := !started || s.gen != handled, s.pos
		handled = s.gen
		s.mu.Unlock()

		if restart {
			started = true
			var err error
			if pass, err = s.restart(ctx, target); err != nil {
				s.fail(err)
				return
			}
			continue
		}
		p, err := s.src.ReadPacket()
		if err == io.EOF {
			if err := s.drain(ctx, handled, pass); err != nil {
				s.fail(err)
				return
			}
			if !s.loop {
				s.mu.Lock()
				if s.gen == handled {
					s.ended = true
				}
				s.mu.Unlock()
				continue
			}
			if _, err := s.seeker.SeekKeyframe(0); err != nil {
				s.fail(fmt.Errorf("playback: rewind audio: %w", err))
				return
			}
			pass++
			continue
		}
		if err == nil {
			err = s.decode(ctx, p, handled, pass)
		}
		if err != nil {
			s.fail(err)
			return
		}
	}
}

// full reports whether decoding is far enough ahead of the reader. The
// caller holds the lock.
func (s *AudioStream) full() bool {
	if len(s.chunks) == 0 {
		return false
	}
	last := s.chunks[len(s.chunks)-1]
	return last.start+int64(len(last.data)/s.frameBytes)-s.pos >= int64(s.rate/audioAhead)
}

// restart puts the source where the sample target of the time line is
// decoded from and empties the decoder. It returns the pass target lies
// in.
func (s *AudioStream) restart(ctx context.Context, target int64) (pass int64, err error) {
	local := target
	if s.loop {
		pass = target / s.length
		local = target - pass*s.length
	}
	if s.seeker != nil {
		// The source positions itself a little before the time asked for,
		// by what the decoder needs to run in; append drops what lies
		// before the reader.
		at := time.Duration(local) * time.Second / time.Duration(s.rate)
		if _, err := s.seeker.SeekKeyframe(at); err != nil {
			return 0, fmt.Errorf("playback: seek audio: %w", err)
		}
	}
	if err := s.dec.Flush(ctx); err != nil {
		return 0, err
	}
	for {
		if _, err := s.dec.Receive(ctx); err != nil {
			break
		}
	}
	return pass, nil
}

func (s *AudioStream) decode(ctx context.Context, p hwmediacodec.Packet, gen uint64, pass int64) error {
	for {
		err := s.dec.Send(ctx, p)
		if errors.Is(err, hwmediacodec.ErrAgain) {
			if err := s.receive(ctx, gen, pass); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		return s.receive(ctx, gen, pass)
	}
}

// drain gets the frames a decoder still holds at the end of the track.
func (s *AudioStream) drain(ctx context.Context, gen uint64, pass int64) error {
	if err := s.dec.Flush(ctx); err != nil {
		return err
	}
	return s.receive(ctx, gen, pass)
}

func (s *AudioStream) receive(ctx context.Context, gen uint64, pass int64) error {
	for {
		f, err := s.dec.Receive(ctx)
		if errors.Is(err, hwmediacodec.ErrAgain) || err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := s.append(f, gen, pass); err != nil {
			return err
		}
	}
}

// append puts a decoded frame on the time line: at the place its PTS
// names within the pass, cut to the presentation (the samples before its
// start are an encoder's delay, the ones behind its end padding) and to
// what the reader has not passed yet.
func (s *AudioStream) append(f *hwmediacodec.AudioFrame, gen uint64, pass int64) error {
	if f.SampleRate != s.rate || f.Channels != s.srcChannels {
		return fmt.Errorf("playback: the audio decoder returns %d Hz with %d channels where the track announced %d Hz with %d", f.SampleRate, f.Channels, s.rate, s.srcChannels)
	}
	samples := f.Float32s()
	n := int64(len(samples) / s.srcChannels)
	start := codec.Rescale(f.PTS, s.scale, int64(s.rate))
	skip := int64(0)
	if start < 0 {
		skip = min(-start, n)
	}
	if s.track > 0 {
		n = min(n, s.track-start)
	}
	if s.length > 0 {
		n = min(n, s.length-start)
	}
	if n <= skip {
		return nil
	}
	start += pass * s.length

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gen != gen || s.closed {
		return nil
	}
	if behind := s.pos - start; behind > skip {
		skip = min(behind, n)
	}
	if n <= skip {
		return nil
	}
	data := s.convert(samples[skip*int64(s.srcChannels) : n*int64(s.srcChannels)])
	s.chunks = append(s.chunks, audioChunk{start: start + skip, data: data})
	signal(s.ready)
	return nil
}

// Downmix weights after ITU-R BS.775: the centre and the surrounds go
// into both sides at -3 dB, the LFE channel is left out, and the sum is
// scaled so that it cannot clip.
const (
	mixSide  = 1 / (1 + math.Sqrt2)
	mixOther = mixSide / math.Sqrt2
)

// convert turns interleaved float samples of the track's channels into
// the stream's channels and sample format.
func (s *AudioStream) convert(in []float32) []byte {
	n := len(in) / s.srcChannels
	out := make([]byte, n*s.frameBytes)
	put := func(i int, v float32) {
		if s.format == hwmediacodec.F32 {
			binary.LittleEndian.PutUint32(out[i*4:], math.Float32bits(v))
			return
		}
		binary.LittleEndian.PutUint16(out[i*2:], uint16(int16(max(-32768, min(32767, math.Round(float64(v)*32768))))))
	}
	for i := range n {
		frame := in[i*s.srcChannels : (i+1)*s.srcChannels]
		if s.channels == s.srcChannels {
			for c, v := range frame {
				put(i*s.channels+c, v)
			}
			continue
		}
		// To stereo first: the channels are in the order of a WAV file
		// (left, right, centre, LFE, surrounds).
		var l, r float32
		switch s.srcChannels {
		case 1:
			l, r = frame[0], frame[0]
		case 2:
			l, r = frame[0], frame[1]
		case 3: // L R C
			l, r = mixSide*frame[0]+mixOther*frame[2], mixSide*frame[1]+mixOther*frame[2]
		case 4: // L R C Cs
			l = mixSide*frame[0] + mixOther*(frame[2]+frame[3])
			r = mixSide*frame[1] + mixOther*(frame[2]+frame[3])
		case 5: // L R C Ls Rs
			l = mixSide*frame[0] + mixOther*(frame[2]+frame[3])
			r = mixSide*frame[1] + mixOther*(frame[2]+frame[4])
		default: // L R C LFE Ls Rs
			l = mixSide*frame[0] + mixOther*(frame[2]+frame[4])
			r = mixSide*frame[1] + mixOther*(frame[2]+frame[5])
		}
		if s.channels == 1 {
			put(i, (l+r)/2)
			continue
		}
		put(i*2, l)
		put(i*2+1, r)
	}
	return out
}
