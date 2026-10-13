package hwmediacodec_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"testing"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/encoding/adts"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
)

// newAudioDecoderOrSkip opens an audio decoder and skips the test on a
// platform without an audio backend for the codec.
func newAudioDecoderOrSkip(t *testing.T, format hwmediacodec.AudioFormat, opts ...hwmediacodec.AudioDecoderOption) hwmediacodec.AudioDecoder {
	t.Helper()
	dec, err := hwmediacodec.NewAudioDecoder(context.Background(), format, opts...)
	if errors.Is(err, hwmediacodec.ErrUnsupported) {
		t.Skipf("no %s decoder on this platform: %v", format.Codec, err)
	}
	if err != nil {
		t.Fatalf("NewAudioDecoder: %v", err)
	}
	t.Cleanup(func() { dec.Close() })
	return dec
}

// newAudioEncoderOrSkip opens an audio encoder and skips the test on a
// platform without an audio backend for the codec.
func newAudioEncoderOrSkip(t *testing.T, c hwmediacodec.Codec, sampleRate, channels int, opts ...hwmediacodec.AudioEncoderOption) hwmediacodec.AudioEncoder {
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

// decodeAudio runs the Send/Receive loop over every packet and returns the
// frames in order.
func decodeAudio(t *testing.T, dec hwmediacodec.AudioDecoder, packets []hwmediacodec.Packet) []*hwmediacodec.AudioFrame {
	t.Helper()
	frames, err := tryDecodeAudio(dec, packets)
	if err != nil {
		t.Fatal(err)
	}
	return frames
}

// decodeAudioOrSkip is decodeAudio for the streams not every browser
// takes (HE-AAC, more than two channels): a browser's decoder may say yes
// to a configuration and then refuse the packets, some way into the
// stream (Firefox on macOS does), and the test is then skipped.
func decodeAudioOrSkip(t *testing.T, dec hwmediacodec.AudioDecoder, packets []hwmediacodec.Packet) []*hwmediacodec.AudioFrame {
	t.Helper()
	frames, err := tryDecodeAudio(dec, packets)
	if err != nil && runtime.GOOS == "js" && (errors.Is(err, hwmediacodec.ErrInvalidData) || errors.Is(err, hwmediacodec.ErrUnsupported)) {
		t.Skipf("this browser's decoder does not take the stream: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	return frames
}

func tryDecodeAudio(dec hwmediacodec.AudioDecoder, packets []hwmediacodec.Packet) ([]*hwmediacodec.AudioFrame, error) {
	ctx := context.Background()
	var frames []*hwmediacodec.AudioFrame
	drain := func() error {
		for {
			f, err := dec.Receive(ctx)
			if errors.Is(err, hwmediacodec.ErrAgain) || err == io.EOF {
				return nil
			}
			if err != nil {
				return fmt.Errorf("Receive: %w", err)
			}
			frames = append(frames, f)
		}
	}
	for i, p := range packets {
		for {
			err := dec.Send(ctx, p)
			if errors.Is(err, hwmediacodec.ErrAgain) {
				if err := drain(); err != nil {
					return nil, err
				}
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("Send packet %d: %w", i, err)
			}
			break
		}
		if err := drain(); err != nil {
			return nil, err
		}
	}
	if err := dec.Flush(ctx); err != nil {
		return nil, fmt.Errorf("Flush: %w", err)
	}
	if err := drain(); err != nil {
		return nil, err
	}
	if _, err := dec.Receive(ctx); err != io.EOF {
		return nil, fmt.Errorf("Receive after Flush: got %v, want io.EOF", err)
	}
	return frames, nil
}

// encodeAudio feeds pcm to the encoder in pieces of chunk samples and
// returns every packet, the ones Flush brings out included.
func encodeAudio(t *testing.T, enc hwmediacodec.AudioEncoder, pcm []int16, sampleRate, channels, chunk int, pts int64) []hwmediacodec.Packet {
	t.Helper()
	ctx := context.Background()
	var packets []hwmediacodec.Packet
	drain := func() {
		for {
			p, err := enc.Receive(ctx)
			if errors.Is(err, hwmediacodec.ErrAgain) || err == io.EOF {
				return
			}
			if err != nil {
				t.Fatalf("Receive: %v", err)
			}
			packets = append(packets, p)
		}
	}
	for len(pcm) > 0 {
		n := min(chunk*channels, len(pcm))
		f := hwmediacodec.S16Frame(pcm[:n], sampleRate, channels, pts)
		for {
			err := enc.Send(ctx, f)
			if errors.Is(err, hwmediacodec.ErrAgain) {
				drain()
				continue
			}
			if err != nil {
				t.Fatalf("Send: %v", err)
			}
			break
		}
		drain()
		pcm = pcm[n:]
		pts += int64(n / channels)
	}
	if err := enc.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	drain()
	if _, err := enc.Receive(ctx); err != io.EOF {
		t.Fatalf("Receive after Flush: got %v, want io.EOF", err)
	}
	return packets
}

// framesPCM joins the samples of S16 frames.
func framesPCM(frames []*hwmediacodec.AudioFrame) []int16 {
	var out []int16
	for _, f := range frames {
		out = append(out, f.Int16s()...)
	}
	return out
}

// packetBitrate is the average bitrate of packets of the given format.
func packetBitrate(packets []hwmediacodec.Packet, format hwmediacodec.AudioFormat) float64 {
	var size int
	for _, p := range packets {
		size += len(p.Data)
	}
	return float64(size*8) * float64(format.SampleRate) / float64(len(packets)*format.FrameSamples)
}

// readADTS returns the stream format and every packet of an ADTS file.
func readADTS(t *testing.T, path string) (hwmediacodec.AudioFormat, []hwmediacodec.Packet) {
	t.Helper()
	r, err := adts.NewReader(bytes.NewReader(testutil.ReadFile(t, path)))
	if err != nil {
		t.Fatalf("adts.NewReader: %v", err)
	}
	var packets []hwmediacodec.Packet
	for {
		p, err := r.ReadPacket()
		if err == io.EOF {
			return r.Format(), packets
		}
		if err != nil {
			t.Fatalf("ReadPacket: %v", err)
		}
		packets = append(packets, p)
	}
}

// TestAudioDecodeAAC decodes ADTS streams made by ffmpeg and compares the
// samples with ffmpeg's own decoder: the same number of them, in the same
// place, and the same up to the rounding of the two implementations.
func TestAudioDecodeAAC(t *testing.T) {
	cases := []struct {
		sampleRate, channels int
	}{
		{48000, 2},
		{44100, 1},
		{22050, 2},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%dHz_%dch", c.sampleRate, c.channels), func(t *testing.T) {
			pcm := testutil.AudioSignal(c.sampleRate, c.channels, c.sampleRate)
			path := testutil.EncodeAudio(t, pcm, c.sampleRate, c.channels, "in.aac", "-c:a", "aac", "-b:a", "128k")
			format, packets := readADTS(t, path)
			if format.SampleRate != c.sampleRate || format.Channels != c.channels {
				t.Fatalf("format: got %d Hz with %d channels, want %d with %d", format.SampleRate, format.Channels, c.sampleRate, c.channels)
			}
			want := testutil.DecodeAudio(t, path, true, "aac")

			frames := decodeAudio(t, newAudioDecoderOrSkip(t, format), packets)
			if len(frames) != len(packets) {
				t.Fatalf("got %d frames for %d packets", len(frames), len(packets))
			}
			for i, f := range frames {
				if f.PTS != packets[i].PTS || f.SampleRate != c.sampleRate || f.Channels != c.channels || f.Format != hwmediacodec.S16 || f.Samples() != format.FrameSamples {
					t.Fatalf("frame %d: PTS %d, %d Hz, %d channels, %s, %d samples; want PTS %d and %d samples", i, f.PTS, f.SampleRate, f.Channels, f.Format, f.Samples(), packets[i].PTS, format.FrameSamples)
				}
			}
			got := framesPCM(frames)
			if len(got) != len(want) {
				t.Errorf("got %d samples, ffmpeg decodes %d", len(got)/c.channels, len(want)/c.channels)
			}
			shift, snr := testutil.AudioAlignment(want, got, c.channels, 8)
			t.Logf("%d packets, %d samples, SNR against ffmpeg %.1f dB at shift %d", len(packets), len(got)/c.channels, snr, shift)
			if shift != 0 || snr < 50 {
				t.Errorf("decoded samples differ from ffmpeg's: best SNR %.1f dB at a shift of %d samples", snr, shift)
			}

			// The floating point output is the same signal.
			f32 := decodeAudio(t, newAudioDecoderOrSkip(t, format, hwmediacodec.WithSampleFormat(hwmediacodec.F32)), packets)
			var conv []int16
			for _, f := range f32 {
				if f.Format != hwmediacodec.F32 || f.Int16s() != nil {
					t.Fatalf("F32 frame reports format %s", f.Format)
				}
				for _, v := range f.Float32s() {
					conv = append(conv, int16(max(-32768, min(32767, v*32768))))
				}
			}
			if snr := testutil.AudioSNR(got, conv); len(conv) != len(got) || snr < 60 {
				t.Errorf("F32 output: %d samples at %.1f dB against the S16 output of %d", len(conv), snr, len(got))
			}
		})
	}
}

// TestAudioEncodeAAC encodes a signal, lets ffmpeg decode the result and
// compares it with the input, which also checks the delay the encoder
// reports: a wrong one shifts the signal.
func TestAudioEncodeAAC(t *testing.T) {
	cases := []struct {
		sampleRate, channels, bitrate int
	}{
		{48000, 2, 128000},
		{44100, 1, 64000},
		{48000, 2, 0},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%dHz_%dch_%d", c.sampleRate, c.channels, c.bitrate), func(t *testing.T) {
			testutil.RequireFFmpeg(t)
			const seconds = 2
			pcm := testutil.AudioSignal(c.sampleRate, c.channels, c.sampleRate*seconds+123)
			var opts []hwmediacodec.AudioEncoderOption
			if c.bitrate > 0 {
				opts = append(opts, hwmediacodec.WithBitrate(c.bitrate))
			}
			enc := newAudioEncoderOrSkip(t, hwmediacodec.AAC, c.sampleRate, c.channels, opts...)
			format := enc.Format()
			if format.Codec != hwmediacodec.AAC || format.SampleRate != c.sampleRate || format.Channels != c.channels || format.FrameSamples != 1024 || len(format.Config) < 2 || format.Delay <= 0 {
				t.Fatalf("unexpected format %+v", format)
			}
			// Frames of an odd size: the encoder cuts the packets itself.
			packets := encodeAudio(t, enc, pcm, c.sampleRate, c.channels, 1000, 0)

			samples := int64(len(pcm) / c.channels)
			for i, p := range packets {
				wantPTS := int64(i*format.FrameSamples - format.Delay)
				wantDur := min(int64(format.FrameSamples), samples-wantPTS)
				if p.PTS != wantPTS || p.DTS != wantPTS || !p.Keyframe || p.Duration != wantDur {
					t.Fatalf("packet %d: PTS %d DTS %d duration %d keyframe %v; want PTS %d duration %d", i, p.PTS, p.DTS, p.Duration, p.Keyframe, wantPTS, wantDur)
				}
			}
			if end := packets[len(packets)-1].PTS + packets[len(packets)-1].Duration; end != samples {
				t.Errorf("the packets end at sample %d, the input at %d", end, samples)
			}
			t.Logf("%d packets, delay %d, %.0f bit/s", len(packets), format.Delay, packetBitrate(packets, format))

			var file bytes.Buffer
			w, err := adts.NewWriter(&file, format)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range packets {
				if err := w.WritePacket(p); err != nil {
					t.Fatal(err)
				}
			}
			path := testutil.TempDir(t) + "/out.aac"
			testutil.WriteFile(t, path, file.Bytes())

			decoded := testutil.DecodeAudio(t, path, true, "aac")
			if len(decoded) != len(packets)*format.FrameSamples*c.channels {
				t.Fatalf("ffmpeg decodes %d samples from %d packets", len(decoded)/c.channels, len(packets))
			}
			decoded = decoded[format.Delay*c.channels:]
			shift, snr := testutil.AudioAlignment(pcm, decoded, c.channels, 8)
			t.Logf("SNR against the input %.1f dB at shift %d", snr, shift)
			if shift != 0 || snr < 20 {
				t.Errorf("the decoded signal differs from the input: best SNR %.1f dB at a shift of %d samples", snr, shift)
			}
		})
	}
}

