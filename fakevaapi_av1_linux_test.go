package hwmediacodec_test

// AV1 tests of the vaapi backend against the fake VA driver; see
// fakevaapi_linux_test.go. Besides checking the buffers through the libva
// headers, the driver logs the picture and tile parameters of every frame,
// and the same streams are decoded once more by ffmpeg's VA-API hwaccel
// against the same driver: the two clients must describe every frame to the
// driver in the same way.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/internal/av1"
	"github.com/shibukawa/hwmediacodec/internal/bitstream"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
	"github.com/shibukawa/hwmediacodec/internal/vaapi/sys"
	"github.com/shibukawa/hwmediacodec/mediacontainer/ivf"
)

// av1Shown is the frame a temporal unit shows: its number in decode order
// and its size.
type av1Shown struct {
	number        int
	grain         bool
	width, height int
}

// av1Decoded is one decoded frame: the numbers of the frames in the eight
// reference slots when it was decoded.
type av1Decoded struct {
	refs     [av1.NumRefFrames]int
	keyShown bool
	grain    bool
	tiles    int
	groups   int
}

// simulateAV1 walks a stream the way a decoder does and returns what every
// temporal unit shows and what every decoded frame references.
func simulateAV1(t *testing.T, units [][]byte) (shown []av1Shown, decoded []av1Decoded) {
	t.Helper()
	state := &av1.State{}
	var slotNumber [av1.NumRefFrames]int
	var slotGrain [av1.NumRefFrames]bool
	for ti, data := range units {
		obus, err := av1.Split(data)
		if err != nil {
			t.Fatal(err)
		}
		var cur *av1.Header
		var pic av1Decoded
		for _, o := range obus {
			var group []byte
			switch o.Type {
			case av1.OBUSequenceHeader:
				seq, err := av1.ParseSequenceHeader(o.Payload)
				if err != nil {
					t.Fatal(err)
				}
				state.Seq = seq
				continue
			case av1.OBUFrameHeader, av1.OBUFrame:
				if cur != nil {
					continue
				}
				h, err := state.ParseHeader(o.Payload, o.TemporalID, o.SpatialID)
				if err != nil {
					t.Fatalf("temporal unit %d: %v", ti, err)
				}
				if h.ShowExistingFrame {
					i := h.FrameToShowMapIdx
					shown = append(shown, av1Shown{slotNumber[i], slotGrain[i], h.UpscaledWidth, h.FrameHeight})
					state.Update(h)
					if h.RefreshFrameFlags != 0 {
						for j := range slotNumber {
							slotNumber[j], slotGrain[j] = slotNumber[i], slotGrain[i]
						}
					}
					continue
				}
				cur = h
				pic = av1Decoded{refs: slotNumber, keyShown: h.FrameType == av1.KeyFrame && h.ShowFrame, grain: h.FilmGrain.ApplyGrain}
				if o.Type != av1.OBUFrame {
					continue
				}
				group = o.Payload[(h.HeaderBits+7)/8:]
			case av1.OBUTileGroup:
				group = o.Payload
			default:
				continue
			}
			tg, err := cur.ParseTileGroup(group)
			if err != nil {
				t.Fatalf("temporal unit %d: %v", ti, err)
			}
			pic.tiles += len(tg.Tiles)
			pic.groups++
			if pic.tiles < cur.NumTiles() {
				continue
			}
			n := len(decoded)
			decoded = append(decoded, pic)
			state.Update(cur)
			for j := range slotNumber {
				if cur.RefreshFrameFlags>>uint(j)&1 != 0 {
					slotNumber[j], slotGrain[j] = n, pic.grain
				}
			}
			if cur.ShowFrame {
				shown = append(shown, av1Shown{n, pic.grain, cur.UpscaledWidth, cur.FrameHeight})
			}
			cur = nil
		}
	}
	return shown, decoded
}

// av1ParamField names the field of VADecPictureParameterBufferAV1 that a
// byte offset falls into.
func av1ParamField(offset int) string {
	typ := reflect.TypeOf(sys.DecPictureParameterBufferAV1{})
	name := "?"
	for i := 0; i < typ.NumField(); i++ {
		if f := typ.Field(i); int(f.Offset) <= offset {
			name = fmt.Sprintf("%s+%d", f.Name, offset-int(f.Offset))
		}
	}
	return name
}

