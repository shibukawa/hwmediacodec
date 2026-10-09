// Package mediatest drives ffmpeg, ffprobe and sips as test oracles for
// the container and image packages: it generates MP4 inputs, inspects
// outputs and converts pictures. Nothing here is used at run time. The
// examples module keeps its own copy of the ffmpeg helpers
// (examples/internal/testutil), since it cannot import this one.
package mediatest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/shibukawa/hwmediacodec"
)

// RequireFFmpeg skips the test when ffmpeg or ffprobe is missing.
func RequireFFmpeg(t testing.TB) {
	t.Helper()
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found in PATH", tool)
		}
	}
}

// HasEncoder reports whether ffmpeg offers the named encoder.
func HasEncoder(t testing.TB, name string) bool {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-hide_banner", "-encoders").Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[1] == name {
			return true
		}
	}
	return false
}

// RequireHardware skips the test unless Probe reports the given capability.
func RequireHardware(t testing.TB, c hwmediacodec.Codec, dir hwmediacodec.Direction) {
	t.Helper()
	caps, err := hwmediacodec.Probe(context.Background())
	if err != nil {
		t.Skipf("probe failed: %v", err)
	}
	for _, cp := range caps {
		if cp.Codec == c && cp.Direction == dir {
			return
		}
	}
	t.Skipf("no hardware %s %s on this machine", c, dir)
}

// MP4Options describes a synthetic test movie.
type MP4Options struct {
	Codec   hwmediacodec.Codec // H264 or HEVC
	Width   int
	Height  int
	Frames  int
	FPS     int
	BFrames int
	GOP     int
	Audio   bool // add an AAC sine tone
	HEV1    bool // HEVC: keep ffmpeg's default hev1 sample entry instead of hvc1
}

// GenerateMP4 writes a test movie with ffmpeg into dir and returns its path.
func GenerateMP4(t testing.TB, dir string, o MP4Options) string {
	t.Helper()
	RequireFFmpeg(t)
	if o.FPS == 0 {
		o.FPS = 30
	}
	if o.GOP == 0 {
		o.GOP = 25
	}
	args := []string{"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=%dx%d:rate=%d", o.Width, o.Height, o.FPS)}
	if o.Audio {
		args = append(args, "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000")
	}
	args = append(args, "-frames:v", fmt.Sprint(o.Frames), "-pix_fmt", "yuv420p")
	name := fmt.Sprintf("test_%s_%dx%d_%d_b%d", o.Codec, o.Width, o.Height, o.Frames, o.BFrames)
	switch o.Codec {
	case hwmediacodec.H264:
		if !HasEncoder(t, "libx264") {
			t.Skip("ffmpeg has no libx264")
		}
		args = append(args, "-c:v", "libx264", "-preset", "veryfast", "-bf", fmt.Sprint(o.BFrames), "-g", fmt.Sprint(o.GOP))
	case hwmediacodec.HEVC:
		if !HasEncoder(t, "libx265") {
			t.Skip("ffmpeg has no libx265")
		}
		args = append(args, "-c:v", "libx265", "-preset", "veryfast",
			"-x265-params", fmt.Sprintf("log-level=error:bframes=%d:keyint=%d:min-keyint=%d:scenecut=0", o.BFrames, o.GOP, o.GOP))
		if !o.HEV1 {
			args = append(args, "-tag:v", "hvc1")
		}
	default:
		t.Fatalf("unsupported codec %s", o.Codec)
	}
	if o.Audio {
		args = append(args, "-c:a", "aac", "-b:a", "96k", "-shortest")
	}
	path := filepath.Join(dir, name+".mp4")
	args = append(args, path)
	if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return path
}