// TestAudioEncodeBitrate checks that the bitrate option steers the encoder,
// with noise as input: a signal that takes every bit it is given.
func TestAudioEncodeBitrate(t *testing.T) {
	const sampleRate, channels = 48000, 2
	pcm := testutil.AudioNoise(sampleRate * channels * 3)
	for _, c := range []hwmediacodec.Codec{hwmediacodec.AAC, hwmediacodec.Opus} {
		for _, rc := range []hwmediacodec.RateControl{hwmediacodec.VBR, hwmediacodec.CBR} {
			for _, bitrate := range []int{64000, 160000} {
				t.Run(fmt.Sprintf("%s_%s_%d", c, rc, bitrate), func(t *testing.T) {
					enc := newAudioEncoderOrSkip(t, c, sampleRate, channels, hwmediacodec.WithBitrate(bitrate), hwmediacodec.WithRateControl(rc))
					packets := encodeAudio(t, enc, pcm, sampleRate, channels, 4800, 0)
					rate := packetBitrate(packets, enc.Format())
					t.Logf("%d packets, %.0f bit/s", len(packets), rate)
					// CBR holds the rate; VBR treats it as a target for
					// ordinary material and spends less on this one.
					lo, hi := 0.6, 1.3
					if rc == hwmediacodec.CBR && !(runtime.GOOS == "js" && c == hwmediacodec.AAC) {
						// (A browser takes the mode as a wish: Chrome's AAC
						// encoder gives the same packets in both.)
						lo, hi = 0.95, 1.05
					}
					if rate < lo*float64(bitrate) || rate > hi*float64(bitrate) {
						t.Errorf("bitrate %.0f is far from the requested %d", rate, bitrate)
					}
					if rc == hwmediacodec.CBR && c == hwmediacodec.Opus {
						// Opus CBR makes every packet the same size (AAC
						// keeps a bit reservoir and only holds the average).
						for i, p := range packets {
							if len(p.Data) != len(packets[0].Data) {
								t.Fatalf("CBR packet %d has %d bytes, packet 0 has %d", i, len(p.Data), len(packets[0].Data))
							}
						}
					}
				})
			}
		}
	}
}

