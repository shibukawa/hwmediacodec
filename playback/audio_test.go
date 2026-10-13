package playback

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"testing"
	"time"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
	"github.com/shibukawa/hwmediacodec/mediacontainer/mp4"
)

// These tests read an AudioStream the way an audio device does and check
// what lies where on its time line. They need an audio decoder (macOS, a
// browser) and ffmpeg, but no device.

// audioTrack encodes pcm with ffmpeg into an MP4 file and returns its
// audio track as a source.
func audioTrack(t *testing.T, pcm []int16, sampleRate, channels int, args ...string) *mp4.AudioPacketSource {
	t.Helper()
	if len(args) == 0 {
		args = []string{"-c:a", "aac", "-b:a", fmt.Sprint(96000 * min(channels, 2))}
	}
	path := testutil.EncodeAudio(t, pcm, sampleRate, channels, "in.mp4", args...)
	d, err := mp4.NewDemuxer(bytes.NewReader(testutil.ReadFile(t, path)))
	if err != nil {
		t.Fatal(err)
	}
	if d.Audio() == nil {
		t.Fatal("no audio track")
	}
	return d.Audio().PacketSource()
}

// openStream makes the stream of a source and starts it, or skips the
// test on a platform without a decoder for it.
func openStream(t *testing.T, src AudioSource, format hwmediacodec.SampleFormat, channels int, loop bool, length time.Duration) *AudioStream {
	t.Helper()
	s, err := newAudioStream(src, format, channels, loop)
	if errors.Is(err, hwmediacodec.ErrUnsupported) {
		t.Skipf("no audio decoder on this platform: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	s.start(length)
	t.Cleanup(s.close)
	return s
}

// readSamples reads n samples per channel from the stream in pieces of
// the size a device asks for and returns them as 16-bit samples.
func readSamples(t *testing.T, s *AudioStream, n int) []int16 {
	t.Helper()
	buf := make([]byte, n*s.frameBytes)
	deadline := time.Now().Add(20 * time.Second)
	for at := 0; at < len(buf); {
		got, err := s.Read(buf[at:min(at+2048*s.frameBytes, len(buf))])
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if got%s.frameBytes != 0 {
			t.Fatalf("Read returned %d bytes, not a whole number of samples", got)
		}
		at += got
		if time.Now().After(deadline) {
			t.Fatalf("the stream delivered %d of %d bytes in 20 s", at, len(buf))
		}
	}
	out := make([]int16, n*s.channels)
	for i := range out {
		if s.format == hwmediacodec.F32 {
			v := math.Float32frombits(binary.LittleEndian.Uint32(buf[i*4:]))
			out[i] = int16(max(-32768, min(32767, math.Round(float64(v)*32768))))
		} else {
			out[i] = int16(binary.LittleEndian.Uint16(buf[i*2:]))
		}
	}
	return out
}

// seekTo puts the stream at a sample of the time line.
func seekTo(t *testing.T, s *AudioStream, sample int) {
	t.Helper()
	off := int64(sample) * int64(s.frameBytes)
	if got, err := s.Seek(off, io.SeekStart); err != nil || got != off {
		t.Fatalf("Seek(%d) = %d, %v", off, got, err)
	}
}

// inPlace fails the test unless got is ref, sample for sample in place,
// as far as a lossy codec leaves it.
func inPlace(t *testing.T, what string, ref, got []int16, channels int) {
	t.Helper()
	shift, snr := testutil.AudioAlignment(ref, got, channels, 8)
	if shift != 0 || snr < 15 {
		t.Errorf("%s: best SNR %.1f dB at a shift of %d samples", what, snr, shift)
	}
}

func silent(s []int16) bool {
	for _, v := range s {
		if v != 0 {
			return false
		}
	}
	return true
}

// TestAudioStreamTimeLine: the byte at offset n is the sample of time n,
// from the start, after a jump and behind the end of the track.
func TestAudioStreamTimeLine(t *testing.T) {
	const rate, channels = 48000, 2
	pcm := testutil.AudioSignal(rate, channels, rate*2)
	s := openStream(t, audioTrack(t, pcm, rate, channels), hwmediacodec.F32, 0, false, 2500*time.Millisecond)
	if s.SampleRate() != rate || s.Channels() != channels || s.Format() != hwmediacodec.F32 || s.BytesPerSecond() != rate*channels*4 {
		t.Fatalf("stream: %d Hz, %d channels, %s, %d bytes per second", s.SampleRate(), s.Channels(), s.Format(), s.BytesPerSecond())
	}
	if at, err := s.Seek(0, io.SeekCurrent); at != 0 || err != nil {
		t.Fatalf("a new stream is at %d (%v)", at, err)
	}

	// The whole track, and half a second more: the encoder's delay is
	// gone, the track's padding too, and what follows is silence.
	all := readSamples(t, s, rate*5/2)
	inPlace(t, "from the start", pcm, all[:len(pcm)], channels)
	if tail := all[len(pcm):]; !silent(tail) {
		t.Errorf("behind the end of the track the stream is not silent")
	}
	if at, _ := s.Seek(0, io.SeekCurrent); at != int64(rate*5/2*channels*4) {
		t.Errorf("after reading 2.5 s the stream is at byte %d", at)
	}

	// Jumps, backwards and forwards and onto odd samples.
	for _, first := range []int{59233, 0, 95999, 481, 33600} {
		seekTo(t, s, first)
		n := min(rate/2, len(pcm)/channels-first)
		got := readSamples(t, s, n)
		// The decoder has run in by the time the target is reached, but
		// the first samples after a jump may still differ a little.
		const settle = 256
		if n > 2*settle {
			inPlace(t, fmt.Sprintf("after a jump to sample %d", first), pcm[(first+settle)*channels:(first+n)*channels], got[settle*channels:], channels)
		}
	}
	if _, err := s.Seek(0, io.SeekEnd); err == nil {
		t.Error("a seek from the end succeeded")
	}
	if _, err := s.Seek(-4, io.SeekStart); err == nil {
		t.Error("a seek before the start succeeded")
	}
	if err := s.Err(); err != nil {
		t.Errorf("stream error: %v", err)
	}
}

// TestAudioStreamLoops: with a pass longer than the track, the track is
// followed by silence up to the end of the pass and starts again there,
// pass after pass, and a jump into a later pass lands in it.
func TestAudioStreamLoops(t *testing.T) {
	const rate, channels = 48000, 2
	const pass = rate*3/2 + 100 // samples: the track's 1.5 s and a little
	pcm := testutil.AudioSignal(rate, channels, rate*3/2-4800)
	length := time.Duration(pass) * time.Second / rate
	s := openStream(t, audioTrack(t, pcm, rate, channels), hwmediacodec.S16, 0, true, length)
	all := readSamples(t, s, pass*3)
	for k := 0; k < 3; k++ {
		one := all[k*pass*channels : (k+1)*pass*channels]
		inPlace(t, fmt.Sprintf("pass %d", k), pcm, one[:len(pcm)], channels)
		if !silent(one[len(pcm):]) {
			t.Errorf("pass %d: the stream is not silent between the end of the track and the end of the pass", k)
		}
	}
	// Into the middle of the sixth pass.
	const k, into = 5, rate / 2
	seekTo(t, s, k*pass+into)
	got := readSamples(t, s, rate/2)
	inPlace(t, "after a jump into a later pass", pcm[(into+256)*channels:(into+rate/2)*channels], got[256*channels:], channels)
}

// TestAudioStreamChannels: a mono track on both sides of a stereo stream,
// and the weights of the mix from more channels.
func TestAudioStreamChannels(t *testing.T) {
	const rate = 44100
	pcm := testutil.AudioSignal(rate, 1, rate)
	s := openStream(t, audioTrack(t, pcm, rate, 1), hwmediacodec.S16, 2, false, time.Second)
	if s.Channels() != 2 || s.SampleRate() != rate {
		t.Fatalf("stream: %d Hz, %d channels", s.SampleRate(), s.Channels())
	}
	got := readSamples(t, s, rate)
	left := make([]int16, rate)
	for i := range left {
		left[i] = got[i*2]
		if got[i*2] != got[i*2+1] {
			t.Fatalf("sample %d differs between the sides: %d and %d", i, got[i*2], got[i*2+1])
		}
	}
	inPlace(t, "mono on both sides", pcm, left, 1)

	// The mix itself, on made-up frames: every channel of the track in
	// turn at full scale.
	side, other := float32(1/(1+math.Sqrt2)), float32(1/(1+math.Sqrt2)/math.Sqrt2)
	for _, c := range []struct {
		from int
		want [][2]float32 // left and right for a full-scale sample in each channel
	}{
		{1, [][2]float32{{1, 1}}},
		{2, [][2]float32{{1, 0}, {0, 1}}},
		{3, [][2]float32{{side, 0}, {0, side}, {other, other}}},
		{5, [][2]float32{{side, 0}, {0, side}, {other, other}, {other, 0}, {0, other}}},
		{6, [][2]float32{{side, 0}, {0, side}, {other, other}, {0, 0}, {other, 0}, {0, other}}},
	} {
		mix := &AudioStream{format: hwmediacodec.F32, channels: 2, srcChannels: c.from, frameBytes: 8}
		in := make([]float32, c.from*c.from)
		for ch := range c.from {
			in[ch*c.from+ch] = 1
		}
		out := mix.convert(in)
		var sumL, sumR float32
		for ch, want := range c.want {
			l := math.Float32frombits(binary.LittleEndian.Uint32(out[ch*8:]))
			r := math.Float32frombits(binary.LittleEndian.Uint32(out[ch*8+4:]))
			if math.Abs(float64(l-want[0])) > 1e-6 || math.Abs(float64(r-want[1])) > 1e-6 {
				t.Errorf("%d channels to stereo: channel %d goes to (%g, %g), want (%g, %g)", c.from, ch, l, r, want[0], want[1])
			}
			sumL, sumR = sumL+l, sumR+r
		}
		if sumL > 1.000001 || sumR > 1.000001 {
			t.Errorf("%d channels to stereo: full scale everywhere gives (%g, %g), which clips", c.from, sumL, sumR)
		}
		// To mono: the mean of the two sides, here of the first channel
		// at full scale.
		mono := &AudioStream{format: hwmediacodec.S16, channels: 1, srcChannels: c.from, frameBytes: 2}
		want := int16(min(32767, math.Round(float64(c.want[0][0]+c.want[0][1])/2*32768)))
		if v := int16(binary.LittleEndian.Uint16(mono.convert(in[:c.from]))); v != want {
			t.Errorf("%d channels to mono: the first channel at full scale gives %d, want %d", c.from, v, want)
		}
	}
	if _, err := newAudioStream(audioTrack(t, pcm, rate, 1), hwmediacodec.F32, 6, false); err == nil {
		t.Error("a mono track was accepted for a stream of six channels")
	}
}

// TestAudioStreamWithoutDecoder: a track no decoder takes gives no stream
// and an error that says so, which Player.AudioErr hands on.
func TestAudioStreamWithoutDecoder(t *testing.T) {
	stream, err := newAudioStream(noDecoderSource{}, hwmediacodec.F32, 0, false)
	if stream != nil || !errors.Is(err, hwmediacodec.ErrUnsupported) {
		t.Fatalf("a stream for a video codec: %v, %v", stream, err)
	}
}

// noDecoderSource announces a format no audio decoder takes.
type noDecoderSource struct{}

func (noDecoderSource) Codec() hwmediacodec.Codec { return hwmediacodec.H264 }
func (noDecoderSource) TimeScale() int32          { return 48000 }
func (noDecoderSource) ReadPacket() (hwmediacodec.Packet, error) {
	return hwmediacodec.Packet{}, io.EOF
}
func (noDecoderSource) Format() hwmediacodec.AudioFormat {
	return hwmediacodec.AudioFormat{Codec: hwmediacodec.H264, SampleRate: 48000, Channels: 2}
}
