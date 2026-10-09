package hwmediacodec_test

// End-to-end tests of the vaapi backend against the fake VA driver in
// internal/vaapi/testdata/fakedriver. They run the real backend through the
// real libva on a machine without a GPU: the driver checks every buffer it
// is handed through the C structure definitions and reports what it saw in
// a log, and "decodes" each picture into a pattern that names its picture
// order count. scripts/vaapi_fake_driver_test.sh builds the driver in a
// container and runs these tests in each driver mode; elsewhere they skip.

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/annexb"
	"github.com/shibukawa/hwmediacodec/internal/h264"
	"github.com/shibukawa/hwmediacodec/internal/hevc"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
)

// requireFakeVAAPI skips unless the process runs against the fake driver,
// and empties the driver's log.
func requireFakeVAAPI(t *testing.T) {
	t.Helper()
	if os.Getenv("HWMEDIACODEC_TEST_FAKE_VAAPI") == "" {
		t.Skip("needs the fake VA driver; run scripts/vaapi_fake_driver_test.sh")
	}
	if err := os.WriteFile(fakeLogPath(t), nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func fakeLogPath(t *testing.T) string {
	t.Helper()
	p := os.Getenv("FAKE_VA_LOG")
	if p == "" {
		t.Fatal("FAKE_VA_LOG is not set")
	}
	return p
}

func fakeEnvInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil {
		return v
	}
	return def
}

// fakeLog reads the driver's log: one map per "dec" or "enc" line. Any
// "error" line fails the test.
func fakeLog(t *testing.T, kind string) []map[string]string {
	t.Helper()
	f, err := os.Open(fakeLogPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []map[string]string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "error" {
			t.Errorf("fake driver: %s", sc.Text())
			continue
		}
		if fields[0] != kind {
			continue
		}
		m := map[string]string{}
		for _, kv := range fields[1:] {
			if k, v, ok := strings.Cut(kv, "="); ok {
				m[k] = v
			}
		}
		out = append(out, m)
	}
	return out
}

func logInt(t *testing.T, m map[string]string, key string) int {
	t.Helper()
	v, err := strconv.ParseInt(m[key], 0, 64)
	if err != nil {
		t.Fatalf("log field %s=%q: %v", key, m[key], err)
	}
	return int(v)
}

func TestFakeVAAPIProbe(t *testing.T) {
	requireFakeVAAPI(t)
	caps, err := hwmediacodec.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, c := range caps {
		if c.Backend != "vaapi" || !c.Hardware || c.MaxWidth != 4096 {
			t.Errorf("unexpected capability %+v", c)
		}
		found[c.Codec.String()+"/"+c.Direction.String()] = true
	}
	if found["av1/encode"] {
		t.Error("Probe reports av1/encode, which the driver does not offer")
	}
	for _, want := range []string{"h264/decode", "h264/encode", "hevc/decode", "hevc/encode", "av1/decode"} {
		if !found[want] {
			t.Errorf("Probe does not report %s (got %v)", want, found)
		}
	}
	fakeLog(t, "dec")
}

// checkFakeFrame verifies that a decoded frame holds the pattern the fake
// driver paints for one picture order count, from the top-left corner of
// the conformance window, and returns that count modulo 256.
func checkFakeFrame(t *testing.T, index int, f decodedFrame, width, height int) int {
	t.Helper()
	if f.width != width || f.height != height {
		t.Fatalf("frame %d is %dx%d, want %dx%d", index, f.width, f.height, width, height)
	}
	p := int(f.pix[0])
	cw := (width + 1) / 2 * 2
	chroma := f.pix[width*height:]
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if got, want := f.pix[y*width+x], uint8(p+x+2*y); got != want {
				t.Fatalf("frame %d (POC %d): luma at %d,%d is %d, want %d", index, p, x, y, got, want)
			}
		}
	}
	for y := 0; y < (height+1)/2; y++ {
		for x := 0; x < cw/2; x++ {
			if cb, cr := chroma[y*cw+2*x], chroma[y*cw+2*x+1]; cb != uint8(p+x) || cr != uint8(p+3*y) {
				t.Fatalf("frame %d (POC %d): chroma at %d,%d is %d/%d, want %d/%d", index, p, x, y, cb, cr, uint8(p+x), uint8(p+3*y))
			}
		}
	}
	return p
}

