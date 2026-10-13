package testutil

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// AudioSignal returns samples of a deterministic test signal as interleaved
// 16-bit PCM: per channel a tone of its own and a slow sweep, at a quarter
// of full scale. The sweep never repeats, so a comparison with a shifted
// copy fails clearly, which is what makes alignment errors visible.
func AudioSignal(sampleRate, channels, samples int) []int16 {
	out := make([]int16, samples*channels)
	for c := range channels {
		tone := 330.0 * float64(c+1)
		phase := 0.0
		for i := range samples {
			t := float64(i) / float64(sampleRate)
			// A sweep from 200 Hz upwards, one octave per second.
			phase += 2 * math.Pi * 200 * math.Pow(2, t+0.37*float64(c)) / float64(sampleRate)
			v := 0.15*math.Sin(2*math.Pi*tone*t) + 0.1*math.Sin(phase)
			out[i*channels+c] = int16(math.Round(v * 32767))
		}
	}
	return out
}

// AudioNoise returns n samples of deterministic white noise at a quarter
// of full scale: a signal that no codec can compress, for bitrate checks.
func AudioNoise(n int) []int16 {
	out := make([]int16, n)
	state := uint32(0x2545F491)
	for i := range out {
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		out[i] = int16(state>>16) / 4
	}
	return out
}

// PCMBytes returns the little-endian bytes of 16-bit samples.
func PCMBytes(samples []int16) []byte {
	b := make([]byte, len(samples)*2)
	for i, s := range samples {
		binary.LittleEndian.PutUint16(b[i*2:], uint16(s))
	}
	return b
}

// PCMSamples is the opposite of PCMBytes.
func PCMSamples(b []byte) []int16 {
	s := make([]int16, len(b)/2)
	for i := range s {
		s[i] = int16(binary.LittleEndian.Uint16(b[i*2:]))
	}
	return s
}

// EncodeAudio encodes 16-bit PCM with ffmpeg into a file called name in a
// temporary directory and returns its path. args go between the input and
// the output (the encoder and its settings); the container follows from
// the extension of name.
func EncodeAudio(t testing.TB, pcm []int16, sampleRate, channels int, name string, args ...string) string {
	t.Helper()
	ffmpeg := RequireFFmpeg(t)
	dir := t.TempDir()
	in := filepath.Join(dir, "in.pcm")
	if err := os.WriteFile(in, PCMBytes(pcm), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, name)
	all := []string{"-hide_banner", "-loglevel", "error", "-y",
		"-f", "s16le", "-ar", fmt.Sprint(sampleRate), "-ac", fmt.Sprint(channels), "-i", in}
	all = append(append(all, args...), out)
	cmd := exec.Command(ffmpeg, all...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ffmpeg encode audio failed: %v\n%s", err, stderr.String())
	}
	return out
}

// DecodeAudio decodes the audio of path with ffmpeg into interleaved 16-bit
// PCM at the stream's own rate and channel count. With all set, ffmpeg
// keeps the samples a player drops (the encoder delay a container
// records), which is what a hwmediacodec audio decoder returns too.
// decoder names the ffmpeg decoder to use, or is empty for the default.
func DecodeAudio(t testing.TB, path string, all bool, decoder string) []int16 {
	t.Helper()
	ffmpeg := RequireFFmpeg(t)
	args := []string{"-hide_banner", "-loglevel", "error"}
	if all {
		args = append(args, "-flags2", "+skip_manual")
	}
	if decoder != "" {
		args = append(args, "-c:a", decoder)
	}
	args = append(args, "-i", path, "-vn", "-f", "s16le", "-c:a", "pcm_s16le", "-")
	cmd := exec.Command(ffmpeg, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ffmpeg decode audio failed: %v\n%s", err, stderr.String())
	}
	return PCMSamples(stdout.Bytes())
}

// AudioSNR returns the signal-to-noise ratio in dB of got against ref over
// the samples both have (positive infinity when they are identical).
func AudioSNR(ref, got []int16) float64 {
	n := min(len(ref), len(got))
	var signal, noise float64
	for i := range n {
		r, d := float64(ref[i]), float64(ref[i])-float64(got[i])
		signal += r * r
		noise += d * d
	}
	if noise == 0 {
		return math.Inf(1)
	}
	if signal == 0 {
		return math.Inf(-1)
	}
	return 10 * math.Log10(signal/noise)
}

// AudioAlignment slides got against ref by up to maxShift samples per
// channel in both directions and returns the shift with the best
// signal-to-noise ratio and that ratio: 0 when got lines up with ref,
// positive when got is late.
func AudioAlignment(ref, got []int16, channels, maxShift int) (shift int, snr float64) {
	snr = math.Inf(-1)
	for s := -maxShift; s <= maxShift; s++ {
		r, g := ref, got
		if s > 0 {
			g = g[min(s*channels, len(g)):]
		} else {
			r = r[min(-s*channels, len(r)):]
		}
		if v := AudioSNR(r, g); v > snr {
			shift, snr = s, v
		}
	}
	return shift, snr
}
