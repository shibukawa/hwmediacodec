package mp4_test

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/internal/mediatest"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
	"github.com/shibukawa/hwmediacodec/mediacontainer/mp4"
)

// These tests are about what the container does with an audio track:
// seeking in it and placing it next to a video track. The tests of the
// codecs through MP4 files are in the root package (audio_mp4_test.go),
// where they also run in a browser.

func audioDecoderOrSkip(t *testing.T, format hwmediacodec.AudioFormat) hwmediacodec.AudioDecoder {
	t.Helper()
	dec, err := hwmediacodec.NewAudioDecoder(context.Background(), format)
	if errors.Is(err, hwmediacodec.ErrUnsupported) {
		t.Skipf("no %s decoder on this platform: %v", format.Codec, err)
	}
	if err != nil {
		t.Fatalf("NewAudioDecoder: %v", err)
	}
	t.Cleanup(func() { dec.Close() })
	return dec
}

func audioEncoderOrSkip(t *testing.T, c hwmediacodec.Codec, sampleRate, channels int, opts ...hwmediacodec.AudioEncoderOption) hwmediacodec.AudioEncoder {
	t.Helper()
	enc, err := hwmediacodec.NewAudioEncoder(context.Background(), c, sampleRate, channels, opts...)
	if errors.Is(err, hwmediacodec.ErrUnsupported) {
		t.Skipf("no %s encoder on this platform: %v", c, err)
	}
	if err != nil {
		t.Fatalf("NewAudioEncoder: %v", err)
	}
	t.Cleanup(func() { enc.Close() })
	return enc
}

// presented joins the samples of frames that lie in the presentation: from
// PTS 0 (in units of timeScale) up to length.
func presented(frames []*hwmediacodec.AudioFrame, timeScale int32, length time.Duration) []int16 {
	var out []int16
	for _, f := range frames {
		s := f.Int16s()
		// In samples; length was cut to whole nanoseconds on the way.
		start := f.PTS * int64(f.SampleRate) / int64(timeScale)
		end := (int64(length)*int64(f.SampleRate) + int64(time.Second)/2) / int64(time.Second)
		if start < 0 {
			s = s[min(int(-start)*f.Channels, len(s)):]
			start = 0
		}
		if n := end - start; n < int64(len(s)/f.Channels) {
			s = s[:max(n, 0)*int64(f.Channels)]
		}
		out = append(out, s...)
	}
	return out
}

// TestAudioPacketSourceSeek jumps into the middle of a track: decoding
// from where the source positions itself and dropping what lies before
// the target gives the samples a decoding from the start has there.
func TestAudioPacketSourceSeek(t *testing.T) {
	const sampleRate, channels = 48000, 2
	for _, c := range []struct {
		name string
		args []string
	}{
		{"aac", []string{"-c:a", "aac", "-b:a", "128k"}},
		{"opus", []string{"-c:a", "libopus", "-b:a", "96k"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.name == "opus" && !testutil.HasEncoder(t, "libopus") {
				t.Skip("ffmpeg has no libopus")
			}
			pcm := testutil.AudioSignal(sampleRate, channels, sampleRate*3)
			path := testutil.EncodeAudio(t, pcm, sampleRate, channels, "in.mp4", c.args...)
			d, err := mp4.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			a := d.Audio()
			dec := audioDecoderOrSkip(t, a.Format())
			src := a.PacketSource()
			whole := presented(mediatest.DecodeAudio(t, dec, src), src.TimeScale(), src.Length())

			for _, target := range []time.Duration{0, 10 * time.Millisecond, 1234 * time.Millisecond, 2900 * time.Millisecond, 10 * time.Second} {
				at, err := src.SeekKeyframe(target)
				if err != nil {
					t.Fatal(err)
				}
				if at > target {
					t.Errorf("seek to %v positions the source at %v, after the target", target, at)
				}
				frames := mediatest.DecodeAudio(t, dec, src)
				if len(frames) == 0 {
					t.Fatalf("seek to %v: nothing decoded", target)
				}
				if got := time.Duration(frames[0].PTS) * time.Second / time.Duration(src.TimeScale()); got != at {
					t.Errorf("seek to %v: the first frame is at %v, SeekKeyframe said %v", target, got, at)
				}
				// Cut at the target, as a player would.
				first := max(int(target*sampleRate/time.Second), 0)
				first = min(first, len(whole)/channels)
				var part []int16
				for _, f := range frames {
					start := int(f.PTS * sampleRate / int64(src.TimeScale()))
					s := f.Int16s()
					if skip := first - start; skip > 0 {
						s = s[min(skip*channels, len(s)):]
					}
					part = append(part, s...)
				}
				want := whole[first*channels:]
				part = part[:min(len(part), len(want))]
				// (Opus runs in over the 80 ms before the target and is
				// then close to, not identical with, the first decoding.)
				if snr := testutil.AudioSNR(want, part); len(part) != len(want) || snr < 20 {
					t.Errorf("seek to %v (decoding from %v): %d samples for %d, %.1f dB against a decoding from the start", target, at, len(part)/channels, len(want)/channels, snr)
				}
			}
		})
	}
}