// checkFakeOrder verifies display order: the picture order count grows by
// step from frame to frame and restarts at zero when a new sequence begins.
func checkFakeOrder(t *testing.T, pocs []int, step int) {
	t.Helper()
	restarts := 0
	for i := 1; i < len(pocs); i++ {
		switch {
		case pocs[i] == (pocs[i-1]+step)&0xff:
		case pocs[i] == 0:
			restarts++
		default:
			t.Fatalf("frame %d has POC %d after %d: not in display order (all: %v)", i, pocs[i], pocs[i-1], pocs)
		}
	}
	if len(pocs) > 0 && pocs[0] != 0 {
		t.Errorf("first frame has POC %d", pocs[0])
	}
	t.Logf("%d frames in display order, %d sequence restarts", len(pocs), restarts)
}

// TestFakeVAAPIDecode decodes real streams through the backend. The driver
// checks reference surfaces, lists and slice parameters; the frames that
// come back prove that the right surface is copied out for every picture,
// cropped and in display order.
func TestFakeVAAPIDecode(t *testing.T) {
	requireFakeVAAPI(t)
	fade := []string{"-vf", "fade=t=in:st=0:d=0.8,fade=t=out:st=1.0:d=0.6"}
	type stream struct {
		name   string
		codec  hwmediacodec.Codec
		w, h   int
		frames int
		gen    func(t *testing.T) string
	}
	generic := func(c hwmediacodec.Codec, w, h, frames, bframes int) func(*testing.T) string {
		return func(t *testing.T) string { return testutil.GenerateStreamBFrames(t, c, w, h, frames, bframes).Path }
	}
	x265 := func(w, h, frames int, params string, extra ...string) func(*testing.T) string {
		return func(t *testing.T) string { return testutil.GenerateHEVC(t, w, h, frames, params, extra...) }
	}
	x264 := func(w, h, frames int, extra ...string) func(*testing.T) string {
		return func(t *testing.T) string { return testutil.GenerateH264(t, w, h, frames, extra...) }
	}
	streams := []stream{
		{"h264/ip", hwmediacodec.H264, 320, 240, 30, generic(hwmediacodec.H264, 320, 240, 30, 0)},
		{"h264/bframes", hwmediacodec.H264, 320, 240, 60, generic(hwmediacodec.H264, 320, 240, 60, 3)},
		{"h264/cropped", hwmediacodec.H264, 322, 242, 30, generic(hwmediacodec.H264, 322, 242, 30, 2)},
		{"h264/refs-slices", hwmediacodec.H264, 320, 240, 48, x264(320, 240, 48, "-preset", "slow", "-bf", "3", "-b-pyramid", "normal", "-refs", "6", "-g", "24",
			"-x264-params", "slices=3:weightp=2:scenecut=0")},
		{"hevc/ip", hwmediacodec.HEVC, 320, 240, 30, generic(hwmediacodec.HEVC, 320, 240, 30, 0)},
		{"hevc/bframes", hwmediacodec.HEVC, 320, 240, 60, generic(hwmediacodec.HEVC, 320, 240, 60, 3)},
		{"hevc/cropped", hwmediacodec.HEVC, 322, 242, 30, generic(hwmediacodec.HEVC, 322, 242, 30, 2)},
		// No multi-slice HEVC here: libx265 4.1 as packaged by Debian
		// writes corrupt streams (or crashes) with slices > 1. Slice
		// segments are covered by TestHEVCDecodeBuffers and, on hardware,
		// by TestDecodeHEVCCodingTools.
		{"hevc/bpyramid-refs", hwmediacodec.HEVC, 320, 240, 48, x265(320, 240, 48, "bframes=4:b-pyramid=1:ref=5:keyint=24:min-keyint=24:open-gop=0")},
		{"hevc/weights-scaling", hwmediacodec.HEVC, 320, 240, 48, x265(320, 240, 48, "bframes=3:ref=3:weightp=1:weightb=1:scaling-list=default:keyint=48:open-gop=0", fade...)},
		{"hevc/open-gop", hwmediacodec.HEVC, 320, 240, 40, x265(320, 240, 40, "bframes=3:b-pyramid=1:keyint=10:min-keyint=10:open-gop=1")},
	}
	for _, s := range streams {
		t.Run(s.name, func(t *testing.T) {
			requireFakeVAAPI(t)
			data := testutil.ReadFile(t, s.gen(t))
			dec, err := hwmediacodec.NewDecoder(context.Background(), s.codec)
			if err != nil {
				t.Fatalf("NewDecoder: %v", err)
			}
			frames := decodeFrames(t, dec, s.codec, data, hwmediacodec.NV12)
			if err := dec.Close(); err != nil {
				t.Fatal(err)
			}
			if len(frames) != s.frames {
				t.Fatalf("decoded %d frames, want %d", len(frames), s.frames)
			}
			var pocs []int
			for i, f := range frames {
				pocs = append(pocs, checkFakeFrame(t, i, f, s.w, s.h))
			}
			step := 1
			if s.codec == hwmediacodec.H264 {
				step = 2 // frames count two fields
			}
			checkFakeOrder(t, pocs, step)
			// Every timestamp given to Send comes back exactly once (in
			// decode order they were 0, 3000, 6000, ...).
			seen := map[int64]bool{}
			for i, f := range frames {
				if f.pts%3000 != 0 || f.pts < 0 || f.pts >= int64(3000*s.frames) || seen[f.pts] {
					t.Fatalf("frame %d has PTS %d", i, f.pts)
				}
				seen[f.pts] = true
			}
			log := fakeLog(t, "dec")
			if len(log) != s.frames {
				t.Errorf("the driver decoded %d pictures, want %d", len(log), s.frames)
			}
			maxRefs := 0
			for _, l := range log {
				if l["codec"] != s.codec.String() {
					t.Fatalf("the driver saw codec %q", l["codec"])
				}
				maxRefs = max(maxRefs, logInt(t, l, "refs"))
			}
			t.Logf("up to %d reference pictures per picture", maxRefs)
		})
	}
}

