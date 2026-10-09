package testutil

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shibukawa/hwmediacodec/internal/codec"
)

// HasDecoder reports whether the installed ffmpeg offers the named decoder.
func HasDecoder(t testing.TB, name string) bool {
	t.Helper()
	out, err := exec.Command(RequireFFmpeg(t), "-hide_banner", "-decoders").Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == name {
			return true
		}
	}
	return false
}

// GenerateAV1 encodes a synthetic test pattern with SVT-AV1 into an IVF
// file: one temporal unit per IVF frame, a keyframe every 25 frames, film
// grain off (so that conformant decoders are bit-exact) and random-access
// GOPs whose hidden alternate reference frames are shown later through
// show_existing_frame. extra holds further ffmpeg output options such as
// "-pix_fmt", "yuv420p10le". It skips the test when ffmpeg lacks libsvtav1.
func GenerateAV1(t testing.TB, width, height, frames int, extra ...string) string {
	t.Helper()
	ffmpeg := RequireFFmpeg(t)
	if !HasEncoder(t, "libsvtav1") {
		t.Skip("ffmpeg has no libsvtav1 encoder")
	}
	path := filepath.Join(t.TempDir(), fmt.Sprintf("test_%dx%d_%d.ivf", width, height, frames))
	args := []string{"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=%dx%d:rate=30", width, height),
		"-frames:v", fmt.Sprint(frames), "-pix_fmt", "yuv420p",
		"-c:v", "libsvtav1", "-preset", "10", "-g", "25", "-svtav1-params", "film-grain=0"}
	args = append(args, extra...)
	args = append(args, "-f", "ivf", path)
	cmd := exec.Command(ffmpeg, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ffmpeg generate failed: %v\n%s", err, stderr.String())
	}
	return path
}

// GenerateAV1With encodes a synthetic test pattern into an IVF file with the
// named ffmpeg AV1 encoder (libaom-av1, libsvtav1, librav1e) and the given
// output options, for streams that exercise particular coding tools. It
// skips the test when ffmpeg lacks the encoder.
func GenerateAV1With(t testing.TB, encoder string, width, height, frames int, args ...string) string {
	t.Helper()
	ffmpeg := RequireFFmpeg(t)
	if !HasEncoder(t, encoder) {
		t.Skipf("ffmpeg has no %s encoder", encoder)
	}
	path := filepath.Join(t.TempDir(), fmt.Sprintf("test_%dx%d_%d.ivf", width, height, frames))
	cmdArgs := []string{"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=%dx%d:rate=30", width, height),
		"-frames:v", fmt.Sprint(frames), "-pix_fmt", "yuv420p", "-c:v", encoder}
	cmdArgs = append(cmdArgs, args...)
	cmdArgs = append(cmdArgs, "-f", "ivf", path)
	cmd := exec.Command(ffmpeg, cmdArgs...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ffmpeg generate failed: %v\n%s", err, stderr.String())
	}
	return path
}

// GenerateAV1Aomenc encodes a synthetic test pattern into an IVF file with
// the aomenc command line encoder, which exposes libaom controls that
// ffmpeg's wrapper does not (super-resolution, reference scaling, S-frames,
// tile groups, forward key frames). args are aomenc options. It skips the
// test when aomenc is not installed.
func GenerateAV1Aomenc(t testing.TB, width, height, frames int, args ...string) string {
	t.Helper()
	ffmpeg := RequireFFmpeg(t)
	aomenc, err := exec.LookPath("aomenc")
	if err != nil {
		t.Skip("aomenc not found in PATH")
	}
	dir := t.TempDir()
	y4m := filepath.Join(dir, "source.y4m")
	if out, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=%dx%d:rate=30", width, height),
		"-frames:v", fmt.Sprint(frames), "-pix_fmt", "yuv420p", y4m).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg generate failed: %v\n%s", err, out)
	}
	path := filepath.Join(dir, fmt.Sprintf("test_%dx%d_%d.ivf", width, height, frames))
	cmdArgs := append([]string{"--ivf", "--passes=1", "--quiet"}, args...)
	cmdArgs = append(cmdArgs, "-o", path, y4m)
	if out, err := exec.Command(aomenc, cmdArgs...).CombinedOutput(); err != nil {
		t.Fatalf("aomenc failed: %v\n%s", err, out)
	}
	return path
}

// containerFormat returns the ffmpeg demuxer name for the elementary stream
// files the generators write: raw Annex-B for H.264 and HEVC, IVF for AV1.
func containerFormat(c codec.Codec) string {
	switch c {
	case codec.H264:
		return "h264"
	case codec.HEVC:
		return "hevc"
	case codec.AV1:
		return "ivf"
	}
	panic("no container format for " + c.String())
}

// decoderArgs returns the ffmpeg input options that select the reference
// decoder: libdav1d for AV1 (the test is skipped when ffmpeg lacks it), the
// default software decoder otherwise.
func decoderArgs(t testing.TB, c codec.Codec) []string {
	t.Helper()
	if c != codec.AV1 {
		return nil
	}
	if !HasDecoder(t, "libdav1d") {
		t.Skip("ffmpeg has no libdav1d decoder")
	}
	return []string{"-c:v", "libdav1d"}
}
