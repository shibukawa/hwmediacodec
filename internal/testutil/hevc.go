package testutil

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// GenerateHEVC encodes a synthetic test pattern with libx265. params is the
// value of -x265-params (without log-level); extra holds further ffmpeg
// output options such as "-pix_fmt", "yuv420p10le". It returns the path of
// the Annex-B stream.
func GenerateHEVC(t testing.TB, width, height, frames int, params string, extra ...string) string {
	t.Helper()
	ffmpeg := RequireFFmpeg(t)
	if !HasEncoder(t, "libx265") {
		t.Skip("ffmpeg has no libx265 encoder")
	}
	path := filepath.Join(t.TempDir(), "stream.hevc")
	if params != "" {
		params += ":"
	}
	args := []string{"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=%dx%d:rate=30", width, height),
		"-frames:v", fmt.Sprint(frames), "-pix_fmt", "yuv420p", "-c:v", "libx265",
		"-x265-params", params + "log-level=error"}
	args = append(args, extra...)
	args = append(args, "-f", "hevc", path)
	cmd := exec.Command(ffmpeg, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ffmpeg generate failed: %v\n%s", err, stderr.String())
	}
	return path
}

// X265Frame is what the x265 command line encoder logged for one frame in
// its CSV report: the frame's position in display order and the display
// positions of the pictures in its two reference lists.
type X265Frame struct {
	POC   int
	Type  string // "I-SLICE", "P-SLICE", "B-SLICE", "b-SLICE"
	List0 []int
	List1 []int
}

// GenerateX265 encodes a synthetic test pattern with the x265 command line
// encoder, which (unlike libx265 inside ffmpeg) can report the reference
// lists of every frame. It returns the stream path and the frames in
// encode (decoding) order. The test is skipped when x265 is not installed.
func GenerateX265(t testing.TB, width, height, frames int, args ...string) (string, []X265Frame) {
	t.Helper()
	return GenerateX265Source(t, fmt.Sprintf("testsrc2=size=%dx%d:rate=30", width, height), frames, args...)
}

// FadeSource is a lavfi source whose brightness fades in and out, which
// makes encoders use weighted prediction.
func FadeSource(width, height int) string {
	return fmt.Sprintf("testsrc2=size=%dx%d:rate=30,fade=t=in:st=0:d=0.8,fade=t=out:st=1.0:d=0.6", width, height)
}

// GenerateX265Source is GenerateX265 for an arbitrary lavfi source graph.
func GenerateX265Source(t testing.TB, source string, frames int, args ...string) (string, []X265Frame) {
	t.Helper()
	ffmpeg := RequireFFmpeg(t)
	x265, err := exec.LookPath("x265")
	if err != nil {
		t.Skip("x265 command line encoder not found in PATH")
	}
	dir := t.TempDir()
	y4m := filepath.Join(dir, "in.y4m")
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", source,
		"-frames:v", fmt.Sprint(frames), "-pix_fmt", "yuv420p", y4m)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg y4m failed: %v\n%s", err, tail(out))
	}
	path := filepath.Join(dir, "stream.hevc")
	report := filepath.Join(dir, "frames.csv")
	full := append([]string{"--log-level", "error", "--csv", report, "--csv-log-level", "1"}, args...)
	full = append(full, "-o", path, y4m)
	if out, err := exec.Command(x265, full...).CombinedOutput(); err != nil {
		t.Fatalf("x265 failed: %v\n%s", err, tail(out))
	}
	f, err := os.Open(report)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rd := csv.NewReader(f)
	rd.FieldsPerRecord = -1
	rd.TrimLeadingSpace = true
	rows, err := rd.ReadAll()
	if err != nil {
		t.Fatalf("x265 csv: %v", err)
	}
	if len(rows) < 2 {
		t.Fatalf("x265 csv has %d rows", len(rows))
	}
	col := map[string]int{}
	for i, name := range rows[0] {
		col[strings.TrimSpace(name)] = i
	}
	for _, name := range []string{"Encode Order", "Type", "POC", "List 0", "List 1"} {
		if _, ok := col[name]; !ok {
			t.Skipf("x265 csv has no %q column (columns: %v)", name, rows[0])
		}
	}
	list := func(s string) []int {
		var out []int
		for _, f := range strings.Fields(s) {
			if v, err := strconv.Atoi(f); err == nil {
				out = append(out, v)
			}
		}
		return out
	}
	byOrder := map[int]X265Frame{}
	for _, row := range rows[1:] {
		if len(row) <= col["List 1"] {
			continue
		}
		order, err := strconv.Atoi(strings.TrimSpace(row[col["Encode Order"]]))
		if err != nil {
			continue // summary rows
		}
		poc, _ := strconv.Atoi(strings.TrimSpace(row[col["POC"]]))
		byOrder[order] = X265Frame{POC: poc, Type: strings.TrimSpace(row[col["Type"]]),
			List0: list(row[col["List 0"]]), List1: list(row[col["List 1"]])}
	}
	out := make([]X265Frame, 0, len(byOrder))
	for i := 0; i < len(byOrder); i++ {
		fr, ok := byOrder[i]
		if !ok {
			t.Fatalf("x265 csv misses encode order %d", i)
		}
		out = append(out, fr)
	}
	return path, out
}