// TestFakeVAAPIDecodeSequenceChange decodes two streams of different size
// back to back and a seek: the surface pool is rebuilt at the new sequence
// and decoding resumes at the keyframe after a flush.
func TestFakeVAAPIDecodeSequenceChange(t *testing.T) {
	for _, c := range []hwmediacodec.Codec{hwmediacodec.H264, hwmediacodec.HEVC} {
		t.Run(c.String(), func(t *testing.T) {
			requireFakeVAAPI(t)
			s1 := testutil.GenerateStreamBFrames(t, c, 320, 240, 30, 2)
			s2 := testutil.GenerateStreamBFrames(t, c, 160, 120, 30, 2)
			stream := append(testutil.ReadFile(t, s1.Path), testutil.ReadFile(t, s2.Path)...)
			dec, err := hwmediacodec.NewDecoder(context.Background(), c)
			if err != nil {
				t.Fatal(err)
			}
			defer dec.Close()
			frames := decodeFrames(t, dec, c, stream, hwmediacodec.NV12)
			if len(frames) != 60 {
				t.Fatalf("decoded %d frames, want 60", len(frames))
			}
			for i, f := range frames {
				w, h := 320, 240
				if i >= 30 {
					w, h = 160, 120
				}
				checkFakeFrame(t, i, f, w, h)
			}

			// The decoder was flushed by decodeFrames. A non-keyframe is
			// skipped, then the first stream decodes again from its start.
			ctx := context.Background()
			aus := accessUnits(t, c, testutil.ReadFile(t, s1.Path))
			if err := dec.Send(ctx, hwmediacodec.Packet{Data: aus[5]}); err != nil {
				t.Fatalf("Send non-keyframe after flush: %v", err)
			}
			if _, err := dec.Receive(ctx); !errors.Is(err, hwmediacodec.ErrAgain) {
				t.Fatalf("Receive after a skipped picture: %v", err)
			}
			again := decodeFrames(t, dec, c, testutil.ReadFile(t, s1.Path), hwmediacodec.NV12)
			if len(again) != 30 {
				t.Fatalf("decoded %d frames after the seek, want 30", len(again))
			}
			for i, f := range again {
				checkFakeFrame(t, i, f, 320, 240)
			}
			if n := len(fakeLog(t, "dec")); n != 90 {
				t.Errorf("the driver decoded %d pictures, want 90", n)
			}
		})
	}
}

func accessUnits(t *testing.T, c hwmediacodec.Codec, data []byte) [][]byte {
	t.Helper()
	r := annexb.NewReader(bytes.NewReader(data), c)
	var out [][]byte
	for {
		au, err := r.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, au)
	}
}