// ffmpegFakeLog decodes a stream with ffmpeg's VA-API hwaccel against the
// fake driver and returns the driver's log.
func ffmpegFakeLog(t *testing.T, path string) []map[string]string {
	t.Helper()
	log := filepath.Join(t.TempDir(), "ffmpeg-va.log")
	cmd := exec.Command(testutil.RequireFFmpeg(t), "-hide_banner", "-loglevel", "error",
		"-hwaccel", "vaapi", "-hwaccel_device", os.Getenv("HWMEDIACODEC_VAAPI_DEVICE"),
		"-c:v", "av1", "-i", path, "-f", "null", "-")
	cmd.Env = append(os.Environ(), "FAKE_VA_LOG="+log)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg -hwaccel vaapi failed: %v\n%s", err, out)
	}
	old := os.Getenv("FAKE_VA_LOG")
	t.Setenv("FAKE_VA_LOG", log)
	defer os.Setenv("FAKE_VA_LOG", old)
	return fakeLog(t, "dec")
}

// compareWithFFmpeg decodes the stream at path with ffmpeg against the fake
// driver and requires the picture parameters, tile parameters and tile data
// of every picture to equal those in log, the driver's log of the backend
// decoding the same stream.
func compareWithFFmpeg(t *testing.T, path string, log []map[string]string) {
	t.Helper()
	ref := ffmpegFakeLog(t, path)
	if len(ref) != len(log) {
		t.Fatalf("ffmpeg submitted %d pictures, the backend %d", len(ref), len(log))
	}
	for i := range log {
		got, want := log[i], ref[i]
		if got["pp"] != want["pp"] {
			var diff []string
			for off := 0; off+2 <= len(got["pp"]) && off+2 <= len(want["pp"]); off += 2 {
				if got["pp"][off:off+2] != want["pp"][off:off+2] {
					diff = append(diff, fmt.Sprintf("%s: %s, ffmpeg %s", av1ParamField(off/2), got["pp"][off:off+2], want["pp"][off:off+2]))
				}
			}
			if len(diff) > 24 {
				diff = append(diff[:24], "...")
			}
			t.Fatalf("picture %d (type %s): picture parameters differ from ffmpeg's:\n%s", i, got["type"], strings.Join(diff, "\n"))
		}
		if got["tiles_hex"] != want["tiles_hex"] || got["data"] != want["data"] {
			t.Fatalf("picture %d: tile parameters or data differ from ffmpeg's:\n%s (data %s)\n%s (data %s)", i, got["tiles_hex"], got["data"], want["tiles_hex"], want["data"])
		}
	}
}