// Stream is one stream of an ffprobe report.
type Stream struct {
	Index     int    `json:"index"`
	CodecType string `json:"codec_type"`
	CodecName string `json:"codec_name"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Packets   string `json:"nb_read_packets"`
	Frames    string `json:"nb_read_frames"`
	HasB      int    `json:"has_b_frames"`
	Duration  string `json:"duration"`
	TimeBase  string `json:"time_base"`
	Tag       string `json:"codec_tag_string"`
}

// PacketCount is nb_read_packets as an int.
func (s Stream) PacketCount() int { n, _ := strconv.Atoi(s.Packets); return n }

// FrameCount is nb_read_frames as an int.
func (s Stream) FrameCount() int { n, _ := strconv.Atoi(s.Frames); return n }

// Seconds is the stream duration in seconds.
func (s Stream) Seconds() float64 { f, _ := strconv.ParseFloat(s.Duration, 64); return f }

// Streams runs ffprobe on path and returns its streams with packet and
// frame counts.
func Streams(t testing.TB, path string) []Stream {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-count_packets", "-count_frames",
		"-show_entries", "stream=index,codec_type,codec_name,width,height,nb_read_packets,nb_read_frames,has_b_frames,duration,time_base,codec_tag_string",
		"-of", "json", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	var rep struct {
		Streams []Stream `json:"streams"`
	}
	if err := json.Unmarshal(out, &rep); err != nil {
		t.Fatalf("ffprobe json: %v", err)
	}
	return rep.Streams
}

// VideoStream returns the first video stream of path.
func VideoStream(t testing.TB, path string) Stream {
	t.Helper()
	for _, s := range Streams(t, path) {
		if s.CodecType == "video" {
			return s
		}
	}
	t.Fatalf("%s has no video stream", path)
	return Stream{}
}

// Frame is one video frame as ffprobe reports it, in display order.
type Frame struct {
	PTS      int64
	Keyframe bool
	PictType string
}

// Frames lists the video frames of path in display order with their PTS
// in the stream time base.
func Frames(t testing.TB, path string) []Frame {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0",
		"-show_entries", "frame=pts,key_frame,pict_type", "-of", "json", path).Output()
	if err != nil {
		t.Fatalf("ffprobe frames %s: %v", path, err)
	}
	var rep struct {
		Frames []struct {
			PTS      int64  `json:"pts"`
			Key      int    `json:"key_frame"`
			PictType string `json:"pict_type"`
		} `json:"frames"`
	}
	if err := json.Unmarshal(out, &rep); err != nil {
		t.Fatalf("ffprobe frames json: %v", err)
	}
	frames := make([]Frame, len(rep.Frames))
	for i, f := range rep.Frames {
		frames[i] = Frame{PTS: f.PTS, Keyframe: f.Key == 1, PictType: f.PictType}
	}
	return frames
}

// FrameMD5 decodes the video of path with ffmpeg and returns one MD5 per
// output frame in display order. input is an optional "-f" format for raw
// elementary streams.
func FrameMD5(t testing.TB, path string, format string) []string {
	t.Helper()
	args := []string{"-v", "error"}
	if format != "" {
		args = append(args, "-f", format)
	}
	args = append(args, "-i", path, "-an", "-f", "framemd5", "-")
	out, err := exec.Command("ffmpeg", args...).Output()
	if err != nil {
		t.Fatalf("ffmpeg framemd5 %s: %v", path, err)
	}
	var sums []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Split(line, ",")
		sums = append(sums, strings.TrimSpace(f[len(f)-1]))
	}
	return sums
}

// CheckDecodes fails the test when ffmpeg reports any error decoding path.
func CheckDecodes(t testing.TB, path string) {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-v", "error", "-i", path, "-f", "null", "-").CombinedOutput()
	if err != nil || len(bytes.TrimSpace(out)) > 0 {
		t.Fatalf("ffmpeg reports problems in %s: %v\n%s", path, err, out)
	}
}

var psnrRE = regexp.MustCompile(`average:([0-9.]+|inf)`)

// PSNR compares the video of a with the video of b frame by frame and
// returns ffmpeg's average PSNR in dB (positive infinity when identical).
func PSNR(t testing.TB, a, b string) float64 {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-hide_banner", "-i", a, "-i", b,
		"-lavfi", "[0:v][1:v]psnr", "-f", "null", "-").CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg psnr: %v\n%s", err, out)
	}
	m := psnrRE.FindSubmatch(out)
	if m == nil {
		t.Fatalf("no PSNR in ffmpeg output:\n%s", out)
	}
	if string(m[1]) == "inf" {
		return math.Inf(1)
	}
	v, _ := strconv.ParseFloat(string(m[1]), 64)
	return v
}

// ExtractFrame decodes frame n (0-based, display order) of path to a PNG
// file and returns its path.
func ExtractFrame(t testing.TB, path string, n int, dir string) string {
	t.Helper()
	out := filepath.Join(dir, fmt.Sprintf("ref_%d.png", n))
	args := []string{"-v", "error", "-y", "-i", path, "-vf", fmt.Sprintf("select=eq(n\\,%d)", n), "-frames:v", "1", out}
	if o, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg extract frame %d: %v\n%s", n, err, o)
	}
	return out
}