// fakeInputFrame builds NV12 frame k: Y(x,y) = k + x + 2y, Cb = k + x,
// Cr = k + 3y.
func fakeInputFrame(k, width, height int) []byte {
	buf := make([]byte, 0, width*height*3/2)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			buf = append(buf, uint8(k+x+2*y))
		}
	}
	for y := 0; y < height/2; y++ {
		for x := 0; x < width/2; x++ {
			buf = append(buf, uint8(k+x), uint8(k+3*y))
		}
	}
	return buf
}

// fakeInputChecksum is the byte sum the driver computes over its input
// surface of the coded size: frame k padded by repeating the last column
// and row.
func fakeInputChecksum(k, width, height, codedW, codedH int) uint32 {
	var sum uint32
	for y := 0; y < codedH; y++ {
		for x := 0; x < codedW; x++ {
			sum += uint32(uint8(k + min(x, width-1) + 2*min(y, height-1)))
		}
	}
	for y := 0; y < codedH/2; y++ {
		for x := 0; x < codedW/2; x++ {
			cx, cy := min(x, width/2-1), min(y, height/2-1)
			sum += uint32(uint8(k+cx)) + uint32(uint8(k+3*cy))
		}
	}
	return sum
}

// TestFakeVAAPIEncode encodes patterned frames through the backend. The
// driver checks the parameter buffers of every picture and reports what it
// was given; where it takes packed slice headers the stream that comes back
// carries the backend's own headers and is walked with the parsers.
func TestFakeVAAPIEncode(t *testing.T) {
	requireFakeVAAPI(t)
	packedMask := fakeEnvInt("FAKE_VA_PACKED", 1)
	const frames, gop, forced = 26, 10, 13
	keyframes := map[int]bool{0: true, 10: true, 13: true, 20: true}
	for _, tc := range []struct {
		name string
		w, h int
		opts []hwmediacodec.EncoderOption
		rc   int
		bps  int
		qp   int
	}{
		{"constant-qp", 322, 242, nil, 0x10, 0, 26},
		{"quality", 320, 240, []hwmediacodec.EncoderOption{hwmediacodec.WithQuality(0.5)}, 0x10, 0, 29},
		{"cbr", 320, 240, []hwmediacodec.EncoderOption{hwmediacodec.WithBitrate(1_000_000), hwmediacodec.WithRateControl(hwmediacodec.CBR)}, 0x2, 1_000_000, 26},
		{"vbr", 1280, 720, []hwmediacodec.EncoderOption{hwmediacodec.WithBitrate(3_000_000)}, 0x4, 6_000_000, 26},
	} {
		for _, c := range []hwmediacodec.Codec{hwmediacodec.H264, hwmediacodec.HEVC} {
			t.Run(c.String()+"/"+tc.name, func(t *testing.T) {
				requireFakeVAAPI(t)
				opts := append([]hwmediacodec.EncoderOption{hwmediacodec.WithFrameRate(30), hwmediacodec.WithKeyframeInterval(gop)}, tc.opts...)
				enc, err := hwmediacodec.NewEncoder(context.Background(), c, tc.w, tc.h, opts...)
				if err != nil {
					t.Fatalf("NewEncoder: %v", err)
				}
				var src [][]byte
				for k := 0; k < frames; k++ {
					src = append(src, fakeInputFrame(k, tc.w, tc.h))
				}
				pkts := encodeAll(t, enc, src, tc.w, tc.h, forced)
				if err := enc.Close(); err != nil {
					t.Fatal(err)
				}
				if len(pkts) != frames {
					t.Fatalf("got %d packets for %d frames", len(pkts), frames)
				}
				checkParameterSets(t, c, pkts)
				for i, p := range pkts {
					if p.Keyframe != keyframes[i] {
						t.Errorf("packet %d: Keyframe = %v", i, p.Keyframe)
					}
					if p.PTS != int64(i)*testPTSStep || p.DTS != p.PTS {
						t.Errorf("packet %d: PTS %d DTS %d", i, p.PTS, p.DTS)
					}
				}

				// The coded size the backend chose: macroblocks for H.264,
				// the driver's minimum coding block for HEVC (8 when it
				// reports block sizes, a guessed 16 otherwise).
				align := 16
				if c == hwmediacodec.HEVC && fakeEnvInt("FAKE_VA_HEVC_ATTRS", 1) != 0 {
					align = 8
				}
				codedW, codedH := (tc.w+align-1)/align*align, (tc.h+align-1)/align*align
				pocStep := 2
				if c == hwmediacodec.HEVC {
					pocStep = 1
				}
				log := fakeLog(t, "enc")
				if len(log) != frames {
					t.Fatalf("the driver encoded %d pictures, want %d", len(log), frames)
				}
				sinceIDR := 0
				for k, l := range log {
					if keyframes[k] {
						sinceIDR = 0
					}
					want := map[string]int{
						"n": k, "idr": b2int(keyframes[k]), "poc": pocStep * sinceIDR, "qp": tc.qp, "rc": tc.rc,
						"in00": int(uint8(k)), "sum": int(fakeInputChecksum(k, tc.w, tc.h, codedW, codedH)),
						"seq": b2int(keyframes[k]), "packed_seq": b2int(keyframes[k] && packedMask&1 != 0), "packed_slice": b2int(packedMask&4 != 0),
					}
					if keyframes[k] {
						want["bps"] = tc.bps
					}
					for key, v := range want {
						if got := logInt(t, l, key); got != v {
							t.Errorf("picture %d: the driver saw %s=%d, want %d (line %v)", k, key, got, v, l)
						}
					}
					if l["codec"] != c.String() {
						t.Errorf("picture %d: codec %q", k, l["codec"])
					}
					sinceIDR++
				}

				var stream []byte
				for _, p := range pkts {
					stream = append(stream, p.Data...)
				}
				if packedMask&4 != 0 {
					walkEncodedStream(t, c, stream, tc.w, tc.h, keyframes)
				}
			})
		}
	}
}