// TestAudioDecodeAACSurround decodes streams with more than two channels:
// they come out in the order of a WAV file (and of ffmpeg), not in the
// order the AAC bitstream stores them. Every channel carries a tone of its
// own, so that swapped channels cannot pass.
func TestAudioDecodeAACSurround(t *testing.T) {
	const sampleRate = 48000
	for _, c := range []struct {
		layout   string
		channels int
	}{
		{"3.0", 3},
		{"4.0", 4},
		{"5.0", 5},
		{"5.1", 6},
	} {
		t.Run(c.layout, func(t *testing.T) {
			channels := c.channels
			pcm := testutil.AudioSignal(sampleRate, channels, sampleRate)
			path := testutil.EncodeAudio(t, pcm, sampleRate, channels, "in.aac", "-af", "aformat=channel_layouts="+c.layout, "-c:a", "aac", "-b:a", fmt.Sprint(64000*channels))
			format, packets := readADTS(t, path)
			if format.Channels != channels {
				t.Fatalf("format has %d channels", format.Channels)
			}
			frames := decodeAudioOrSkip(t, newAudioDecoderOrSkip(t, format), packets)
			got, want := framesPCM(frames), testutil.DecodeAudio(t, path, true, "aac")
			if len(got) != len(want) {
				t.Fatalf("got %d samples, ffmpeg decodes %d", len(got)/channels, len(want)/channels)
			}
			// Channel by channel: a swap of two quiet channels would hide
			// in an overall figure. (The LFE channel keeps little of its
			// tone and compares worst.)
			column := func(pcm []int16, ch int) []int16 {
				out := make([]int16, len(pcm)/channels)
				for i := range out {
					out[i] = pcm[i*channels+ch]
				}
				return out
			}
			for ch := range channels {
				ref := column(want, ch)
				if snr := testutil.AudioSNR(ref, column(got, ch)); snr < 30 {
					// Say where the channel went, if anywhere.
					at := -1
					for other := range channels {
						if testutil.AudioSNR(ref, column(got, other)) >= 30 {
							at = other
						}
					}
					t.Errorf("channel %d differs from ffmpeg's (%.1f dB); it is channel %d of the output", ch, snr, at)
				}
			}
		})
	}
}