// TestFakeVAAPIDecodeAV1 decodes AV1 streams that use hidden frames,
// show_existing_frame, tiles and tile groups, film grain, super-resolution,
// reference scaling and S-frames. The driver checks every picture; the
// frames that come back prove that each temporal unit shows the right
// picture (the grained one where film grain applies) at the right size; the
// reference slots the driver was given are compared with a simulation of
// the stream; and every parameter buffer is compared with the one ffmpeg
// submits for the same frame.
func TestFakeVAAPIDecodeAV1(t *testing.T) {
	requireFakeVAAPI(t)
	type gen func(t *testing.T) string
	ffmpegEnc := func(encoder string, w, h, frames int, args ...string) gen {
		return func(t *testing.T) string { return testutil.GenerateAV1With(t, encoder, w, h, frames, args...) }
	}
	aomenc := func(w, h, frames int, args ...string) gen {
		return func(t *testing.T) string { return testutil.GenerateAV1Aomenc(t, w, h, frames, args...) }
	}
	for _, s := range []struct {
		name string
		gen  gen
		// features the stream must exercise
		hidden, existing, grain, tiles, groups, sizes bool
	}{
		{name: "svt", gen: ffmpegEnc("libsvtav1", 320, 240, 40, "-preset", "10", "-g", "25"), hidden: true, existing: true},
		{name: "svt-film-grain", gen: ffmpegEnc("libsvtav1", 320, 240, 30, "-preset", "10", "-g", "25", "-svtav1-params", "film-grain=8"), hidden: true, existing: true, grain: true},
		{name: "aom", gen: ffmpegEnc("libaom-av1", 320, 240, 40, "-cpu-used", "6", "-g", "20", "-lag-in-frames", "16", "-b:v", "300k")},
		{name: "aom-global-motion", gen: ffmpegEnc("libaom-av1", 320, 240, 24, "-vf", "rotate=a=0.03*n", "-cpu-used", "2", "-g", "24", "-lag-in-frames", "12", "-b:v", "400k")},
		{name: "aom-tiles", gen: ffmpegEnc("libaom-av1", 640, 480, 20, "-cpu-used", "8", "-g", "10", "-tiles", "3x2", "-b:v", "500k"), tiles: true},
		{name: "aom-film-grain", gen: ffmpegEnc("libaom-av1", 320, 240, 30, "-cpu-used", "8", "-g", "15", "-lag-in-frames", "8", "-aom-params", "film-grain-test=3", "-b:v", "300k"), grain: true},
		{name: "aom-film-grain-denoise", gen: ffmpegEnc("libaom-av1", 320, 240, 30, "-cpu-used", "8", "-g", "15", "-denoise-noise-level", "30", "-b:v", "300k"), grain: true},
		{name: "aom-error-resilient", gen: ffmpegEnc("libaom-av1", 320, 240, 30, "-cpu-used", "8", "-g", "15", "-lag-in-frames", "8", "-error-resilience", "default", "-b:v", "300k")},
		{name: "aom-screen", gen: ffmpegEnc("libaom-av1", 320, 240, 20, "-cpu-used", "6", "-g", "10", "-aom-params", "tune-content=screen", "-b:v", "300k")},
		{name: "aom-lossless", gen: ffmpegEnc("libaom-av1", 160, 120, 10, "-cpu-used", "8", "-aom-params", "lossless=1")},
		{name: "aom-segmentation", gen: ffmpegEnc("libaom-av1", 320, 240, 30, "-cpu-used", "8", "-g", "15", "-aq-mode", "1", "-b:v", "200k")},
		{name: "aom-deltaq", gen: ffmpegEnc("libaom-av1", 320, 240, 20, "-cpu-used", "6", "-g", "10", "-aom-params", "deltaq-mode=1:delta-lf-mode=1:enable-tpl-model=1", "-b:v", "200k")},
		{name: "aom-realtime", gen: ffmpegEnc("libaom-av1", 320, 240, 40, "-usage", "realtime", "-cpu-used", "8", "-g", "20", "-aq-mode", "3", "-lag-in-frames", "0", "-b:v", "200k")},
		{name: "aom-qmatrix", gen: ffmpegEnc("libaom-av1", 320, 240, 20, "-cpu-used", "8", "-g", "10", "-aom-params", "enable-qm=1", "-b:v", "300k")},
		{name: "aom-128sb-restoration", gen: ffmpegEnc("libaom-av1", 640, 480, 12, "-cpu-used", "3", "-g", "12", "-aom-params", "sb-size=128:enable-restoration=1", "-b:v", "600k")},
		{name: "rav1e", gen: ffmpegEnc("librav1e", 320, 240, 20, "-speed", "10", "-g", "10", "-b:v", "300k")},
		{name: "aomenc-superres", gen: aomenc(320, 240, 24, "--cpu-used=6", "--kf-max-dist=12", "--superres-mode=1", "--superres-denominator=12", "--superres-kf-denominator=11", "--target-bitrate=300")},
		{name: "aomenc-resize-random", gen: aomenc(320, 240, 40, "--cpu-used=6", "--kf-max-dist=40", "--resize-mode=2", "--target-bitrate=300"), sizes: true},
		{name: "aomenc-resize-superres-random", gen: aomenc(320, 240, 40, "--cpu-used=6", "--kf-max-dist=40", "--lag-in-frames=0", "--resize-mode=2", "--superres-mode=2", "--target-bitrate=300"), sizes: true},
		{name: "aomenc-sframes", gen: aomenc(320, 240, 40, "--cpu-used=6", "--kf-max-dist=40", "--lag-in-frames=0", "--sframe-dist=8", "--sframe-mode=1", "--error-resilient=1", "--target-bitrate=300")},
		{name: "aomenc-tile-groups", gen: aomenc(640, 480, 12, "--cpu-used=6", "--tile-columns=2", "--tile-rows=1", "--num-tile-groups=3", "--target-bitrate=500"), tiles: true, groups: true},
		{name: "aomenc-forward-keyframes", gen: aomenc(320, 240, 48, "--cpu-used=6", "--lag-in-frames=19", "--enable-fwd-kf=1", "--fwd-kf-dist=16", "--kf-max-dist=16", "--kf-min-dist=16", "--target-bitrate=300"), hidden: true, existing: true},
	} {
		t.Run(s.name, func(t *testing.T) {
			requireFakeVAAPI(t)
			path := s.gen(t)
			units := splitStream(t, hwmediacodec.AV1, testutil.ReadFile(t, path))
			shown, decoded := simulateAV1(t, units)
			if len(shown) != len(units) {
				t.Fatalf("the stream shows %d frames in %d temporal units", len(shown), len(units))
			}
			hidden, existing := av1Structure(t, units)
			grain, tiles, groups, sizes := false, false, false, false
			for _, p := range decoded {
				grain = grain || p.grain
				tiles = tiles || p.tiles > 1
				groups = groups || p.groups > 1
			}
			for _, f := range shown {
				sizes = sizes || f.width != shown[0].width || f.height != shown[0].height
			}
			for name, v := range map[string][2]bool{"hidden frames": {s.hidden, hidden > 0}, "show_existing_frame": {s.existing, existing > 0},
				"film grain": {s.grain, grain}, "several tiles": {s.tiles, tiles}, "several tile groups": {s.groups, groups}, "several frame sizes": {s.sizes, sizes}} {
				if v[0] && !v[1] {
					t.Fatalf("the generated stream has no %s; the test needs it", name)
				}
			}

			dec, err := hwmediacodec.NewDecoder(context.Background(), hwmediacodec.AV1)
			if err != nil {
				t.Fatalf("NewDecoder: %v", err)
			}
			frames := decodePackets(t, dec, units, hwmediacodec.NV12)
			if err := dec.Close(); err != nil {
				t.Fatal(err)
			}
			if len(frames) != len(units) {
				t.Fatalf("decoded %d frames from %d temporal units", len(frames), len(units))
			}
			for i, f := range frames {
				want := shown[i].number
				if shown[i].grain {
					want += 128
				}
				if got := checkFakeFrame(t, i, f, shown[i].width, shown[i].height); got != want&0xff {
					t.Fatalf("temporal unit %d shows picture %d, want %d (film grain %v)", i, got, want&0xff, shown[i].grain)
				}
				if f.pts != int64(i)*3000 {
					t.Fatalf("frame %d has PTS %d, want %d", i, f.pts, int64(i)*3000)
				}
			}

			log := fakeLog(t, "dec")
			if len(log) != len(decoded) {
				t.Fatalf("the driver decoded %d pictures, the stream has %d", len(log), len(decoded))
			}
			for i, l := range log {
				p := decoded[i]
				want := make([]string, len(p.refs))
				for j, n := range p.refs {
					want[j] = fmt.Sprint(n)
					if p.keyShown {
						want[j] = "-1"
					}
				}
				if l["codec"] != "av1" || logInt(t, l, "n") != i || l["refs"] != strings.Join(want, ",") ||
					logInt(t, l, "tiles") != p.tiles || logInt(t, l, "groups") != p.groups || (logInt(t, l, "grain") != 0) != p.grain {
					t.Fatalf("picture %d: the driver saw codec=%s n=%s refs=%s tiles=%s groups=%s grain=%s, want refs %s, %d tiles in %d groups, grain %v",
						i, l["codec"], l["n"], l["refs"], l["tiles"], l["groups"], l["grain"], strings.Join(want, ","), p.tiles, p.groups, p.grain)
				}
			}

			compareWithFFmpeg(t, path, log)
			t.Logf("%d temporal units, %d pictures (%d hidden, %d shown again), all parameter buffers equal to ffmpeg's", len(units), len(decoded), hidden, existing)
		})
	}
}

