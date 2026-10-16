package hwmediacodec_test

import (
	"bytes"
	"context"
	"io"
	"math"
	"testing"
	"time"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
	"github.com/shibukawa/hwmediacodec/mediacontainer/webm"
)

// These tests take AV1 and Opus through WebM files, with ffmpeg on the
// other side of every comparison. Like the MP4 ones they read and write
// files through testutil, so they also run in a browser.

func openWebM(t *testing.T, data []byte) *webm.Demuxer {
	t.Helper()
	d, err := webm.NewDemuxer(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func readPackets(t *testing.T, src hwmediacodec.PacketReader) []hwmediacodec.Packet {
	t.Helper()
	var out []hwmediacodec.Packet
	for {
		p, err := src.ReadPacket()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
}

// TestDecodeAV1FromWebM writes the temporal units of an SVT-AV1 stream
// into a WebM file, reads them back and decodes them: every picture must
// match ffmpeg's decoding of the original stream bit for bit.
func TestDecodeAV1FromWebM(t *testing.T) {
	requireHardwareDecode(t, hwmediacodec.AV1)
	s := testutil.GenerateStream(t, hwmediacodec.AV1, 320, 240, 60)
	units := splitStream(t, hwmediacodec.AV1, testutil.ReadFile(t, s.Path))

	var file memFile
	m, err := webm.NewMuxer(&file)
	if err != nil {
		t.Fatal(err)
	}
	vw, err := m.AddVideoTrack(hwmediacodec.AV1, 30)
	if err != nil {
		t.Fatal(err)
	}
	for i, u := range units {
		if err := vw.WritePacket(hwmediacodec.Packet{Data: u, PTS: int64(i), DTS: int64(i), Keyframe: i%25 == 0}); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}

	v := openWebM(t, file.data).Video()
	if v == nil || v.Width != 320 || v.Height != 240 {
		t.Fatalf("video track %+v", v)
	}
	packets := readPackets(t, v.PacketSource())
	if len(packets) != len(units) {
		t.Fatalf("%d packets read back, %d written", len(packets), len(units))
	}
	data := make([][]byte, len(packets))
	for i, p := range packets {
		if want := int64(i) * 1000 / 30; math.Abs(float64(p.PTS-want)) > 1 {
			t.Fatalf("packet %d at %d ms, want %d", i, p.PTS, want)
		}
		data[i] = p.Data
	}
	dec, err := hwmediacodec.NewDecoder(context.Background(), hwmediacodec.AV1)
	if err != nil {
		t.Fatalf("NewDecoder: %v", err)
	}
	defer dec.Close()
	got := checksums(decodePackets(t, dec, data, hwmediacodec.NV12))
	want := testutil.ReferenceNV12(t, s.Path, s.Codec, s.Width, s.Height)
	if len(got) != len(want) {
		t.Fatalf("decoded %d frames, ffmpeg %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("frame %d differs from ffmpeg's", i)
		}
	}
}

// TestOpusWebMDecode demuxes and decodes the Opus track of files made by
// ffmpeg: every sample must match ffmpeg's own decoder, and the part
// after the codec delay must be the signal that went in, in place.
func TestOpusWebMDecode(t *testing.T) {
	for _, channels := range []int{1, 2} {
		t.Run(map[int]string{1: "mono", 2: "stereo"}[channels], func(t *testing.T) {
			if !testutil.HasEncoder(t, "libopus") {
				t.Skip("ffmpeg has no libopus")
			}
			pcm := testutil.AudioSignal(48000, channels, 48000*2)
			path := testutil.EncodeAudio(t, pcm, 48000, channels, "in.webm", "-c:a", "libopus", "-b:a", "96k")
			a := openWebM(t, testutil.ReadFile(t, path)).Audio()
			if a == nil {
				t.Fatal("no audio track")
			}
			format := a.Format()
			if format.Codec != hwmediacodec.Opus || format.Channels != channels || format.Delay != 312 || len(format.Config) == 0 {
				t.Fatalf("format %+v", format)
			}
			src := a.PacketSource()
			packets := readPackets(t, src)
			frames := decodeAudio(t, newAudioDecoderOrSkip(t, format), packets)
			if len(frames) != len(packets) || frames[0].PTS != -312 {
				t.Fatalf("%d frames for %d packets, the first at %d", len(frames), len(packets), frames[0].PTS)
			}

			// (AudioToolbox keeps the first 2.5 ms back; they lie in the
			// pre-skip.)
			all := framesPCM(frames)
			ref := testutil.DecodeAudio(t, path, true, "libopus")
			skip := 120 * channels
			if len(all) != len(ref) {
				t.Errorf("decoded %d samples, ffmpeg %d", len(all)/channels, len(ref)/channels)
			}
			shift, snr := testutil.AudioAlignment(ref[skip:], all[skip:], channels, 8)
			t.Logf("%d packets; against ffmpeg's decoder %.1f dB at shift %d", len(frames), snr, shift)
			if shift != 0 || snr < 50 {
				t.Errorf("decoded samples differ from ffmpeg's: best SNR %.1f dB at a shift of %d samples", snr, shift)
			}

			heard := presented(frames, src.TimeScale(), src.Length())
			if len(heard) != len(pcm) {
				t.Errorf("the presentation has %d samples, the input had %d", len(heard)/channels, len(pcm)/channels)
			}
			shift, snr = testutil.AudioAlignment(pcm, heard, channels, 8)
			if shift != 0 || snr < 15 {
				t.Errorf("the presented samples are not the input in place: best SNR %.1f dB at a shift of %d samples", snr, shift)
			}
		})
	}
}

// TestOpusWebMRoundTrip encodes a signal, writes it as the Opus track of
// a WebM file and checks the file three ways: ffprobe's view, ffmpeg's
// decoding against the input, and this package reading back the format
// and the packets that were written.
func TestOpusWebMRoundTrip(t *testing.T) {
	testutil.RequireFFmpeg(t)
	for _, channels := range []int{1, 2} {
		t.Run(map[int]string{1: "mono", 2: "stereo"}[channels], func(t *testing.T) {
			samples := 48000*2 + 321
			pcm := testutil.AudioSignal(48000, channels, samples)
			enc := newAudioEncoderOrSkip(t, hwmediacodec.Opus, 48000, channels, hwmediacodec.WithBitrate(96000*channels))
			format := enc.Format()
			written := encodeAudio(t, enc, pcm, 48000, channels, 1470, 0)

			var file memFile
			m, err := webm.NewMuxer(&file)
			if err != nil {
				t.Fatal(err)
			}
			aw, err := m.AddAudioTrack(format, 48000)
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
			path := testutil.TempDir(t) + "/out.webm"
			testutil.WriteFile(t, path, file.data)

			s := testutil.ProbeAudio(t, path)
			if s.Codec != "opus" || s.SampleRate != 48000 || s.Channels != channels || s.Packets != len(written) {
				t.Errorf("ffprobe: %+v; want opus, %d channels, %d packets", s, channels, len(written))
			}

			// ffmpeg drops the codec delay and the DiscardPadding of the
			// last packet: what comes out is the input.
			heard := testutil.DecodeAudio(t, path, false, "")
			if len(heard) != len(pcm) {
				t.Errorf("ffmpeg plays %d samples, want %d", len(heard)/channels, samples)
			}
			minSNR, lead := 15.0, 0
			if telephonyOpus() {
				minSNR, lead = 12, 8
			}
			shift, snr := testutil.AudioAlignment(pcm, heard, channels, 8)
			t.Logf("%d packets, delay %d; ffmpeg's decoding against the input: %.1f dB at shift %d", len(written), format.Delay, snr, shift)
			if shift > 0 || shift < -lead || snr < minSNR {
				t.Errorf("ffmpeg's decoding is not the input in place: best SNR %.1f dB at a shift of %d samples", snr, shift)
			}

			a := openWebM(t, file.data).Audio()
			if a == nil {
				t.Fatal("no audio track in the written file")
			}
			got := a.Format()
			if got.Codec != format.Codec || got.Channels != format.Channels || got.Delay != format.Delay {
				t.Errorf("format read back %+v, written %+v", got, format)
			}
			src := a.PacketSource()
			packets := readPackets(t, src)
			if want := time.Duration(samples) * time.Second / 48000; src.Length()-want > time.Millisecond || want-src.Length() > time.Millisecond {
				t.Errorf("length %v, want %v", src.Length(), want)
			}
			if len(packets) != len(written) {
				t.Fatalf("%d packets read back, %d written", len(packets), len(written))
			}
			for i, w := range written {
				p := packets[i]
				if !bytes.Equal(p.Data, w.Data) || p.PTS != w.PTS || p.Duration != w.Duration {
					t.Fatalf("packet %d read back with PTS %d duration %d, written with PTS %d duration %d",
						i, p.PTS, p.Duration, w.PTS, w.Duration)
				}
			}
			own := presented(decodeAudio(t, newAudioDecoderOrSkip(t, got), packets), src.TimeScale(), src.Length())
			shift, snr = testutil.AudioAlignment(pcm, own, channels, 8)
			if len(own) != len(pcm) || shift > 0 || shift < -lead || snr < minSNR {
				t.Errorf("own decoding: %d samples for %d, best SNR %.1f dB at a shift of %d samples", len(own)/channels, samples, snr, shift)
			}
		})
	}
}
