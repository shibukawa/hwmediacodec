package hwmediacodec_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"runtime"
	"testing"
	"time"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
	"github.com/shibukawa/hwmediacodec/mediacontainer/mp4"
)

// These tests take the audio codecs through MP4 files, with ffmpeg on the
// other side of every comparison. Files are read and written through
// testutil, so the tests also run in a browser, where the files are on
// the host.

// openMP4 parses the file at a host path.
func openMP4(t *testing.T, path string) *mp4.Demuxer {
	t.Helper()
	d, err := mp4.NewDemuxer(bytes.NewReader(testutil.ReadFile(t, path)))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// memFile is a file in memory: the io.WriteSeeker an mp4.Muxer writes to.
type memFile struct {
	data []byte
	pos  int
}

func (f *memFile) Write(p []byte) (int, error) {
	if end := f.pos + len(p); end > len(f.data) {
		f.data = append(f.data, make([]byte, end-len(f.data))...)
	}
	copy(f.data[f.pos:], p)
	f.pos += len(p)
	return len(p), nil
}

func (f *memFile) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekCurrent:
		offset += int64(f.pos)
	case io.SeekEnd:
		offset += int64(len(f.data))
	}
	if offset < 0 {
		return 0, errors.New("seek before the start")
	}
	f.pos = int(offset)
	return offset, nil
}

