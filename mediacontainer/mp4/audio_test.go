package mp4_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/internal/mediatest"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
	"github.com/shibukawa/hwmediacodec/mediacontainer/mp4"
)

// These tests decode and encode with the operating system's audio codecs
// and use ffmpeg for the other half of every comparison.

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

// TestAudioTrackDecode demuxes and decodes the audio of files made by
// ffmpeg: every sample must match ffmpeg's own decoder, and the part the
// edit list presents must be the signal that went in, in place.
func TestAudioTrackDecode(t *testing.T) {
	cases := []struct {
		name                 string
		codec                hwmediacodec.Codec
		sampleRate, channels int
		args                 []string
		decoder              string
		delay                int
		frame                int
		minSNR               float64 // against ffmpeg's decoder
	}{
		{"aac", hwmediacodec.AAC, 48000, 2, []string{"-c:a", "aac", "-b:a", "128k"}, "aac", 1024, 1024, 50},
		{"aac_44100_mono", hwmediacodec.AAC, 44100, 1, []string{"-c:a", "aac", "-b:a", "96k"}, "aac", 1024, 1024, 50},
		{"opus", hwmediacodec.Opus, 48000, 2, []string{"-c:a", "libopus", "-b:a", "96k"}, "libopus", 312, 960, 50},
		{"opus_mono", hwmediacodec.Opus, 48000, 1, []string{"-c:a", "libopus", "-b:a", "48k"}, "libopus", 312, 960, 50},
		{"opus_10ms", hwmediacodec.Opus, 48000, 2, []string{"-c:a", "libopus", "-b:a", "96k", "-frame_duration", "10"}, "libopus", 312, 480, 50},
		{"opus_60ms", hwmediacodec.Opus, 48000, 2, []string{"-c:a", "libopus", "-b:a", "96k", "-frame_duration", "60"}, "libopus", 312, 2880, 50},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.decoder == "libopus" && !testutil.HasEncoder(t, "libopus") {
				t.Skip("ffmpeg has no libopus")
			}
			pcm := testutil.AudioSignal(c.sampleRate, c.channels, c.sampleRate*2)
			path := testutil.EncodeAudio(t, pcm, c.sampleRate, c.channels, "in.mp4", c.args...)
			d, err := mp4.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			a := d.Audio()
			if a == nil {
				t.Fatal("no audio track")
			}
			if len(d.Others()) != 1 || d.Others()[0].ID != a.ID {
				t.Errorf("the audio track is not among Others")
			}
			format := a.Format()
			if format.Codec != c.codec || format.SampleRate != c.sampleRate || format.Channels != c.channels || format.Delay != c.delay || format.FrameSamples != c.frame || len(format.Config) == 0 {
				t.Fatalf("format %+v; want %s %d Hz %d channels, delay %d, frames of %d", format, c.codec, c.sampleRate, c.channels, c.delay, c.frame)
			}

			src := a.PacketSource()
			frames := mediatest.DecodeAudio(t, audioDecoderOrSkip(t, format), src)
			if len(frames) != a.SampleCount() {
				t.Fatalf("got %d frames for %d samples of the track", len(frames), a.SampleCount())
			}
			var all []int16
			for i, f := range frames {
				if f.Samples() != c.frame || f.SampleRate != c.sampleRate || f.Channels != c.channels {
					t.Fatalf("frame %d: %d samples at %d Hz with %d channels", i, f.Samples(), f.SampleRate, f.Channels)
				}
				all = append(all, f.Int16s()...)
			}
			if got, want := frames[0].PTS*int64(c.sampleRate)/int64(src.TimeScale()), int64(-c.delay); got != want {
				t.Errorf("the first frame starts at sample %d, want %d", got, want)
			}

			// Everything the stream codes, against ffmpeg's decoder. (The
			// first 2.5 ms of Opus are silence here; they lie in the
			// pre-skip.)
			ref := testutil.DecodeAudio(t, path, true, c.decoder)
			skip := 0
			if c.codec == hwmediacodec.Opus {
				skip = 120 * c.channels
			}
			if len(all) != len(ref) {
				t.Errorf("decoded %d samples, ffmpeg %d", len(all)/c.channels, len(ref)/c.channels)
			}
			shift, snr := testutil.AudioAlignment(ref[skip:], all[skip:], c.channels, 8)
			t.Logf("%d packets; against ffmpeg's decoder %.1f dB at shift %d", len(frames), snr, shift)
			if shift != 0 || snr < c.minSNR {
				t.Errorf("decoded samples differ from ffmpeg's: best SNR %.1f dB at a shift of %d samples", snr, shift)
			}

			// What a player hears, against the signal that was encoded.
			heard := presented(frames, src.TimeScale(), src.Length())
			if len(heard) != len(pcm) {
				t.Errorf("the presentation has %d samples, the input had %d", len(heard)/c.channels, len(pcm)/c.channels)
			}
			shift, snr = testutil.AudioAlignment(pcm, heard, c.channels, 8)
			t.Logf("against the input %.1f dB at shift %d", snr, shift)
			if shift != 0 || snr < 15 {
				t.Errorf("the presented samples are not the input in place: best SNR %.1f dB at a shift of %d samples", snr, shift)
			}
		})
	}
}