// TestAudioRoundTrip sends a signal through an encoder and a decoder of
// this library, for both codecs and both sample formats, with timestamps
// in a unit and from a start of the caller's choosing.
func TestAudioRoundTrip(t *testing.T) {
	const sampleRate, channels, timeScale, startPTS = 48000, 2, 90000, 180000
	pcm := testutil.AudioSignal(sampleRate, channels, sampleRate+500)
	ctx := context.Background()
	for _, c := range []hwmediacodec.Codec{hwmediacodec.AAC, hwmediacodec.Opus} {
		for _, sf := range []hwmediacodec.SampleFormat{hwmediacodec.S16, hwmediacodec.F32} {
			t.Run(fmt.Sprintf("%s_%s", c, sf), func(t *testing.T) {
				enc := newAudioEncoderOrSkip(t, c, sampleRate, channels,
					hwmediacodec.WithSampleFormat(sf), hwmediacodec.WithTimeScale(timeScale), hwmediacodec.WithBitrate(160000))
				format := enc.Format()
				frame := hwmediacodec.S16Frame(pcm, sampleRate, channels, startPTS)
				if sf == hwmediacodec.F32 {
					f32 := make([]float32, len(pcm))
					for i, v := range pcm {
						f32[i] = float32(v) / 32768
					}
					frame = hwmediacodec.F32Frame(f32, sampleRate, channels, startPTS)
				}
				if got, want := frame.Samples(), len(pcm)/channels; got != want {
					t.Fatalf("frame reports %d samples, want %d", got, want)
				}
				// One Send with the whole signal: more packets than the
				// encoder holds, so it has to be drained in between.
				var packets []hwmediacodec.Packet
				drain := func() {
					for {
						p, err := enc.Receive(ctx)
						if errors.Is(err, hwmediacodec.ErrAgain) || err == io.EOF {
							return
						}
						if err != nil {
							t.Fatal(err)
						}
						packets = append(packets, p)
					}
				}
				if err := enc.Send(ctx, frame); err != nil {
					t.Fatalf("Send: %v", err)
				}
				drain()
				if err := enc.Flush(ctx); err != nil {
					t.Fatal(err)
				}
				drain()

				// 90 kHz timestamps: 15 units for every 8 samples.
				unit := int64(timeScale) / 6000
				for i, p := range packets {
					want := startPTS + int64(i*format.FrameSamples-format.Delay)*unit/8
					if d := p.PTS - want; d < -1 || d > 1 {
						t.Fatalf("packet %d has PTS %d, want %d", i, p.PTS, want)
					}
				}
				last := packets[len(packets)-1]
				if end, want := last.PTS+last.Duration, startPTS+int64(len(pcm)/channels)*unit/8; end < want-1 || end > want+1 {
					t.Errorf("the packets end at %d, the signal at %d", end, want)
				}

				frames := decodeAudio(t, newAudioDecoderOrSkip(t, format, hwmediacodec.WithSampleFormat(sf)), packets)
				var out []int16
				for i, f := range frames {
					if f.PTS != packets[i].PTS || f.Format != sf {
						t.Fatalf("frame %d: PTS %d format %s, want PTS %d format %s", i, f.PTS, f.Format, packets[i].PTS, sf)
					}
					if sf == hwmediacodec.S16 {
						out = append(out, f.Int16s()...)
						continue
					}
					for _, v := range f.Float32s() {
						out = append(out, int16(max(-32768, min(32767, v*32768))))
					}
				}
				out = out[format.Delay*channels:]
				shift, snr := testutil.AudioAlignment(pcm, out, channels, 8)
				t.Logf("%d packets, delay %d, %.1f dB at shift %d", len(packets), format.Delay, snr, shift)
				if c == hwmediacodec.Opus && telephonyOpus() {
					// The filter shifts the phase of the low tones, which
					// reads as a few samples of lead and a lower figure.
					if shift < -8 || shift > 0 || snr < 12 {
						t.Errorf("round trip in the telephony mode: best SNR %.1f dB at a shift of %d samples", snr, shift)
					}
				} else if shift != 0 || snr < 25 {
					t.Errorf("round trip: best SNR %.1f dB at a shift of %d samples", snr, shift)
				}
			})
		}
	}
}