// trackPackets reads every packet of an audio track with the edit list
// applied, and returns the track's time scale and length with them.
func trackPackets(t *testing.T, a *mp4.AudioTrack) (packets []hwmediacodec.Packet, timeScale int32, length time.Duration) {
	t.Helper()
	src := a.PacketSource()
	for {
		p, err := src.ReadPacket()
		if err == io.EOF {
			return packets, src.TimeScale(), src.Length()
		}
		if err != nil {
			t.Fatal(err)
		}
		packets = append(packets, p)
	}
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
	}{
		{"aac", hwmediacodec.AAC, 48000, 2, []string{"-c:a", "aac", "-b:a", "128k"}, "aac", 1024, 1024},
		{"aac_44100_mono", hwmediacodec.AAC, 44100, 1, []string{"-c:a", "aac", "-b:a", "96k"}, "aac", 1024, 1024},
		{"opus", hwmediacodec.Opus, 48000, 2, []string{"-c:a", "libopus", "-b:a", "96k"}, "libopus", 312, 960},
		{"opus_mono", hwmediacodec.Opus, 48000, 1, []string{"-c:a", "libopus", "-b:a", "48k"}, "libopus", 312, 960},
		{"opus_10ms", hwmediacodec.Opus, 48000, 2, []string{"-c:a", "libopus", "-b:a", "96k", "-frame_duration", "10"}, "libopus", 312, 480},
		{"opus_60ms", hwmediacodec.Opus, 48000, 2, []string{"-c:a", "libopus", "-b:a", "96k", "-frame_duration", "60"}, "libopus", 312, 2880},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.decoder == "libopus" && !testutil.HasEncoder(t, "libopus") {
				t.Skip("ffmpeg has no libopus")
			}
			pcm := testutil.AudioSignal(c.sampleRate, c.channels, c.sampleRate*2)
			path := testutil.EncodeAudio(t, pcm, c.sampleRate, c.channels, "in.mp4", c.args...)
			d := openMP4(t, path)
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

			packets, timeScale, length := trackPackets(t, a)
			frames := decodeAudio(t, newAudioDecoderOrSkip(t, format), packets)
			if len(frames) != len(packets) {
				t.Fatalf("got %d frames for %d packets", len(frames), len(packets))
			}
			for i, f := range frames {
				if f.Samples() != c.frame || f.SampleRate != c.sampleRate || f.Channels != c.channels || f.PTS != packets[i].PTS {
					t.Fatalf("frame %d: %d samples at %d Hz with %d channels, PTS %d (packet %d)", i, f.Samples(), f.SampleRate, f.Channels, f.PTS, packets[i].PTS)
				}
			}
			all := framesPCM(frames)
			if got, want := frames[0].PTS*int64(c.sampleRate)/int64(timeScale), int64(-c.delay); got != want {
				t.Errorf("the first frame starts at sample %d, want %d", got, want)
			}

			// Everything the stream codes, against ffmpeg's decoder. (The
			// first 2.5 ms of Opus are left out: AudioToolbox's decoder
			// keeps them back and they come out as silence. They lie in
			// the pre-skip.)
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
			if shift != 0 || snr < 50 {
				t.Errorf("decoded samples differ from ffmpeg's: best SNR %.1f dB at a shift of %d samples", snr, shift)
			}

			// What a player hears, against the signal that was encoded.
			heard := presented(frames, timeScale, length)
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
// stream, ffmpeg's decoding against the input, and the demuxer reading
// back the format and the packets that were written.
func TestAudioMuxRoundTrip(t *testing.T) {
	testutil.RequireFFmpeg(t)
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
			enc := newAudioEncoderOrSkip(t, c.codec, c.sampleRate, c.channels, hwmediacodec.WithBitrate(96000*c.channels))
			format := enc.Format()
			written := encodeAudio(t, enc, pcm, c.sampleRate, c.channels, 1470, 0)
			n := len(written)

			var file memFile
			m, err := mp4.NewMuxer(&file)
			if err != nil {
				t.Fatal(err)
			}
			aw, err := m.AddAudioTrack(format, uint32(c.sampleRate))
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range written {
				if err := aw.WritePacket(p); err != nil {
					t.Fatal(err)
				}
			}
			if err := m.Close(); err != nil {
				t.Fatal(err)
			}
			path := testutil.TempDir(t) + "/out.mp4"
			testutil.WriteFile(t, path, file.data)

			s := testutil.ProbeAudio(t, path)
			want := float64(samples) / float64(c.sampleRate)
			if s.Codec != c.name || s.SampleRate != c.sampleRate || s.Channels != c.channels || s.Packets != n ||
				math.Abs(s.Duration-want) > 0.002 || math.Abs(s.Start) > 0.0005 {
				t.Errorf("ffprobe: %+v; want %s %d Hz %d channels, %d packets, %.4f s from 0", s, c.name, c.sampleRate, c.channels, n, want)
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
			// (An Opus encoder in the telephony mode filters the low end:
			// the tones lead by a few samples and compare worse.)
			minSNR, lead := c.minSNR, 0
			if c.codec == hwmediacodec.Opus && telephonyOpus() {
				minSNR, lead = 12, 8
			}
			shift, snr := testutil.AudioAlignment(pcm, heard, c.channels, 8)
			t.Logf("%d packets, delay %d; ffmpeg's decoding against the input: %.1f dB at shift %d", n, format.Delay, snr, shift)
			if shift > 0 || shift < -lead || snr < minSNR {
				t.Errorf("ffmpeg's decoding is not the input in place: best SNR %.1f dB at a shift of %d samples", snr, shift)
			}

			// Reading the file back gives the same stream.
			a := openMP4(t, path).Audio()
			if a == nil {
				t.Fatal("no audio track in the written file")
			}
			got := a.Format()
			if got.Codec != format.Codec || got.SampleRate != format.SampleRate || got.Channels != format.Channels ||
				got.Delay != format.Delay || got.FrameSamples != format.FrameSamples || !bytes.Equal(got.Config, format.Config) {
				t.Errorf("format read back %+v, written %+v", got, format)
			}
			packets, timeScale, length := trackPackets(t, a)
			if length != time.Duration(samples)*time.Second/time.Duration(c.sampleRate) {
				t.Errorf("length %v, want %d samples", length, samples)
			}
			if len(packets) != len(written) {
				t.Fatalf("%d packets read back, %d written", len(packets), len(written))
			}
			for i, w := range written {
				p := packets[i]
				if !bytes.Equal(p.Data, w.Data) || p.PTS != w.PTS || p.Duration != w.Duration || !p.Keyframe {
					t.Fatalf("packet %d read back with PTS %d duration %d (%d bytes), written with PTS %d duration %d (%d bytes)",
						i, p.PTS, p.Duration, len(p.Data), w.PTS, w.Duration, len(w.Data))
				}
			}

			// And decoding it here gives the input as well.
			own := presented(decodeAudio(t, newAudioDecoderOrSkip(t, got), packets), timeScale, length)
			shift, snr = testutil.AudioAlignment(pcm, own, c.channels, 8)
			if len(own) != len(pcm) || shift > 0 || shift < -lead || snr < minSNR {
				t.Errorf("own decoding: %d samples for %d, best SNR %.1f dB at a shift of %d samples", len(own)/c.channels, samples, snr, shift)
			}
		})
	}
}