// TestFakeVAAPIDecodeAV1Unsupported checks that streams outside the 8-bit
// 4:2:0 Main profile are refused with ErrUnsupported before anything is
// submitted to the driver.
func TestFakeVAAPIDecodeAV1Unsupported(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"10-bit", []string{"-cpu-used", "8", "-pix_fmt", "yuv420p10le", "-b:v", "200k"}},
		{"4:4:4", []string{"-cpu-used", "8", "-pix_fmt", "yuv444p", "-b:v", "200k"}},
		{"monochrome", []string{"-cpu-used", "8", "-pix_fmt", "gray", "-b:v", "200k"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requireFakeVAAPI(t)
			path := testutil.GenerateAV1With(t, "libaom-av1", 320, 240, 4, tc.args...)
			units := splitStream(t, hwmediacodec.AV1, testutil.ReadFile(t, path))
			ctx := context.Background()
			dec, err := hwmediacodec.NewDecoder(ctx, hwmediacodec.AV1)
			if err != nil {
				t.Fatal(err)
			}
			defer dec.Close()
			err = dec.Send(ctx, hwmediacodec.Packet{Data: units[0]})
			var ue *hwmediacodec.UnsupportedError
			if !errors.As(err, &ue) || ue.Backend != "vaapi" || ue.Codec != hwmediacodec.AV1 {
				t.Fatalf("Send = %v, want an UnsupportedError from vaapi", err)
			}
			t.Log(err)
			if n := len(fakeLog(t, "dec")); n != 0 {
				t.Fatalf("the driver decoded %d pictures", n)
			}
		})
	}
}