// TestAudioFlushAndReuse checks what Flush promises: the decoder starts
// afresh (a seek), the encoder starts a new signal, and both keep working.
func TestAudioFlushAndReuse(t *testing.T) {
	const sampleRate, channels = 48000, 2
	pcm := testutil.AudioSignal(sampleRate, channels, sampleRate)
	for _, c := range []hwmediacodec.Codec{hwmediacodec.AAC, hwmediacodec.Opus} {
		t.Run(c.String(), func(t *testing.T) {
			enc := newAudioEncoderOrSkip(t, c, sampleRate, channels)
			format := enc.Format()
			first := encodeAudio(t, enc, pcm, sampleRate, channels, 4096, 0)
			// The same signal again from another start: the same packets
			// count, timestamps moved.
			second := encodeAudio(t, enc, pcm, sampleRate, channels, 4096, 1_000_000)
			if len(second) != len(first) {
				t.Fatalf("the second signal gave %d packets, the first %d", len(second), len(first))
			}
			for i := range first {
				if second[i].PTS-first[i].PTS != 1_000_000 || second[i].Duration != first[i].Duration {
					t.Fatalf("packet %d: PTS %d after %d, duration %d after %d", i, second[i].PTS, first[i].PTS, second[i].Duration, first[i].Duration)
				}
			}

			dec := newAudioDecoderOrSkip(t, format)
			whole := framesPCM(decodeAudio(t, dec, first))
			// Decode again from the middle, as after a seek: once the
			// decoder has run in, the samples are the same as before
			// (all but the same for Opus, whose state converges without
			// ever being reset by the stream).
			const from, settle = 20, 6
			part := framesPCM(decodeAudio(t, dec, first[from:]))
			a := whole[(from+settle)*format.FrameSamples*channels:]
			b := part[settle*format.FrameSamples*channels:]
			if snr := testutil.AudioSNR(a, b); len(a) != len(b) || snr < 35 {
				t.Errorf("decoding from packet %d: %d samples for %d, %.1f dB against the first pass", from, len(b), len(a), snr)
			}
		})
	}
}

