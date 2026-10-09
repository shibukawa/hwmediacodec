package testutil

import (
	"bufio"
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// GenerateH264 encodes a synthetic test pattern with libx264 and the given
// extra ffmpeg arguments (encoder options such as "-bf", "3" or
// "-x264-params", "..."). It returns the path of the Annex-B stream.
func GenerateH264(t testing.TB, width, height, frames int, extra ...string) string {
	t.Helper()
	ffmpeg := RequireFFmpeg(t)
	if !HasEncoder(t, "libx264") {
		t.Skip("ffmpeg has no libx264 encoder")
	}
	path := filepath.Join(t.TempDir(), "stream.h264")
	args := []string{"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=%dx%d:rate=30", width, height),
		"-frames:v", fmt.Sprint(frames), "-pix_fmt", "yuv420p", "-c:v", "libx264"}
	args = append(args, extra...)
	args = append(args, "-f", "h264", path)
	cmd := exec.Command(ffmpeg, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ffmpeg generate failed: %v\n%s", err, stderr.String())
	}
	return path
}

// TraceField is one syntax element printed by ffmpeg's trace_headers
// bitstream filter.
type TraceField struct {
	Pos   int    // bit position inside the NAL unit (after emulation prevention removal)
	Name  string // syntax element name, with array indices as printed
	Bits  int    // number of bits the element occupies
	Value int64
}

// TraceUnit is one NAL unit as printed by trace_headers.
type TraceUnit struct {
	Kind   string // "Sequence Parameter Set", "Picture Parameter Set", "Slice Header", ...
	Fields []TraceField
}

var (
	traceFieldRe  = regexp.MustCompile(`^\[trace_headers @ [^\]]+\] (\d+) +([A-Za-z_0-9\[\]]+) +([01]+) = (-?\d+)\s*$`)
	traceKindRe   = regexp.MustCompile(`^\[trace_headers @ [^\]]+\] ([A-Za-z][A-Za-z ]+[A-Za-z])\s*$`)
	tracePacketRe = regexp.MustCompile(`^\[trace_headers @ [^\]]+\] Packet: `)
)

// TraceHeaders runs ffmpeg's trace_headers bitstream filter over an
// elementary stream and returns the NAL units it printed, in stream order.
// The copy of the parameter sets that ffmpeg prints as "Extradata" before
// the first packet is omitted.
func TraceHeaders(t testing.TB, path string, format string) []TraceUnit {
	t.Helper()
	ffmpeg := RequireFFmpeg(t)
	cmd := exec.Command(ffmpeg, "-hide_banner", "-f", format, "-i", path, "-c", "copy", "-bsf:v", "trace_headers", "-f", "null", "-")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg trace_headers failed: %v\n%s", err, tail(out))
	}
	var units []TraceUnit
	var cur *TraceUnit
	inPackets := false
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	for sc.Scan() {
		line := sc.Text()
		if tracePacketRe.MatchString(line) {
			inPackets = true
			cur = nil
			continue
		}
		if m := traceFieldRe.FindStringSubmatch(line); m != nil {
			if cur == nil {
				continue
			}
			pos, _ := strconv.Atoi(m[1])
			val, _ := strconv.ParseInt(m[4], 10, 64)
			cur.Fields = append(cur.Fields, TraceField{Pos: pos, Name: m[2], Bits: len(m[3]), Value: val})
			continue
		}
		if m := traceKindRe.FindStringSubmatch(line); m != nil {
			if !inPackets {
				cur = nil
				continue
			}
			units = append(units, TraceUnit{Kind: strings.TrimSpace(m[1])})
			cur = &units[len(units)-1]
		}
	}
	return units
}

// RefEntry is one reference picture as printed by ffmpeg's "-debug mmco".
// Index is the position in ffmpeg's list (the LongTermFrameIdx for
// long-term references).
type RefEntry struct {
	Index    int
	FrameNum int
	POC      int
}

// RefList is ffmpeg's view of the DPB after a picture.
type RefList struct {
	Short []RefEntry
	Long  []RefEntry
}

// Equal reports whether two lists hold the same entries in the same order.
func (a RefList) Equal(b RefList) bool {
	if len(a.Short) != len(b.Short) || len(a.Long) != len(b.Long) {
		return false
	}
	for i := range a.Short {
		if a.Short[i] != b.Short[i] {
			return false
		}
	}
	for i := range a.Long {
		if a.Long[i] != b.Long[i] {
			return false
		}
	}
	return true
}

