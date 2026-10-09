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
	"strconv"
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
	case codec.AV1:
		// SVT-AV1's random-access structure always has hidden frames (shown
		// through show_existing_frame), whatever bframes says; the stream is
		// an IVF file of temporal units.
		return Stream{Path: GenerateAV1(t, width, height, frames), Codec: c, Width: width, Height: height, Frames: frames}
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
	return ReferenceFrames(t, path, c, codec.NV12, width, height)
}

// ReferenceFrames decodes the stream with ffmpeg's software decoder and
// returns every frame tightly packed in format f, in display order.
//
// For the RGB formats the conversion uses the colour matrix the stream
// declares (BT.601 when it declares none, as VideoToolbox assumes for
// untagged standard-definition streams) with bilinear chroma interpolation
// and accurate rounding, the closest match to the hardware conversion;
// compare with BlockPSNR, since the chroma siting still differs around
// sharp edges.
func ReferenceFrames(t testing.TB, path string, c codec.Codec, f codec.PixelFormat, width, height int) [][]byte {
	t.Helper()
	ffmpeg := RequireFFmpeg(t)
	args := []string{"-hide_banner", "-loglevel", "error", "-f", containerFormat(c)}
	args = append(args, decoderArgs(t, c)...)
	args = append(args, "-i", path, "-fps_mode", "passthrough")
	if f != codec.NV12 {
		args = append(args, "-vf", "scale=flags=bilinear+full_chroma_int+accurate_rnd")
	}
	args = append(args, "-f", "rawvideo", "-pix_fmt", PixFmt(f), "-")
	cmd := exec.Command(ffmpeg, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ffmpeg reference decode failed: %v\n%s", err, stderr.String())
	}
	return splitFrames(t, out, f.FrameSize(width, height))
}

// PixFmt returns the ffmpeg pixel format name of f.
func PixFmt(f codec.PixelFormat) string {
	switch f {
	case codec.NV12:
		return "nv12"
	case codec.RGBA:
		return "rgba"
	case codec.BGRA:
		return "bgra"
	}
	panic("no ffmpeg pixel format for " + f.String())
}

// NV12FrameSize returns the number of bytes of one NV12 frame.
func NV12FrameSize(width, height int) int {
	return codec.NV12.FrameSize(width, height)
}

func splitFrames(t testing.TB, data []byte, frameSize int) [][]byte {
	t.Helper()
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
	return GenerateRawFrames(t, codec.NV12, width, height, frames)
}

// GenerateRawFrames renders the synthetic test pattern with ffmpeg and
// returns the raw frames tightly packed in format f.
func GenerateRawFrames(t testing.TB, f codec.PixelFormat, width, height, frames int) [][]byte {
	t.Helper()
	ffmpeg := RequireFFmpeg(t)
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=%dx%d:rate=30", width, height),
		"-frames:v", fmt.Sprint(frames), "-f", "rawvideo", "-pix_fmt", PixFmt(f), "-")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ffmpeg generate raw frames failed: %v\n%s", err, stderr.String())
	}
	got := splitFrames(t, out, f.FrameSize(width, height))
	if len(got) != frames {
		t.Fatalf("ffmpeg produced %d raw frames, want %d", len(got), frames)
	}
	return got
}

// NV12Frame wraps one raw NV12 frame as a codec.Frame that aliases data.
func NV12Frame(data []byte, width, height int, pts int64) *codec.Frame {
	return RawFrame(data, codec.NV12, width, height, pts)
}

// RawFrame wraps one tightly packed raw frame as a codec.Frame that aliases
// data.
func RawFrame(data []byte, f codec.PixelFormat, width, height int, pts int64) *codec.Frame {
	fr := &codec.Frame{Width: width, Height: height, Format: f, PTS: pts}
	off := 0
	for i := 0; i < f.PlaneCount(); i++ {
		rows, rowBytes := f.PlaneLayout(i, width, height)
		fr.Planes = append(fr.Planes, data[off:off+rows*rowBytes])
		fr.Strides = append(fr.Strides, rowBytes)
		off += rows * rowBytes
	}
	return fr
}

// FrameBytes returns the pixels of f tightly packed in the layout
// ReferenceFrames uses, honouring the frame strides.
func FrameBytes(f *codec.Frame) []byte {
	out := make([]byte, 0, f.Format.FrameSize(f.Width, f.Height))
	for i, p := range f.Planes {
		rows, rowBytes := f.Format.PlaneLayout(i, f.Width, f.Height)
		stride := f.Strides[i]
		for r := 0; r < rows; r++ {
			out = append(out, p[r*stride:r*stride+rowBytes]...)
		}
	}
	return out
}