// TestAudioErrors covers the requests that must fail and how.
func TestAudioErrors(t *testing.T) {
	ctx := context.Background()
	if _, err := hwmediacodec.NewDecoder(ctx, hwmediacodec.AAC); !errors.Is(err, hwmediacodec.ErrUnsupported) {
		t.Errorf("NewDecoder(AAC): got %v, want ErrUnsupported", err)
	}
	if _, err := hwmediacodec.NewEncoder(ctx, hwmediacodec.Opus, 640, 480); !errors.Is(err, hwmediacodec.ErrUnsupported) {
		t.Errorf("NewEncoder(Opus): got %v, want ErrUnsupported", err)
	}
	if _, err := hwmediacodec.NewAudioDecoder(ctx, hwmediacodec.AudioFormat{Codec: hwmediacodec.H264}); !errors.Is(err, hwmediacodec.ErrUnsupported) {
		t.Errorf("NewAudioDecoder(H264): got %v, want ErrUnsupported", err)
	}
	if _, err := hwmediacodec.NewAudioEncoder(ctx, hwmediacodec.HEVC, 48000, 2); !errors.Is(err, hwmediacodec.ErrUnsupported) {
		t.Errorf("NewAudioEncoder(HEVC): got %v, want ErrUnsupported", err)
	}
	if _, err := hwmediacodec.NewAudioEncoder(ctx, hwmediacodec.AAC, 0, 2); err == nil || errors.Is(err, hwmediacodec.ErrUnsupported) {
		t.Errorf("NewAudioEncoder at 0 Hz: got %v, want an argument error", err)
	}
	if _, err := hwmediacodec.NewAudioEncoder(ctx, hwmediacodec.AAC, 48000, 2, hwmediacodec.WithBitrate(-1)); err == nil {
		t.Error("NewAudioEncoder with a negative bitrate succeeded")
	}

	enc := newAudioEncoderOrSkip(t, hwmediacodec.AAC, 48000, 2)
	// Requests the codecs do not offer.
	for name, open := range map[string]func() (hwmediacodec.AudioEncoder, error){
		"opus at 44100 Hz": func() (hwmediacodec.AudioEncoder, error) {
			return hwmediacodec.NewAudioEncoder(ctx, hwmediacodec.Opus, 44100, 2)
		},
		"aac at 2 Mbit/s": func() (hwmediacodec.AudioEncoder, error) {
			return hwmediacodec.NewAudioEncoder(ctx, hwmediacodec.AAC, 48000, 2, hwmediacodec.WithBitrate(2_000_000))
		},
		"aac with 6 channels": func() (hwmediacodec.AudioEncoder, error) {
			return hwmediacodec.NewAudioEncoder(ctx, hwmediacodec.AAC, 48000, 6)
		},
		"aac at 12345 Hz": func() (hwmediacodec.AudioEncoder, error) {
			return hwmediacodec.NewAudioEncoder(ctx, hwmediacodec.AAC, 12345, 2)
		},
		"unknown sample format": func() (hwmediacodec.AudioEncoder, error) {
			return hwmediacodec.NewAudioEncoder(ctx, hwmediacodec.AAC, 48000, 2, hwmediacodec.WithSampleFormat(99))
		},
	} {
		if runtime.GOOS == "js" && (name == "aac at 2 Mbit/s" || name == "aac at 12345 Hz") {
			// A browser may take a bitrate or a rate its encoder does not
			// offer and encode at another; nothing in the API tells.
			continue
		}
		if runtime.GOOS != "darwin" && name == "aac at 2 Mbit/s" {
			// AudioToolbox refuses a bitrate it does not offer; the other
			// encoders code at the nearest one they have.
			continue
		}
		e, err := open()
		if !errors.Is(err, hwmediacodec.ErrUnsupported) {
			t.Errorf("%s: got %v, want ErrUnsupported", name, err)
		} else {
			t.Logf("%s: %v", name, err)
		}
		if e != nil {
			e.Close()
		}
	}

	// Frames that do not match the encoder.
	for name, f := range map[string]*hwmediacodec.AudioFrame{
		"nil":            nil,
		"other rate":     hwmediacodec.S16Frame(make([]int16, 200), 44100, 2, 0),
		"other channels": hwmediacodec.S16Frame(make([]int16, 200), 48000, 1, 0),
		"other format":   hwmediacodec.F32Frame(make([]float32, 200), 48000, 2, 0),
		"half a sample":  {SampleRate: 48000, Channels: 2, Format: hwmediacodec.S16, Data: make([]byte, 7)},
	} {
		if err := enc.Send(ctx, f); !errors.Is(err, hwmediacodec.ErrInvalidData) {
			t.Errorf("Send(%s): got %v, want ErrInvalidData", name, err)
		}
	}
	if _, err := enc.Receive(ctx); !errors.Is(err, hwmediacodec.ErrAgain) {
		t.Errorf("Receive on an idle encoder: got %v, want ErrAgain", err)
	}

	dec := newAudioDecoderOrSkip(t, enc.Format())
	if err := dec.Send(ctx, hwmediacodec.Packet{}); !errors.Is(err, hwmediacodec.ErrInvalidData) {
		t.Errorf("Send of an empty packet: got %v, want ErrInvalidData", err)
	}
	if _, err := dec.Receive(ctx); !errors.Is(err, hwmediacodec.ErrAgain) {
		t.Errorf("Receive on an idle decoder: got %v, want ErrAgain", err)
	}
	// A decoder without a configuration and without the numbers to assume
	// one from.
	if _, err := hwmediacodec.NewAudioDecoder(ctx, hwmediacodec.AudioFormat{Codec: hwmediacodec.AAC}); err == nil {
		t.Error("NewAudioDecoder(AAC) without config, rate and channels succeeded")
	}
	if _, err := hwmediacodec.NewAudioDecoder(ctx, hwmediacodec.AudioFormat{Codec: hwmediacodec.AAC, Config: []byte{0xFF}}); !errors.Is(err, hwmediacodec.ErrInvalidData) {
		t.Errorf("NewAudioDecoder with a broken config: got %v, want ErrInvalidData", err)
	}

	enc.Close()
	dec.Close()
	if err := enc.Send(ctx, hwmediacodec.S16Frame(make([]int16, 200), 48000, 2, 0)); !errors.Is(err, hwmediacodec.ErrClosed) {
		t.Errorf("Send after Close: got %v, want ErrClosed", err)
	}
	if _, err := dec.Receive(ctx); !errors.Is(err, hwmediacodec.ErrClosed) {
		t.Errorf("Receive after Close: got %v, want ErrClosed", err)
	}
}