var (
	refEntryRe  = regexp.MustCompile(`^\[h264 @ ([^\]]+)\] (\d+) fn:(\d+) poc:(-?\d+) `)
	refHeaderRe = regexp.MustCompile(`^\[h264 @ ([^\]]+)\] (short|long) term list:`)
)

// DPBTrace decodes the stream with ffmpeg's software decoder (one thread)
// and returns the sequence of distinct DPB states it logged with
// "-debug mmco": the empty state before the first picture followed by the
// state after every reference picture. Consecutive identical states are
// collapsed.
func DPBTrace(t testing.TB, path string) []RefList {
	t.Helper()
	ffmpeg := RequireFFmpeg(t)
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "debug", "-threads", "1", "-debug", "mmco", "-f", "h264", "-i", path, "-f", "null", "-")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg -debug mmco failed: %v\n%s", err, tail(out))
	}
	// ffmpeg decodes once while probing the stream and once for real, each
	// with its own codec context; keep the context that logged the most.
	type block struct {
		ctx  string
		list RefList
		long bool
	}
	var blocks []block
	var cur *block
	counts := map[string]int{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	for sc.Scan() {
		line := sc.Text()
		if m := refHeaderRe.FindStringSubmatch(line); m != nil {
			if m[2] == "short" {
				blocks = append(blocks, block{ctx: m[1]})
				cur = &blocks[len(blocks)-1]
				counts[m[1]]++
			} else if cur != nil && cur.ctx == m[1] {
				cur.long = true
			}
			continue
		}
		if m := refEntryRe.FindStringSubmatch(line); m != nil && cur != nil && cur.ctx == m[1] {
			idx, _ := strconv.Atoi(m[2])
			fn, _ := strconv.Atoi(m[3])
			poc, _ := strconv.Atoi(m[4])
			e := RefEntry{Index: idx, FrameNum: fn, POC: poc}
			if cur.long {
				cur.list.Long = append(cur.list.Long, e)
			} else {
				cur.list.Short = append(cur.list.Short, e)
			}
		}
	}
	best := ""
	for ctx, n := range counts {
		if best == "" || n > counts[best] {
			best = ctx
		}
	}
	var states []RefList
	for _, b := range blocks {
		if b.ctx != best {
			continue
		}
		if len(states) > 0 && states[len(states)-1].Equal(b.list) {
			continue
		}
		states = append(states, b.list)
	}
	return states
}

func tail(b []byte) string {
	if len(b) > 4000 {
		return "...\n" + string(b[len(b)-4000:])
	}
	return string(b)
}

// SlicePOC is what ffmpeg logs for one slice with "-debug pict".
type SlicePOC struct {
	FrameNum  int
	TopPOC    int
	BottomPOC int
}

var slicePOCRe = regexp.MustCompile(`^\[h264 @ ([^\]]+)\] slice:\d+ .* frame:(\d+) poc:(-?\d+)/(-?\d+) `)

// POCTrace decodes the stream with ffmpeg (one thread) and returns the
// frame_num and picture order count ffmpeg derived for every slice, in
// decoding order. ffmpeg offsets POC values internally by a constant.
func POCTrace(t testing.TB, path string) []SlicePOC {
	t.Helper()
	ffmpeg := RequireFFmpeg(t)
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "debug", "-threads", "1", "-debug", "pict", "-f", "h264", "-i", path, "-f", "null", "-")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg -debug pict failed: %v\n%s", err, tail(out))
	}
	byCtx := map[string][]SlicePOC{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	for sc.Scan() {
		m := slicePOCRe.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		fn, _ := strconv.Atoi(m[2])
		top, _ := strconv.Atoi(m[3])
		bottom, _ := strconv.Atoi(m[4])
		byCtx[m[1]] = append(byCtx[m[1]], SlicePOC{FrameNum: fn, TopPOC: top, BottomPOC: bottom})
	}
	// ffmpeg decodes once while probing and once for real; keep the
	// context that logged the most slices.
	var best []SlicePOC
	for _, v := range byCtx {
		if len(v) > len(best) {
			best = v
		}
	}
	return best
}
