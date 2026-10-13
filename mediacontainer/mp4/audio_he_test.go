package mp4_test

import (
	"io"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/internal/mediatest"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
	"github.com/shibukawa/hwmediacodec/mediacontainer/mp4"
)

// TestAudioTrackDecodeHEAAC decodes HE-AAC, whose spectral band replication
// doubles the sample rate of the AAC-LC core: the track reports the output
// rate and every packet decodes to 2048 samples. ffmpeg cannot encode
// HE-AAC without a non-free library, so the file comes from afconvert,
// which only macOS has.
func TestAudioTrackDecodeHEAAC(t *testing.T) {
	afconvert, err := exec.LookPath("afconvert")
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
			path := filepath.Join(t.TempDir(), "he.m4a")
			if out, err := exec.Command(afconvert, "-f", "m4af", "-d", c.format, "-b", c.bitrate, wav, path).CombinedOutput(); err != nil {
				t.Skipf("afconvert cannot make %s: %v\n%s", c.format, err, out)
			}
			d, err := mp4.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			a := d.Audio()
			if a == nil {
				t.Fatal("no audio track")
			}
			format := a.Format()
			t.Logf("format %+v, time scale %d, %d samples", format, a.TimeScale, a.SampleCount())
			if format.Codec != hwmediacodec.AAC || format.SampleRate != sampleRate || format.Channels != channels || format.FrameSamples != 2048 {
				t.Errorf("format %+v; want aac at %d Hz with %d channels in frames of 2048", format, sampleRate, channels)
			}
			var packets []hwmediacodec.Packet
			for i := range a.SampleCount() {
				p, err := a.Packet(i)
				if err != nil {
					t.Fatal(err)
				}
				packets = append(packets, p)
			}
			dec := audioDecoderOrSkip(t, format)
			decode := func(packets []hwmediacodec.Packet) []int16 {
				frames := mediatest.DecodeAudio(t, dec, &packetList{packets: packets})
				if len(frames) != len(packets) {
					t.Fatalf("got %d frames for %d packets", len(frames), len(packets))
				}
				var out []int16
				for i, f := range frames {
					if f.Samples() != 2048 || f.SampleRate != sampleRate || f.Channels != channels || f.PTS != packets[i].PTS {
						t.Fatalf("frame %d: %d samples at %d Hz with %d channels, PTS %d; want 2048 samples and PTS %d", i, f.Samples(), f.SampleRate, f.Channels, f.PTS, packets[i].PTS)
					}
					out = append(out, f.Int16s()...)
				}
				return out
			}
			all := decode(packets)

			// afconvert records its delay (2112 samples of the core, twice
			// that at the output rate) in iTunes metadata, not in an edit
			// list, so it is taken as known here. The upper half of the
			// spectrum is synthesised (and with parametric stereo so is
			// the stereo image), so the signal is compared for its place
			// more than for its shape.
			const delay = 2 * 2112
			shift, snr := testutil.AudioAlignment(pcm, all[delay*channels:], channels, 8)
			t.Logf("%d frames; against the input %.1f dB at shift %d", len(packets), snr, shift)
			if shift != 0 || snr < c.minSNR {
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

// packetList serves packets as a hwmediacodec.PacketReader.
type packetList struct {
	packets []hwmediacodec.Packet
	next    int
}

func (l *packetList) Codec() hwmediacodec.Codec { return hwmediacodec.AAC }
func (l *packetList) TimeScale() int32          { return 48000 }
func (l *packetList) ReadPacket() (hwmediacodec.Packet, error) {
	if l.next >= len(l.packets) {
		return hwmediacodec.Packet{}, io.EOF
	}
	l.next++
	return l.packets[l.next-1], nil
}