// TestAudioDecodeGarbage feeds bytes that are no AAC to a decoder: an
// error, and the decoder goes on with the packets that follow.
func TestAudioDecodeGarbage(t *testing.T) {
	const sampleRate, channels = 48000, 2
	ctx := context.Background()
	enc := newAudioEncoderOrSkip(t, hwmediacodec.AAC, sampleRate, channels)
	packets := encodeAudio(t, enc, testutil.AudioSignal(sampleRate, channels, sampleRate/2), sampleRate, channels, 4096, 0)
	dec := newAudioDecoderOrSkip(t, enc.Format())
	want := framesPCM(decodeAudio(t, dec, packets))

	// The error may come from Send or, where the codec works on its own
	// time (a browser), from a later call; Flush is the latest.
	garbage := bytes.Repeat([]byte{0xFF, 0x00, 0xA5, 0x5A}, 60)
	err := dec.Send(ctx, hwmediacodec.Packet{Data: garbage})
	if err == nil {
		err = dec.Flush(ctx)
	}
	for err == nil {
		_, err = dec.Receive(ctx)
	}
	t.Logf("garbage: %v", err)
	if err != io.EOF && !errors.Is(err, hwmediacodec.ErrAgain) && !errors.Is(err, hwmediacodec.ErrInvalidData) {
		t.Errorf("garbage: got %v, want ErrInvalidData or frames", err)
	}
	got := framesPCM(decodeAudio(t, dec, packets))
	const settle = 4 * 1024 * channels
	if snr := testutil.AudioSNR(want[settle:], got[settle:]); len(got) != len(want) || snr < 50 {
		t.Errorf("after garbage: %d samples for %d, %.1f dB against a clean pass", len(got), len(want), snr)
	}
}

// TestProbeAudio checks that Probe lists the audio codecs next to the video
// engines where a backend offers them, as software.
func TestProbeAudio(t *testing.T) {
	caps, err := hwmediacodec.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, c := range caps {
		if !c.Codec.IsAudio() {
			continue
		}
		if c.Hardware {
			t.Errorf("%s %s %s is listed as hardware", c.Backend, c.Codec, c.Direction)
		}
		found[fmt.Sprintf("%s %s %s", c.Backend, c.Codec, c.Direction)] = true
	}
	if runtime.GOOS == "darwin" {
		for _, want := range []string{"audiotoolbox aac decode", "audiotoolbox aac encode", "audiotoolbox opus decode", "audiotoolbox opus encode"} {
			if !found[want] {
				t.Errorf("Probe does not list %q", want)
			}
		}
	}
	for _, c := range []hwmediacodec.Codec{hwmediacodec.AAC, hwmediacodec.Opus} {
		if hwmediacodec.HasHardware(context.Background(), c, hwmediacodec.Decode) {
			t.Errorf("HasHardware reports a hardware %s decoder", c)
		}
	}
}