// headerBit returns bit i of an OBU payload.
func headerBit(payload []byte, i int) uint64 {
	return uint64(payload[i>>3] >> (7 - uint(i&7)) & 1)
}

// shortSignaling rewrites the inter frames of a stream to signal their
// references with frame_refs_short_signaling: only the LAST and GOLDEN
// slots stay in the header and the decoder derives the other five (Section
// 7.8 of the specification). No encoder at hand writes such headers. The
// derived references are generally not the ones the encoder predicted
// from, so the result decodes to garbage, but it is a valid stream whose
// headers the fake driver's two clients must read alike. It returns the
// rewritten units and the number of frames whose references changed.
func shortSignaling(t *testing.T, units [][]byte) (out [][]byte, changed int) {
	t.Helper()
	state := &av1.State{}
	for ti, data := range units {
		obus, err := av1.Split(data)
		if err != nil {
			t.Fatal(err)
		}
		var unit []byte
		for _, o := range obus {
			if o.Type == av1.OBUSequenceHeader {
				if state.Seq, err = av1.ParseSequenceHeader(o.Payload); err != nil {
					t.Fatal(err)
				}
			}
			if o.Type == av1.OBUFrameHeader {
				// SVT-AV1 writes show_existing_frame headers this way.
				h, err := state.ParseHeader(o.Payload, 0, 0)
				if err != nil || !h.ShowExistingFrame {
					t.Fatalf("temporal unit %d: a frame split into several OBUs (%v)", ti, err)
				}
				state.Update(h)
			}
			if o.Type != av1.OBUFrame {
				if o.Type == av1.OBUTileGroup {
					t.Fatalf("temporal unit %d: the stream splits frames into several OBUs", ti)
				}
				unit = append(unit, o.Raw...)
				continue
			}
			scratch := *state
			h, err := scratch.ParseHeader(o.Payload, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			payload := o.Payload
			if !h.ShowExistingFrame && !h.IsIntra() && state.Seq.EnableOrderHint && !state.Seq.FrameIDNumbersPresent {
				// The flag is the 0 bit in front of the seven 3-bit
				// reference indices. A candidate position is taken when
				// the rewritten header parses to exactly its new length,
				// 15 bits less. A frame for which no position does keeps
				// its header: with other references the header can need
				// another length, since skip_mode_present is only coded
				// when the references allow skip mode.
				for p := 8; p+22 <= h.HeaderBits; p++ {
					match := headerBit(o.Payload, p) == 0
					for i := 0; i < 21 && match; i++ {
						match = headerBit(o.Payload, p+1+i) == uint64(h.RefFrameIdx[i/3]>>(2-uint(i%3))&1)
					}
					if !match {
						continue
					}
					w := bitstream.NewWriter()
					for i := 0; i < p; i++ {
						w.WriteBits(headerBit(o.Payload, i), 1)
					}
					w.WriteBits(1, 1)
					w.WriteBits(uint64(h.RefFrameIdx[av1.RefLast-av1.RefLast]), 3)
					w.WriteBits(uint64(h.RefFrameIdx[av1.RefGolden-av1.RefLast]), 3)
					for i := p + 22; i < h.HeaderBits; i++ {
						w.WriteBits(headerBit(o.Payload, i), 1)
					}
					for !w.ByteAligned() {
						w.WriteBits(0, 1)
					}
					candidate := append(append([]byte{}, w.Bytes()...), o.Payload[(h.HeaderBits+7)/8:]...)
					scratch = *state
					h2, err := scratch.ParseHeader(candidate, 0, 0)
					if err == nil && h2.FrameRefsShort && h2.HeaderBits == h.HeaderBits-15 && h2.NumTiles() == h.NumTiles() {
						payload = candidate
						if h2.RefFrameIdx != h.RefFrameIdx {
							changed++
						}
						break
					}
				}
			}
			h, err = state.ParseHeader(payload, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			state.Update(h)
			unit = av1.AppendOBU(unit, av1.OBUFrame, payload)
		}
		out = append(out, unit)
	}
	return out, changed
}

// TestFakeVAAPIDecodeAV1ShortSignaling decodes streams whose inter frames
// signal two of their seven references and leave the rest to the decoder,
// and compares the references the backend derives with ffmpeg's.
func TestFakeVAAPIDecodeAV1ShortSignaling(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"random-access", []string{"-preset", "10", "-g", "25"}},
		{"low-delay", []string{"-preset", "10", "-g", "50", "-svtav1-params", "pred-struct=1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requireFakeVAAPI(t)
			src := testutil.GenerateAV1With(t, "libsvtav1", 320, 240, 50, tc.args...)
			units, changed := shortSignaling(t, splitStream(t, hwmediacodec.AV1, testutil.ReadFile(t, src)))
			if changed < 5 {
				t.Fatalf("only %d frames have derived references that differ from the signalled ones", changed)
			}
			path := filepath.Join(t.TempDir(), "short.ivf")
			f, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			w := ivf.NewWriter(f, "AV01", 320, 240, 1, 30)
			for i, u := range units {
				if err := w.WriteFrame(u, uint64(i)); err != nil {
					t.Fatal(err)
				}
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}

			dec, err := hwmediacodec.NewDecoder(context.Background(), hwmediacodec.AV1)
			if err != nil {
				t.Fatal(err)
			}
			frames := decodePackets(t, dec, units, hwmediacodec.NV12)
			if err := dec.Close(); err != nil {
				t.Fatal(err)
			}
			if len(frames) != len(units) {
				t.Fatalf("decoded %d frames from %d temporal units", len(frames), len(units))
			}
			compareWithFFmpeg(t, path, fakeLog(t, "dec"))
			t.Logf("%d temporal units; %d frames with derived references that differ from the encoder's; all parameter buffers equal to ffmpeg's", len(units), changed)
		})
	}
}