// TestAudioMuxRoundTrip encodes a signal, writes it as the audio track of
// an MP4 file and checks the file three ways: ffprobe's view of the
// stream, ffmpeg's decoding against the input, and this package reading
// back the format and the packets that were written.
func TestAudioMuxRoundTrip(t *testing.T) {
	mediatest.RequireFFmpeg(t)
	cases := []struct {
		codec                hwmediacodec.Codec
		sampleRate, channels int
		name                 string
		minSNR               float64
	}{
		{hwmediacodec.AAC, 48000, 2, "aac", 20},
		{hwmediacodec.AAC, 44100, 1, "aac", 20},
		{hwmediacodec.Opus, 48000, 2, "opus", 15},
		{hwmediacodec.Opus, 48000, 1, "opus", 15},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%s_%d_%dch", c.codec, c.sampleRate, c.channels), func(t *testing.T) {
			samples := c.sampleRate*2 + 321
			pcm := testutil.AudioSignal(c.sampleRate, c.channels, samples)
			enc := audioEncoderOrSkip(t, c.codec, c.sampleRate, c.channels, hwmediacodec.WithBitrate(96000*c.channels))
			format := enc.Format()

			path := filepath.Join(t.TempDir(), "out.mp4")
			m, err := mp4.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			aw, err := m.AddAudioTrack(format, uint32(c.sampleRate))
			if err != nil {
				t.Fatal(err)
			}
			var written []hwmediacodec.Packet
			n := mediatest.EncodeAudio(t, enc, pcm, 1470, 0, hwmediacodec.PacketWriterFunc(func(p hwmediacodec.Packet) error {
				written = append(written, p)
				return aw.WritePacket(p)
			}))
			if err := m.Close(); err != nil {
				t.Fatal(err)
			}

			mediatest.CheckDecodes(t, path)
			s := mediatest.AudioStream(t, path)
			want := float64(samples) / float64(c.sampleRate)
			if s.CodecName != c.name || s.Rate() != c.sampleRate || s.Channels != c.channels || s.PacketCount() != n ||
				math.Abs(s.Seconds()-want) > 0.002 || math.Abs(s.Start()) > 0.0005 {
				t.Errorf("ffprobe: %s %d Hz %d channels, %d packets, %.4f s from %.4f; want %s %d Hz %d channels, %d packets, %.4f s from 0",
					s.CodecName, s.Rate(), s.Channels, s.PacketCount(), s.Seconds(), s.Start(), c.name, c.sampleRate, c.channels, n, want)
			}

			// ffmpeg applies the edit list: what comes out is the input.
			// (For Opus it only applies the start and plays the padding of
			// the last packet too, as it does with files of its own.)
			heard := testutil.DecodeAudio(t, path, false, "")
			wantHeard := samples
			if c.codec == hwmediacodec.Opus {
				wantHeard = n*format.FrameSamples - format.Delay
			}
			if len(heard) != wantHeard*c.channels {
				t.Errorf("ffmpeg plays %d samples, want %d", len(heard)/c.channels, wantHeard)
			}
			shift, snr := testutil.AudioAlignment(pcm, heard, c.channels, 8)
			t.Logf("%d packets, delay %d; ffmpeg's decoding against the input: %.1f dB at shift %d", n, format.Delay, snr, shift)
			if shift != 0 || snr < c.minSNR {
				t.Errorf("ffmpeg's decoding is not the input in place: best SNR %.1f dB at a shift of %d samples", snr, shift)
			}

			// Reading the file back gives the same stream.
			d, err := mp4.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			a := d.Audio()
			if a == nil {
				t.Fatal("no audio track in the written file")
			}
			got := a.Format()
			if got.Codec != format.Codec || got.SampleRate != format.SampleRate || got.Channels != format.Channels ||
				got.Delay != format.Delay || got.FrameSamples != format.FrameSamples || !bytes.Equal(got.Config, format.Config) {
				t.Errorf("format read back %+v, written %+v", got, format)
			}
			src := a.PacketSource()
			if l := src.Length(); l != time.Duration(samples)*time.Second/time.Duration(c.sampleRate) {
				t.Errorf("length %v, want %d samples", l, samples)
			}
			for i, w := range written {
				p, err := src.ReadPacket()
				if err != nil {
					t.Fatalf("ReadPacket %d: %v", i, err)
				}
				if !bytes.Equal(p.Data, w.Data) || p.PTS != w.PTS || p.Duration != w.Duration || !p.Keyframe {
					t.Fatalf("packet %d read back with PTS %d duration %d (%d bytes), written with PTS %d duration %d (%d bytes)",
						i, p.PTS, p.Duration, len(p.Data), w.PTS, w.Duration, len(w.Data))
				}
			}

			// And decoding it here gives the input as well.
			src = a.PacketSource()
			frames := mediatest.DecodeAudio(t, audioDecoderOrSkip(t, got), src)
			own := presented(frames, src.TimeScale(), src.Length())
			shift, snr = testutil.AudioAlignment(pcm, own, c.channels, 8)
			if len(own) != len(pcm) || shift != 0 || snr < c.minSNR {
				t.Errorf("own decoding: %d samples for %d, best SNR %.1f dB at a shift of %d samples", len(own)/c.channels, samples, snr, shift)
			}
		})
	}
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
			// The sound itself is untouched by the offset.
			heard := testutil.DecodeAudio(t, path, false, "")
			extra := len(heard) - len(pcm)
			if shift, snr := testutil.AudioAlignment(pcm, heard, channels, 8); extra < 0 || (extra > 0 && c.lead <= 0) || extra >= 1024*channels || shift != 0 || snr < 20 {
				t.Errorf("ffmpeg plays %d samples for %d: best SNR %.1f dB at a shift of %d samples", len(heard)/channels, len(pcm)/channels, snr, shift)
			}
		})
	}
}
