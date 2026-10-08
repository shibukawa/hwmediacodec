// Package testutil generates test bitstreams and reference output with the
// ffmpeg command-line tool. ffmpeg is a test oracle only; the library never
// depends on it at run time.
package testutil

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shibukawa/hwmediacodec/internal/codec"
)

// RequireFFmpeg skips the test when ffmpeg is not installed.
func RequireFFmpeg(t testing.TB) string {
	t.Helper()
	p, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not found in PATH; skipping reference comparison")
	}
	return p
}

// HasEncoder reports whether the installed ffmpeg offers the named encoder.
func HasEncoder(t testing.TB, name string) bool {
	t.Helper()
	out, err := exec.Command(RequireFFmpeg(t), "-hide_banner", "-encoders").Output()
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

// Stream describes a generated elementary stream.
type Stream struct {
	Path   string
	Codec  codec.Codec
	Width  int
	Height int
	Frames int
}

// GenerateStream encodes a synthetic test pattern into an Annex-B elementary
// stream without B-frames, so that decode order equals display order.
func GenerateStream(t testing.TB, c codec.Codec, width, height, frames int) Stream {
	t.Helper()
	return GenerateStreamBFrames(t, c, width, height, frames, 0)
}

// GenerateStreamBFrames is GenerateStream with the given number of
// consecutive B-frames, so that decode order differs from display order.
func GenerateStreamBFrames(t testing.TB, c codec.Codec, width, height, frames, bframes int) Stream {
	t.Helper()
	ffmpeg := RequireFFmpeg(t)
	dir := t.TempDir()
	var path string
	args := []string{"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=%dx%d:rate=30", width, height),
		"-frames:v", fmt.Sprint(frames), "-pix_fmt", "yuv420p"}
	switch c {
	case codec.H264:
		if !HasEncoder(t, "libx264") {
			t.Skip("ffmpeg has no libx264 encoder")
		}
		path = filepath.Join(dir, fmt.Sprintf("test_%dx%d_%d_b%d.h264", width, height, frames, bframes))
		args = append(args, "-c:v", "libx264", "-preset", "veryfast", "-bf", fmt.Sprint(bframes), "-g", "25", "-f", "h264", path)
	case codec.HEVC:
		if !HasEncoder(t, "libx265") {
			t.Skip("ffmpeg has no libx265 encoder")
		}
		path = filepath.Join(dir, fmt.Sprintf("test_%dx%d_%d_b%d.hevc", width, height, frames, bframes))
		args = append(args, "-c:v", "libx265", "-preset", "veryfast",
			"-x265-params", fmt.Sprintf("bframes=%d:open-gop=0:keyint=25:log-level=error", bframes), "-f", "hevc", path)
	default:
		t.Fatalf("no generator for %s", c)
	}
	cmd := exec.Command(ffmpeg, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ffmpeg generate failed: %v\n%s", err, stderr.String())
	}
	return Stream{Path: path, Codec: c, Width: width, Height: height, Frames: frames}
}

// ReferenceNV12 decodes the stream with ffmpeg's software decoder and returns
// the SHA-256 of every frame in NV12 layout (Y plane then interleaved CbCr).
func ReferenceNV12(t testing.TB, path string, c codec.Codec, width, height int) [][32]byte {
	t.Helper()
	ffmpeg := RequireFFmpeg(t)
	format := map[codec.Codec]string{codec.H264: "h264", codec.HEVC: "hevc"}[c]
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error",
		"-f", format, "-i", path, "-fps_mode", "passthrough",
		"-f", "rawvideo", "-pix_fmt", "nv12", "-")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ffmpeg reference decode failed: %v\n%s", err, stderr.String())
	}
	frameSize := width*height + ((width+1)/2*2)*((height+1)/2)
	if len(out)%frameSize != 0 {
		t.Fatalf("reference output %d bytes is not a multiple of frame size %d", len(out), frameSize)
	}
	sums := make([][32]byte, 0, len(out)/frameSize)
	for i := 0; i+frameSize <= len(out); i += frameSize {
		sums = append(sums, sha256.Sum256(out[i:i+frameSize]))
	}
	return sums
}

// ReadFile reads a whole file or fails the test.
func ReadFile(t testing.TB, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// FrameChecksum hashes an NV12 frame in the same layout ReferenceNV12 uses,
// honouring the frame strides.
func FrameChecksum(f *codec.Frame) [32]byte {
	h := sha256.New()
	rows := []int{f.Height, (f.Height + 1) / 2}
	rowBytes := []int{f.Width, (f.Width + 1) / 2 * 2}
	for i, p := range f.Planes {
		stride := f.Strides[i]
		for r := 0; r < rows[i]; r++ {
			h.Write(p[r*stride : r*stride+rowBytes[i]])
		}
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum
}
