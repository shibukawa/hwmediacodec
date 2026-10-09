// Package testutil generates test bitstreams and reference output with the
// ffmpeg command-line tool. ffmpeg is a test oracle only; the library never
// depends on it at run time.
package testutil

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"math"
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
	frames := ReferenceNV12Frames(t, path, c, width, height)
	sums := make([][32]byte, len(frames))
	for i, f := range frames {
		sums[i] = sha256.Sum256(f)
	}
	return sums
}

// ReferenceNV12Frames decodes the stream with ffmpeg's software decoder and
// returns every frame in NV12 layout, in display order.
func ReferenceNV12Frames(t testing.TB, path string, c codec.Codec, width, height int) [][]byte {
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
	return splitNV12(t, out, width, height)
}

// NV12FrameSize returns the number of bytes of one NV12 frame.
func NV12FrameSize(width, height int) int {
	return width*height + ((width+1)/2*2)*((height+1)/2)
}

func splitNV12(t testing.TB, data []byte, width, height int) [][]byte {
	t.Helper()
	frameSize := NV12FrameSize(width, height)
	if len(data)%frameSize != 0 {
		t.Fatalf("raw output %d bytes is not a multiple of frame size %d", len(data), frameSize)
	}
	frames := make([][]byte, 0, len(data)/frameSize)
	for i := 0; i+frameSize <= len(data); i += frameSize {
		frames = append(frames, data[i:i+frameSize])
	}
	return frames
}

// GenerateNV12Frames renders the synthetic test pattern with ffmpeg and
// returns the raw NV12 frames.
func GenerateNV12Frames(t testing.TB, width, height, frames int) [][]byte {
	t.Helper()
	ffmpeg := RequireFFmpeg(t)
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=%dx%d:rate=30", width, height),
		"-frames:v", fmt.Sprint(frames), "-f", "rawvideo", "-pix_fmt", "nv12", "-")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ffmpeg generate raw frames failed: %v\n%s", err, stderr.String())
	}
	got := splitNV12(t, out, width, height)
	if len(got) != frames {
		t.Fatalf("ffmpeg produced %d raw frames, want %d", len(got), frames)
	}
	return got
}

// NV12Frame wraps one raw NV12 frame as a codec.Frame that aliases data.
func NV12Frame(data []byte, width, height int, pts int64) *codec.Frame {
	ySize := width * height
	return &codec.Frame{
		Width:   width,
		Height:  height,
		Format:  codec.NV12,
		Planes:  [][]byte{data[:ySize], data[ySize:]},
		Strides: []int{width, (width + 1) / 2 * 2},
		PTS:     pts,
	}
}

// PSNRY returns the peak signal-to-noise ratio of the luma planes of two
// NV12 frames of the given size, in dB (+Inf for identical planes).
func PSNRY(a, b []byte, width, height int) float64 {
	n := width * height
	var sum float64
	for i := 0; i < n; i++ {
		d := float64(a[i]) - float64(b[i])
		sum += d * d
	}
	if sum == 0 {
		return math.Inf(1)
	}
	mse := sum / float64(n)
	return 10 * math.Log10(255*255/mse)
}

// FrameInfo is what ffprobe reports about one coded frame.
type FrameInfo struct {
	Keyframe bool
	PictType string // "I", "P" or "B"
}

// ProbeFrames runs ffprobe over an elementary stream and returns per-frame
// information in decode order. It skips the test when ffprobe is missing.
func ProbeFrames(t testing.TB, path string, c codec.Codec) []FrameInfo {
	t.Helper()
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not found in PATH")
	}
	format := map[codec.Codec]string{codec.H264: "h264", codec.HEVC: "hevc"}[c]
	cmd := exec.Command(ffprobe, "-hide_banner", "-loglevel", "error",
		"-f", format, "-i", path, "-select_streams", "v:0",
		"-show_entries", "frame=key_frame,pict_type", "-of", "csv=p=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ffprobe failed: %v\n%s", err, stderr.String())
	}
	var infos []FrameInfo
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, ",")
		if len(fields) < 2 {
			t.Fatalf("unexpected ffprobe line %q", line)
		}
		infos = append(infos, FrameInfo{Keyframe: fields[0] == "1", PictType: fields[1]})
	}
	return infos
}

// ProbeProfile returns the profile string ffprobe reports for the stream
// (for example "High" or "Main").
func ProbeProfile(t testing.TB, path string, c codec.Codec) string {
	t.Helper()
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not found in PATH")
	}
	format := map[codec.Codec]string{codec.H264: "h264", codec.HEVC: "hevc"}[c]
	cmd := exec.Command(ffprobe, "-hide_banner", "-loglevel", "error",
		"-f", format, "-i", path, "-select_streams", "v:0",
		"-show_entries", "stream=profile", "-of", "csv=p=0")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ffprobe failed: %v", err)
	}
	return strings.TrimSpace(string(out))
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