// TestFakeVAAPIDecodeAV1SequenceChange decodes two AV1 streams of different
// size back to back and a seek: the surfaces are rebuilt at the new
// sequence and decoding resumes at the key frame after a flush.
func TestFakeVAAPIDecodeAV1SequenceChange(t *testing.T) {
	requireFakeVAAPI(t)
	u1 := splitStream(t, hwmediacodec.AV1, testutil.ReadFile(t, testutil.GenerateAV1(t, 320, 240, 30)))
	u2 := splitStream(t, hwmediacodec.AV1, testutil.ReadFile(t, testutil.GenerateAV1(t, 160, 120, 30)))
	dec, err := hwmediacodec.NewDecoder(context.Background(), hwmediacodec.AV1)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	frames := decodePackets(t, dec, append(append([][]byte{}, u1...), u2...), hwmediacodec.NV12)
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

	// The decoder was flushed by decodePackets. Units that are not key
	// frames are skipped, then decoding resumes at the second key frame of
	// the first stream (frame 25).
	ctx := context.Background()
	for i := 5; i < 8; i++ {
		if err := dec.Send(ctx, hwmediacodec.Packet{Data: u1[i]}); err != nil {
			t.Fatalf("Send non-keyframe after flush: %v", err)
		}
		if _, err := dec.Receive(ctx); !errors.Is(err, hwmediacodec.ErrAgain) {
			t.Fatalf("Receive after a skipped unit: %v", err)
		}
	}
	again := decodePackets(t, dec, u1[25:], hwmediacodec.NV12)
	if len(again) != 5 {
		t.Fatalf("decoded %d frames after the seek, want 5", len(again))
	}
	for i, f := range again {
		checkFakeFrame(t, i, f, 320, 240)
	}
}
