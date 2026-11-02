//go:build linux && !android

package vaapi

import (
	"testing"

	"github.com/shibukawa/hwmediacodec/internal/av1"
	"github.com/shibukawa/hwmediacodec/internal/bitstream"
)

// TestAV1HeadersParse writes the headers of a key frame and two inter
// frames and reads them back with the decoder's parser, which follows the
// specification syntax: every field must come back and the bit positions
// handed to the driver must point at the fields they name.
func TestAV1HeadersParse(t *testing.T) {
	for _, tc := range []struct {
		w, h          int
		cdef, bt709   bool
		txMode        uint8
		obuSize       int
		tileCols, row int
	}{
		{352, 640, true, false, av1TxModeSelect, 2, 1, 1},
		{1920, 1080, false, true, av1TxModeLargest, 1, 1, 1},
		{64, 64, true, false, av1TxModeSelect, 4, 1, 1},
		{8192, 4352, true, false, av1TxModeSelect, 2, 2, 2},
	} {
		s := newAV1Stream(tc.w, tc.h, av1LevelIdx(tc.w, tc.h, 30, 0))
		s.enableCDEF, s.bt709, s.txMode, s.obuSizeBytes = tc.cdef, tc.bt709, tc.txMode, tc.obuSize
		if s.tileCols != tc.tileCols || s.tileRows != tc.row {
			t.Errorf("%dx%d: %dx%d tiles, want %dx%d", tc.w, tc.h, s.tileCols, s.tileRows, tc.tileCols, tc.row)
		}

		obus, err := av1.Split(s.sequenceHeaderOBU())
		if err != nil || len(obus) != 1 || obus[0].Type != av1.OBUSequenceHeader {
			t.Fatalf("%dx%d: sequence header OBU: %v %v", tc.w, tc.h, obus, err)
		}
		seq, err := av1.ParseSequenceHeader(obus[0].Payload)
		if err != nil {
			t.Fatalf("%dx%d: %v", tc.w, tc.h, err)
		}
		if seq.MaxFrameWidth != tc.w || seq.MaxFrameHeight != tc.h || seq.BitDepth != 8 || seq.Profile != 0 ||
			!seq.EnableOrderHint || seq.OrderHintBits != av1OrderHintBits || seq.EnableCDEF != tc.cdef ||
			seq.ColorDescriptionPresent != tc.bt709 || seq.SubsamplingX != 1 || seq.SubsamplingY != 1 {
			t.Errorf("%dx%d: sequence header %+v", tc.w, tc.h, seq)
		}
		if tc.bt709 && (seq.ColorPrimaries != 1 || seq.MatrixCoefficients != 1) {
			t.Errorf("%dx%d: colour %d/%d", tc.w, tc.h, seq.ColorPrimaries, seq.MatrixCoefficients)
		}

		st := &av1.State{Seq: seq}
		frames := []av1Frame{
			{key: true, orderHint: 0, refreshFlags: 0xff, qindex: 120},
			{orderHint: 1, refreshFlags: 1 << 1, refSlot: 0, qindex: 140},
			{orderHint: 2, refreshFlags: 1 << 0, refSlot: 1, qindex: 1},
		}
		for i, f := range frames {
			data, pos := s.frameHeaderOBU(&f)
			obus, err := av1.Split(data)
			if err != nil || len(obus) != 1 || obus[0].Type != av1.OBUFrameHeader {
				t.Fatalf("%dx%d frame %d: frame header OBU: %v %v", tc.w, tc.h, i, obus, err)
			}
			if got := len(obus[0].Raw) - len(obus[0].Payload) - 1; got != tc.obuSize {
				t.Errorf("%dx%d frame %d: size field of %d bytes, want %d", tc.w, tc.h, i, got, tc.obuSize)
			}
			h, err := st.ParseHeader(obus[0].Payload, 0, 0)
			if err != nil {
				t.Fatalf("%dx%d frame %d: %v", tc.w, tc.h, i, err)
			}
			wantType := av1.InterFrame
			if f.key {
				wantType = av1.KeyFrame
			}
			if h.FrameType != wantType || !h.ShowFrame || h.OrderHint != f.orderHint || h.BaseQIdx != f.qindex ||
				h.FrameWidth != tc.w || h.FrameHeight != tc.h || h.UpscaledWidth != tc.w ||
				h.TileCols != s.tileCols || h.TileRows != s.tileRows || h.TxMode != tc.txMode ||
				h.SegmentationEnabled || h.ReferenceSelect || h.AllowWarpedMotion {
				t.Errorf("%dx%d frame %d: header %+v", tc.w, tc.h, i, h)
			}
			if !f.key {
				if h.RefreshFrameFlags != f.refreshFlags || h.PrimaryRefFrame != 0 {
					t.Errorf("%dx%d frame %d: refresh %#x primary %d", tc.w, tc.h, i, h.RefreshFrameFlags, h.PrimaryRefFrame)
				}
				for j, idx := range h.RefFrameIdx {
					if idx != f.refSlot {
						t.Errorf("%dx%d frame %d: ref_frame_idx[%d] = %d, want %d", tc.w, tc.h, i, j, idx, f.refSlot)
					}
				}
			}
			cols, rows := s.tileSizes()
			for j, c := range cols {
				if h.WidthInSbsMinus1[j] != c {
					t.Errorf("%dx%d: tile column %d: %d sbs, header has %d", tc.w, tc.h, j, c+1, h.WidthInSbsMinus1[j]+1)
				}
			}
			for j, r := range rows {
				if h.HeightInSbsMinus1[j] != r {
					t.Errorf("%dx%d: tile row %d: %d sbs, header has %d", tc.w, tc.h, j, r+1, h.HeightInSbsMinus1[j]+1)
				}
			}
			// The header must end, before the trailing bits, where the
			// payload's bits run out.
			if trailing := 8*len(obus[0].Payload) - h.HeaderBits; trailing < 1 || trailing > 8 {
				t.Errorf("%dx%d frame %d: %d bits after the header", tc.w, tc.h, i, trailing)
			}
			// base_q_idx sits at the qindex position.
			r := bitstream.New(data)
			r.SkipBits(pos.qindex)
			if q, _ := r.ReadBits(8); uint8(q) != f.qindex {
				t.Errorf("%dx%d frame %d: %d at the qindex offset, want %d", tc.w, tc.h, i, q, f.qindex)
			}
			if pos.total != 8*len(data) {
				t.Errorf("%dx%d frame %d: total %d bits, OBU has %d", tc.w, tc.h, i, pos.total, 8*len(data))
			}
			wantCDEF := 0
			if tc.cdef {
				wantCDEF = 16
			}
			if pos.cdefSize != wantCDEF || pos.loopFilter-pos.segmentation != 1+b2i(f.qindex > 0) || pos.cdef-pos.loopFilter != 16 {
				t.Errorf("%dx%d frame %d: offsets %+v", tc.w, tc.h, i, pos)
			}
			st.Update(h)
		}
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestAV1LevelIdx(t *testing.T) {
	for _, tc := range []struct {
		w, h    int
		fps     float64
		bitrate int64
		want    uint8
	}{
		{352, 640, 30, 4_000_000, 4},  // 3.0
		{1280, 720, 30, 0, 5},         // 3.1
		{1920, 1080, 30, 0, 8},        // 4.0
		{1920, 1080, 60, 0, 9},        // 4.1
		{3840, 2160, 60, 0, 13},       // 5.1
		{1920, 1080, 30, 50e6, 14},    // bitrate pushes it to 5.2
		{16384, 16384, 30, 0, 31},     // larger than any level
		{426, 240, 0, 0, 0},           // 2.0
		{1920, 1080, 0, 1_000_000, 8}, // unknown frame rate
	} {
		if got := av1LevelIdx(tc.w, tc.h, tc.fps, tc.bitrate); got != tc.want {
			t.Errorf("%dx%d@%g %d bit/s: level %d, want %d", tc.w, tc.h, tc.fps, tc.bitrate, got, tc.want)
		}
	}
}