// TestMuxAudioWithVideo writes a video and an audio track from packets
// whose timestamps share a clock but not a start: the track that starts
// later must start later in the file too, by the same amount.
func TestMuxAudioWithVideo(t *testing.T) {
	const sampleRate, channels = 48000, 2
	dir := t.TempDir()
	srcPath := mediatest.GenerateMP4(t, dir, mediatest.MP4Options{Codec: hwmediacodec.H264, Width: 160, Height: 120, Frames: 60, BFrames: 2, GOP: 15})
	in, err := mp4.Open(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	v := in.Video()
	_, firstPTS, _ := v.Info(0)
	for i := range v.SampleCount() {
		_, pts, _ := v.Info(i)
		firstPTS = min(firstPTS, pts)
	}
	pcm := testutil.AudioSignal(sampleRate, channels, sampleRate*2)

	for _, c := range []struct {
		name string
		// lead is how long after the first picture the sound starts
		// (before it when negative).
		lead time.Duration
		// epoch is where both clocks start counting, in seconds: far from
		// zero for timestamps taken from a wall clock.
		epoch int64
	}{
		{"together", 0, 0},
		{"audio_later", 500 * time.Millisecond, 0},
		{"video_later", -250 * time.Millisecond, 0},
		{"wall_clock", 500 * time.Millisecond, 1_760_000_000},
	} {
		t.Run(c.name, func(t *testing.T) {
			enc := audioEncoderOrSkip(t, hwmediacodec.AAC, sampleRate, channels)
			path := filepath.Join(dir, c.name+".mp4")
			m, err := mp4.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			vw, err := m.AddVideoTrack(v.Codec, v.TimeScale)
			if err != nil {
				t.Fatal(err)
			}
			aw, err := m.AddAudioTrack(enc.Format(), sampleRate)
			if err != nil {
				t.Fatal(err)
			}
			for i := range v.SampleCount() {
				p, err := v.Packet(i)
				if err != nil {
					t.Fatal(err)
				}
				p.PTS += c.epoch * int64(v.TimeScale)
				p.DTS += c.epoch * int64(v.TimeScale)
				if err := vw.WritePacket(p); err != nil {
					t.Fatal(err)
				}
			}
			// The sound's first sample on the video's clock, in samples.
			start := c.epoch*sampleRate + firstPTS*sampleRate/int64(v.TimeScale) + int64(c.lead*sampleRate/time.Second)
			mediatest.EncodeAudio(t, enc, pcm, 4800, start, aw)
			if err := m.Close(); err != nil {
				t.Fatal(err)
			}

			mediatest.CheckDecodes(t, path)
			vs, as := mediatest.VideoStream(t, path), mediatest.AudioStream(t, path)
			wantV, wantA := max(-c.lead, 0).Seconds(), max(c.lead, 0).Seconds()
			t.Logf("video from %.4f s for %.4f s, audio from %.4f s for %.4f s", vs.Start(), vs.Seconds(), as.Start(), as.Seconds())
			if math.Abs(vs.Start()-wantV) > 0.0015 || math.Abs(as.Start()-wantA) > 0.0015 {
				t.Errorf("video starts at %.4f s and audio at %.4f s; want %.4f and %.4f", vs.Start(), as.Start(), wantV, wantA)
			}
			// (Behind an empty edit ffmpeg stops reading the end of the
			// edit that follows: ffprobe counts the encoder's delay into
			// the duration of the stream and ffmpeg plays the padding of
			// the last packet. The start, which is what the edits are
			// for, is right.)
			wantDur := 2.0
			if c.lead > 0 {
				wantDur += float64(enc.Format().Delay) / sampleRate
			}
			if vs.FrameCount() != v.SampleCount() || math.Abs(as.Seconds()-wantDur) > 0.002 {
				t.Errorf("%d pictures and %.4f s of sound; want %d and %.4f s", vs.FrameCount(), as.Seconds(), v.SampleCount(), wantDur)
			}
			// The demuxer reads the offset back: both packet sources
			// count from the start of the presentation.
			out, err := mp4.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer out.Close()
			vsrc, asrc := out.Video().PacketSource(), out.Audio().PacketSource()
			firstV := time.Duration(math.MaxInt64)
			for {
				p, err := vsrc.ReadPacket()
				if err != nil {
					break
				}
				firstV = min(firstV, time.Duration(p.PTS)*time.Second/time.Duration(vsrc.TimeScale()))
			}
			ap, err := asrc.ReadPacket()
			if err != nil {
				t.Fatal(err)
			}
			firstA := time.Duration(ap.PTS+int64(asrc.Format().Delay)) * time.Second / sampleRate
			if d := firstA - firstV - c.lead; d < -2*time.Millisecond || d > 2*time.Millisecond {
				t.Errorf("read back: pictures from %v, sound from %v; want the sound %v after the pictures", firstV, firstA, c.lead)
			}
			if min(firstV, firstA) < 0 || min(firstV, firstA) > 2*time.Millisecond {
				t.Errorf("read back: pictures from %v, sound from %v; one of them should start the presentation", firstV, firstA)
			}

			// The sound itself is untouched by the offset. (What ffmpeg
			// plays beyond it is the padding of the last packet: behind
			// an empty edit, and in ffmpeg 7 and older always.)
			heard := testutil.DecodeAudio(t, path, false, "")
			extra := len(heard) - len(pcm)
			if shift, snr := testutil.AudioAlignment(pcm, heard, channels, 8); extra < 0 || extra >= 1024*channels || shift != 0 || snr < 20 {
				t.Errorf("ffmpeg plays %d samples for %d: best SNR %.1f dB at a shift of %d samples", len(heard)/channels, len(pcm)/channels, snr, shift)
			}
		})
	}
}