func b2int(b bool) int {
	if b {
		return 1
	}
	return 0
}

// walkEncodedStream parses a stream whose slice headers the backend wrote
// and runs it through the decoder-side bookkeeping: every picture must be
// an IDR picture where a keyframe was produced and otherwise a P picture
// that references exactly the picture before it.
func walkEncodedStream(t *testing.T, c hwmediacodec.Codec, stream []byte, width, height int, keyframes map[int]bool) {
	t.Helper()
	aus := accessUnits(t, c, stream)
	if len(aus) != 26 {
		t.Fatalf("stream has %d access units", len(aus))
	}
	switch c {
	case hwmediacodec.HEVC:
		ps := hevc.NewParameterSets()
		dpb := hevc.NewDPB()
		for i, au := range aus {
			for _, nal := range annexb.Split(au) {
				switch typ := hevc.Type(nal); {
				case typ == hevc.NALSPS:
					s, err := ps.AddSPS(nal)
					if err != nil {
						t.Fatalf("picture %d: SPS: %v", i, err)
					}
					if _, _, w, h := s.Crop(); w != width || h != height {
						t.Fatalf("SPS describes %dx%d, want %dx%d", w, h, width, height)
					}
				case typ == hevc.NALPPS:
					if _, err := ps.AddPPS(nal); err != nil {
						t.Fatalf("picture %d: PPS: %v", i, err)
					}
				case hevc.IsSlice(typ):
					sh, sps, _, err := hevc.ParseSliceHeader(nal, ps, nil)
					if err != nil {
						t.Fatalf("picture %d: slice header: %v", i, err)
					}
					if hevc.IsIDR(typ) != keyframes[i] || (sh.SliceType == hevc.SliceI) != keyframes[i] {
						t.Fatalf("picture %d: NAL type %d slice type %d", i, typ, sh.SliceType)
					}
					cur, err := dpb.Start(sps, sh, i)
					if err != nil {
						t.Fatalf("picture %d: %v", i, err)
					}
					l0, _ := dpb.RefPicLists(sh)
					if !keyframes[i] && (len(l0) != 1 || l0[0].Missing || l0[0].POC != cur.POC-1 || l0[0].Handle != i-1) {
						t.Fatalf("picture %d (POC %d): reference list %+v", i, cur.POC, l0)
					}
					dpb.Finish()
				}
			}
		}
	case hwmediacodec.H264:
		ps := h264.NewParameterSets()
		dpb := h264.NewDPB(discardSurfaces{})
		for i, au := range aus {
			for _, nal := range annexb.Split(au) {
				_, typ, _ := h264.NALHeader(nal)
				switch typ {
				case h264.NALSPS:
					s, err := ps.AddSPS(nal)
					if err != nil {
						t.Fatalf("picture %d: SPS: %v", i, err)
					}
					if _, _, w, h := s.Crop(); w != width || h != height {
						t.Fatalf("SPS describes %dx%d, want %dx%d", w, h, width, height)
					}
				case h264.NALPPS:
					if err := ps.AddPPS(nal); err != nil {
						t.Fatalf("picture %d: PPS: %v", i, err)
					}
				case h264.NALSlice, h264.NALSliceIDR:
					sh, sps, _, err := h264.ParseSliceHeader(nal, ps)
					if err != nil {
						t.Fatalf("picture %d: slice header: %v", i, err)
					}
					if sh.IDR != keyframes[i] {
						t.Fatalf("picture %d: IDR = %v", i, sh.IDR)
					}
					cur, err := dpb.Start(sps, sh, i)
					if err != nil {
						t.Fatalf("picture %d: %v", i, err)
					}
					l0, _ := dpb.RefPicLists(sh)
					if !keyframes[i] && (len(l0) != 1 || l0[0].POC() != cur.POC()-2 || l0[0].Handle != i-1) {
						t.Fatalf("picture %d (POC %d): reference list %+v", i, cur.POC(), l0)
					}
					if err := dpb.Finish(sh); err != nil {
						t.Fatalf("picture %d: %v", i, err)
					}
				}
			}
		}
	}
}