// TestAudioTrackDecodeHEAAC decodes HE-AAC, whose spectral band replication
// doubles the sample rate of the AAC-LC core: the track reports the output
// rate and every packet decodes to 2048 samples. ffmpeg cannot encode
// HE-AAC without a non-free library, so the file comes from afconvert,
// which only macOS has.
func TestAudioTrackDecodeHEAAC(t *testing.T) {
	afconvert, err := testutil.LookPath("afconvert")
	if err != nil {
		t.Skip("afconvert not found (it is part of macOS)")
	}
	const sampleRate, channels = 48000, 2
	pcm := testutil.AudioSignal(sampleRate, channels, sampleRate*2)
	wav := testutil.EncodeAudio(t, pcm, sampleRate, channels, "in.wav", "-c:a", "pcm_s16le")
	for _, c := range []struct {
		name, format string
		bitrate      string
		minSNR       float64
	}{
		{"he", "aach", "64000", 25},
		{"he_v2", "aacp", "32000", 5},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := testutil.TempDir(t) + "/he.m4a"
			if out, err := testutil.Command(afconvert, "-f", "m4af", "-d", c.format, "-b", c.bitrate, wav, path).CombinedOutput(); err != nil {
				t.Skipf("afconvert cannot make %s: %v\n%s", c.format, err, out)
			}
			a := openMP4(t, path).Audio()
			if a == nil {
				t.Fatal("no audio track")
			}
			format := a.Format()
			t.Logf("format %+v, time scale %d, %d samples", format, a.TimeScale, a.SampleCount())
			if format.Codec != hwmediacodec.AAC || format.SampleRate != sampleRate || format.Channels != channels || format.FrameSamples != 2048 {
				t.Errorf("format %+v; want aac at %d Hz with %d channels in frames of 2048", format, sampleRate, channels)
			}
			packets, _, _ := trackPackets(t, a)
			dec := newAudioDecoderOrSkip(t, format)
			decode := func(packets []hwmediacodec.Packet) []int16 {
				frames := decodeAudioOrSkip(t, dec, packets)
				if len(frames) != len(packets) {
					t.Fatalf("got %d frames for %d packets", len(frames), len(packets))
				}
				for i, f := range frames {
					if f.Samples() != 2048 || f.SampleRate != sampleRate || f.Channels != channels || f.PTS != packets[i].PTS {
						t.Fatalf("frame %d: %d samples at %d Hz with %d channels, PTS %d; want 2048 samples and PTS %d", i, f.Samples(), f.SampleRate, f.Channels, f.PTS, packets[i].PTS)
					}
				}
				return framesPCM(frames)
			}
			all := decode(packets)

			// afconvert records its delay (2112 samples of the core, twice
			// that at the output rate) in iTunes metadata, not in an edit
			// list, so it is taken as known here. The upper half of the
			// spectrum is synthesised (and with parametric stereo so is
			// the stereo image), so the signal is compared for its place
			// more than for its shape.
			//
			// AudioToolbox takes the delay of the band replication (962
			// samples) out of its output; ffmpeg's decoder, which Chrome
			// uses, leaves it in, so in a browser the signal may lie that
			// much later.
			const delay = 2 * 2112
			shift, snr := testutil.AudioAlignment(pcm, all[delay*channels:], channels, 1000)
			t.Logf("%d frames; against the input %.1f dB at shift %d", len(packets), snr, shift)
			if (shift != 0 && !(runtime.GOOS == "js" && shift == 962)) || snr < c.minSNR {
				t.Errorf("the decoded samples are not the input in place: best SNR %.1f dB at a shift of %d samples", snr, shift)
			}

			// After a Flush the decoder starts over: the same stream gives
			// the same samples, and one entered in the middle lands in the
			// same place once the decoder has run in. (Not so with
			// parametric stereo: its parameters travel in some packets
			// only, and until one comes by the decoder plays mono.)
			if snr := testutil.AudioSNR(all, decode(packets)); snr < 80 {
				t.Errorf("decoding the stream a second time: %.1f dB against the first pass", snr)
			}
			if c.format == "aach" {
				const from, settle = 20, 4
				part := decode(packets[from:])
				x, y := all[(from+settle)*2048*channels:], part[settle*2048*channels:]
				if snr := testutil.AudioSNR(x, y); len(x) != len(y) || snr < 40 {
					t.Errorf("decoding from packet %d: %d samples for %d, %.1f dB against the first pass", from, len(y), len(x), snr)
				}
			}
		})
	}
}