// GenerateStreamWithTimestamps is GenerateStreamBFrames through an MP4
// container, so that every access unit has a real presentation timestamp.
// It returns the Annex-B stream and the PTS of every access unit in decode
// order; sorting the indexes by PTS gives the display order.
func GenerateStreamWithTimestamps(t testing.TB, c codec.Codec, width, height, frames, bframes int) (Stream, []int64) {
	t.Helper()
	ffmpeg := RequireFFmpeg(t)
	dir := t.TempDir()
	mp4 := filepath.Join(dir, "stream.mp4")
	args := []string{"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=%dx%d:rate=30", width, height),
		"-frames:v", fmt.Sprint(frames), "-pix_fmt", "yuv420p"}
	var path, bsf string
	switch c {
	case codec.H264:
		if !HasEncoder(t, "libx264") {
			t.Skip("ffmpeg has no libx264 encoder")
		}
		path = filepath.Join(dir, "stream.h264")
		bsf = "h264_mp4toannexb"
		args = append(args, "-c:v", "libx264", "-preset", "veryfast", "-bf", fmt.Sprint(bframes), "-g", "25", "-f", "mp4", mp4)
	case codec.HEVC:
		if !HasEncoder(t, "libx265") {
			t.Skip("ffmpeg has no libx265 encoder")
		}
		path = filepath.Join(dir, "stream.hevc")
		bsf = "hevc_mp4toannexb"
		args = append(args, "-c:v", "libx265", "-preset", "veryfast", "-tag:v", "hvc1",
			"-x265-params", fmt.Sprintf("bframes=%d:open-gop=0:keyint=25:log-level=error", bframes), "-f", "mp4", mp4)
	default:
		t.Fatalf("no generator for %s", c)
	}
	run := func(name string, args ...string) []byte {
		cmd := exec.Command(name, args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s failed: %v\n%s", name, err, stderr.String())
		}
		return out
	}
	run(ffmpeg, args...)
	run(ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-i", mp4, "-c", "copy", "-bsf:v", bsf, "-f", map[codec.Codec]string{codec.H264: "h264", codec.HEVC: "hevc"}[c], path)
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not found in PATH")
	}
	out := run(ffprobe, "-hide_banner", "-loglevel", "error", "-select_streams", "v:0",
		"-show_entries", "packet=pts", "-of", "csv=p=0", mp4)
	var pts []int64
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSuffix(strings.TrimSpace(line), ",")
		if line == "" {
			continue
		}
		v, err := strconv.ParseInt(line, 10, 64)
		if err != nil {
			t.Fatalf("unexpected ffprobe pts %q", line)
		}
		pts = append(pts, v)
	}
	if len(pts) != frames {
		t.Fatalf("ffprobe listed %d packets, want %d", len(pts), frames)
	}
	return Stream{Path: path, Codec: c, Width: width, Height: height, Frames: frames}, pts
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
	cmd := exec.Command(ffprobe, "-hide_banner", "-loglevel", "error",
		"-f", containerFormat(c), "-i", path, "-select_streams", "v:0",
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
	return ProbeStreamField(t, path, c, "profile")
}

// ProbeStreamField returns one stream field ffprobe reports (for example
// "profile" or "color_space").
func ProbeStreamField(t testing.TB, path string, c codec.Codec, field string) string {
	t.Helper()
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe not found in PATH")
	}
	cmd := exec.Command(ffprobe, "-hide_banner", "-loglevel", "error",
		"-f", containerFormat(c), "-i", path, "-select_streams", "v:0",
		"-show_entries", "stream="+field, "-of", "csv=p=0")
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

// FrameChecksum hashes a frame in the same layout ReferenceFrames uses,
// honouring the frame strides.
func FrameChecksum(f *codec.Frame) [32]byte {
	return sha256.Sum256(FrameBytes(f))
}

// BlockPSNR compares two tightly packed RGBA/BGRA pictures after averaging
// every block x block pixel block per channel (alpha excluded). The
// low-pass removes the differences between chroma upsampling methods while
// a wrong colour matrix, range or channel order still shows.
func BlockPSNR(a, b []byte, width, height, block int) float64 {
	avg := func(pix []byte) []float64 {
		var out []float64
		for y := 0; y+block <= height; y += block {
			for x := 0; x+block <= width; x += block {
				for k := 0; k < 3; k++ {
					sum := 0
					for yy := 0; yy < block; yy++ {
						for xx := 0; xx < block; xx++ {
							sum += int(pix[((y+yy)*width+x+xx)*4+k])
						}
					}
					out = append(out, float64(sum)/float64(block*block))
				}
			}
		}
		return out
	}
	fa, fb := avg(a), avg(b)
	var sum float64
	for i := range fa {
		d := fa[i] - fb[i]
		sum += d * d
	}
	if sum == 0 {
		return math.Inf(1)
	}
	return 10 * math.Log10(255*255/(sum/float64(len(fa))))
}

// PSNR returns the peak signal-to-noise ratio between two byte slices of
// equal length, in dB (+Inf when identical).
func PSNR(a, b []byte) float64 {
	var sum float64
	for i := range a {
		d := float64(a[i]) - float64(b[i])
		sum += d * d
	}
	if sum == 0 {
		return math.Inf(1)
	}
	mse := sum / float64(len(a))
	return 10 * math.Log10(255*255/mse)
}