type discardSurfaces struct{}

func (discardSurfaces) Allocate() (any, error) { return nil, fmt.Errorf("unexpected frame_num gap") }
func (discardSurfaces) Release(*h264.Picture)  {}

// TestFakeVAAPIEncodeFlushAndTranscode reuses an encoder after Flush and
// feeds a decoder's frames to an encoder, the batch transcode path.
func TestFakeVAAPIEncodeFlushAndTranscode(t *testing.T) {
	for _, c := range []hwmediacodec.Codec{hwmediacodec.H264, hwmediacodec.HEVC} {
		t.Run(c.String(), func(t *testing.T) {
			requireFakeVAAPI(t)
			ctx := context.Background()
			const w, h = 320, 240
			enc, err := hwmediacodec.NewEncoder(ctx, c, w, h, hwmediacodec.WithKeyframeInterval(100))
			if err != nil {
				t.Fatal(err)
			}
			defer enc.Close()
			var src [][]byte
			for k := 0; k < 6; k++ {
				src = append(src, fakeInputFrame(k, w, h))
			}
			first := encodeAll(t, enc, src, w, h)
			second := encodeAll(t, enc, src, w, h)
			if len(first) != 6 || len(second) != 6 || !second[0].Keyframe || second[1].Keyframe {
				t.Fatalf("after Flush: %d and %d packets, keyframes %v %v", len(first), len(second), second[0].Keyframe, second[1].Keyframe)
			}
			checkParameterSets(t, c, second)

			// Decode a stream and re-encode its frames as they come out.
			s := testutil.GenerateStreamBFrames(t, c, w, h, 20, 2)
			dec, err := hwmediacodec.NewDecoder(ctx, c)
			if err != nil {
				t.Fatal(err)
			}
			defer dec.Close()
			frames := decodeFrames(t, dec, c, testutil.ReadFile(t, s.Path), hwmediacodec.NV12)
			var raw [][]byte
			for _, f := range frames {
				raw = append(raw, f.pix)
			}
			out := encodeAll(t, enc, raw, w, h)
			if len(out) != 20 {
				t.Fatalf("transcoded %d packets, want 20", len(out))
			}
			log := fakeLog(t, "enc")
			if len(log) != 32 {
				t.Fatalf("the driver encoded %d pictures, want 32", len(log))
			}
			// The encoder saw the decoder's frames in display order: the
			// first luma sample is the picture order count.
			step := 1
			if c == hwmediacodec.H264 {
				step = 2
			}
			for i, l := range log[12:] {
				if got := logInt(t, l, "in00"); got != (step*i)&0xff {
					t.Errorf("transcoded picture %d starts with %d, want %d", i, got, (step*i)&0xff)
				}
			}
		})
	}
}
