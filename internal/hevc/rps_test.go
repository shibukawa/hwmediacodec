package hevc

import (
	"reflect"
	"testing"

	"github.com/shibukawa/hwmediacodec/internal/bitstream"
)

// rpsList returns the S0 and S1 deltas of a set with their "used" flags.
func rpsList(s *ShortTermRPS) (deltas []int32, used []bool) {
	for i := 0; i < s.NumNegativePics; i++ {
		deltas = append(deltas, s.DeltaPocS0[i])
		used = append(used, s.UsedByCurrPicS0[i])
	}
	for i := 0; i < s.NumPositivePics; i++ {
		deltas = append(deltas, s.DeltaPocS1[i])
		used = append(used, s.UsedByCurrPicS1[i])
	}
	return deltas, used
}

// TestInterRPSPrediction decodes the reference picture sets of the HEVC
// reference encoder's random access configuration (GOP 8), where every set
// but the first is predicted from the previous one. The expected sets are
// the "reference pictures" column of that configuration; the idc values
// are its "reference idcs" (0 = not in the set, 1 = used by the current
// picture, 2 = kept for later pictures).
func TestInterRPSPrediction(t *testing.T) {
	type predicted struct {
		deltaRPS int32
		idcs     []int
		want     []int32
		used     []bool
	}
	all := func(n int) []bool {
		u := make([]bool, n)
		for i := range u {
			u[i] = true
		}
		return u
	}
	sets := []predicted{
		{4, []int{1, 1, 0, 0, 1}, []int32{-4, -6, 4}, all(3)},
		{2, []int{1, 1, 1, 1}, []int32{-2, -4, 2, 6}, all(4)},
		{1, []int{1, 0, 1, 1, 1}, []int32{-1, 1, 3, 7}, all(4)},
		{-2, []int{1, 1, 1, 1, 0}, []int32{-1, -3, 1, 5}, all(4)},
		{-3, []int{1, 1, 1, 1, 0}, []int32{-2, -4, -6, 2}, all(4)},
		{1, []int{1, 0, 1, 1, 1}, []int32{-1, -5, 1, 3}, all(4)},
		{-2, []int{1, 1, 1, 1, 0}, []int32{-1, -3, -7, 1}, all(4)},
		// Not part of the reference configuration: a picture kept without
		// being used (idc 2) and a delta that lands on the current picture.
		{-1, []int{2, 1, 0, 0, 1}, []int32{-1, -2, -4}, []bool{true, false, true}},
	}

	w := bitstream.NewWriter()
	// Set 0, coded explicitly: -8 -10 -12 -16.
	w.WriteUE(4)
	w.WriteUE(0)
	for _, d := range []uint32{7, 1, 1, 3} {
		w.WriteUE(d)
		w.WriteFlag(true)
	}
	writePredicted := func(p predicted) {
		w.WriteFlag(true) // inter_ref_pic_set_prediction_flag
		w.WriteFlag(p.deltaRPS < 0)
		abs := p.deltaRPS
		if abs < 0 {
			abs = -abs
		}
		w.WriteUE(uint32(abs - 1))
		for _, idc := range p.idcs {
			w.WriteFlag(idc == 1)
			if idc != 1 {
				w.WriteFlag(idc == 2)
			}
		}
	}
	for _, p := range sets {
		writePredicted(p)
	}
	// A set coded in a slice header, predicted from the set three back
	// (delta_idx_minus1 = 2): the same prediction as sets[6] from sets[5].
	inSlice := sets[6]
	w.WriteFlag(true)
	w.WriteUE(2)
	w.WriteFlag(inSlice.deltaRPS < 0)
	w.WriteUE(uint32(-inSlice.deltaRPS - 1))
	for _, idc := range inSlice.idcs {
		w.WriteFlag(idc == 1)
		if idc != 1 {
			w.WriteFlag(idc == 2)
		}
	}
	w.Trailing()

	r := &reader{r: bitstream.New(w.Bytes())}
	var parsed []ShortTermRPS
	for i := 0; i <= len(sets); i++ {
		s := r.shortTermRPS(i, parsed, false)
		if r.err != nil {
			t.Fatalf("set %d: %v", i, r.err)
		}
		parsed = append(parsed, s)
	}
	if got, _ := rpsList(&parsed[0]); !reflect.DeepEqual(got, []int32{-8, -10, -12, -16}) {
		t.Fatalf("set 0 = %v", got)
	}
	for i, p := range sets {
		got, used := rpsList(&parsed[i+1])
		if !reflect.DeepEqual(got, p.want) || !reflect.DeepEqual(used, p.used) {
			t.Errorf("set %d: deltas %v used %v, want %v %v", i+1, got, used, p.want, p.used)
		}
		if !parsed[i+1].InterRPSPrediction {
			t.Errorf("set %d: not marked as predicted", i+1)
		}
	}
	// In the slice header the reference is parsed[len-3] = sets[5]'s set,
	// which is what sets[6] was predicted from.
	s := r.shortTermRPS(len(parsed), parsed, true)
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got, _ := rpsList(&s); !reflect.DeepEqual(got, inSlice.want) || s.DeltaIdxMinus1 != 2 {
		t.Errorf("slice set: deltas %v (delta_idx_minus1 %d), want %v", got, s.DeltaIdxMinus1, inSlice.want)
	}
	if n := parsed[len(parsed)-1].NumUsedByCurr(); n != 2 {
		t.Errorf("NumUsedByCurr = %d, want 2", n)
	}

	// Writing the derived sets explicitly and parsing them back gives the
	// same lists.
	w2 := bitstream.NewWriter()
	for i := range parsed {
		parsed[i].write(w2, i)
	}
	w2.Trailing()
	r2 := &reader{r: bitstream.New(w2.Bytes())}
	var again []ShortTermRPS
	for i := range parsed {
		s := r2.shortTermRPS(i, again, false)
		if r2.err != nil {
			t.Fatalf("rewritten set %d: %v", i, r2.err)
		}
		again = append(again, s)
		d1, u1 := rpsList(&parsed[i])
		d2, u2 := rpsList(&s)
		if !reflect.DeepEqual(d1, d2) || !reflect.DeepEqual(u1, u2) {
			t.Errorf("rewritten set %d: %v %v, want %v %v", i, d2, u2, d1, u1)
		}
	}
}

func TestRPSRejectsOversizedSets(t *testing.T) {
	w := bitstream.NewWriter()
	w.WriteUE(12)
	w.WriteUE(12)
	w.Trailing()
	r := &reader{r: bitstream.New(w.Bytes())}
	r.shortTermRPS(0, nil, false)
	if r.err == nil {
		t.Error("a set of 24 pictures was accepted")
	}
}

// Default 8x8 lists as printed in most references: by position (row by
// row). Table 7-6 lists them in coded (up-right diagonal) order.
var (
	rasterIntra = [64]uint8{
		16, 16, 16, 16, 17, 18, 21, 24,
		16, 16, 16, 16, 17, 19, 22, 25,
		16, 16, 17, 18, 20, 22, 25, 29,
		16, 16, 18, 21, 24, 27, 31, 36,
		17, 17, 20, 24, 30, 35, 41, 47,
		18, 19, 22, 27, 35, 44, 54, 65,
		21, 22, 25, 31, 41, 54, 70, 88,
		24, 25, 29, 36, 47, 65, 88, 115,
	}
	rasterInter = [64]uint8{
		16, 16, 16, 16, 17, 18, 20, 24,
		16, 16, 16, 17, 18, 20, 24, 25,
		16, 16, 17, 18, 20, 24, 25, 28,
		16, 17, 18, 20, 24, 25, 28, 33,
		17, 18, 20, 24, 25, 28, 33, 41,
		18, 20, 24, 25, 28, 33, 41, 54,
		20, 24, 25, 28, 33, 41, 54, 71,
		24, 25, 28, 33, 41, 54, 71, 91,
	}
)

// TestDefaultScalingLists checks the coded-order default lists against the
// by-position form through the up-right diagonal scan (6.5.3).
func TestDefaultScalingLists(t *testing.T) {
	var scan [][2]int // x, y
	for x, y := 0, 0; len(scan) < 64; {
		for y >= 0 {
			if x < 8 && y < 8 {
				scan = append(scan, [2]int{x, y})
			}
			y--
			x++
		}
		y, x = x, 0
	}
	for i, p := range scan {
		if got, want := defaultScalingIntra[i], rasterIntra[p[1]*8+p[0]]; got != want {
			t.Errorf("intra coefficient %d: %d, want %d", i, got, want)
		}
		if got, want := defaultScalingInter[i], rasterInter[p[1]*8+p[0]]; got != want {
			t.Errorf("inter coefficient %d: %d, want %d", i, got, want)
		}
	}
	sl := DefaultScalingList()
	for m := 0; m < 6; m++ {
		for _, v := range sl.L4[m] {
			if v != 16 {
				t.Fatalf("4x4 list %d is not flat", m)
			}
		}
		want := defaultScalingIntra
		if m >= 3 {
			want = defaultScalingInter
		}
		if sl.L8[m] != want || sl.L16[m] != want || sl.DC16[m] != 16 {
			t.Errorf("8x8/16x16 list %d differs from the default", m)
		}
	}
	if sl.L32[0] != defaultScalingIntra || sl.L32[1] != defaultScalingInter || sl.DC32 != [2]uint8{16, 16} {
		t.Error("32x32 lists differ from the default")
	}
}

func TestTileSizes(t *testing.T) {
	sps := &SPS{Width: 1920, Height: 1080, Log2MinLumaCodingBlockSizeMinus3: 0, Log2DiffMaxMinLumaCodingBlockSize: 3}
	if sps.PicWidthInCtbs() != 30 || sps.PicHeightInCtbs() != 17 {
		t.Fatalf("picture is %dx%d CTBs", sps.PicWidthInCtbs(), sps.PicHeightInCtbs())
	}
	uniform := &PPS{TilesEnabled: true, NumTileColumns: 4, NumTileRows: 3, UniformSpacing: true}
	if got := uniform.TileColumnWidths(sps); !reflect.DeepEqual(got, []int{7, 8, 7, 8}) {
		t.Errorf("uniform columns %v", got)
	}
	if got := uniform.TileRowHeights(sps); !reflect.DeepEqual(got, []int{5, 6, 6}) {
		t.Errorf("uniform rows %v", got)
	}
	explicit := &PPS{TilesEnabled: true, NumTileColumns: 3, NumTileRows: 2, ColumnWidthMinus1: []uint32{9, 4}, RowHeightMinus1: []uint32{7}}
	if got := explicit.TileColumnWidths(sps); !reflect.DeepEqual(got, []int{10, 5, 15}) {
		t.Errorf("explicit columns %v", got)
	}
	if got := explicit.TileRowHeights(sps); !reflect.DeepEqual(got, []int{8, 9}) {
		t.Errorf("explicit rows %v", got)
	}
	none := &PPS{NumTileColumns: 1, NumTileRows: 1, UniformSpacing: true}
	if got := none.TileColumnWidths(sps); !reflect.DeepEqual(got, []int{30}) {
		t.Errorf("no tiles: columns %v", got)
	}
}

// TestSliceHeaderEmulationPrevention parses a slice segment header that
// needs emulation prevention bytes (a header extension of zero bytes): the
// header size is counted without them and their number is reported, so that
// both the unescaped offset VA-API asks for and the position in the stream
// are known.
func TestSliceHeaderEmulationPrevention(t *testing.T) {
	sps := &SPS{
		TemporalIDNesting: true, ChromaFormatIDC: 1, Width: 64, Height: 64, BitDepthLuma: 8, BitDepthChroma: 8, Log2MaxPOCLsb: 8,
		MaxDecPicBufferingMinus1: []uint32{0}, MaxNumReorderPics: []uint32{0}, MaxLatencyIncreasePlus1: []uint32{0},
		Log2DiffMaxMinLumaCodingBlockSize: 3, Log2DiffMaxMinLumaTransformBlockSize: 3,
	}
	pps := &PPS{NumRefIdxL0DefaultActive: 1, NumRefIdxL1DefaultActive: 1, NumTileColumns: 1, NumTileRows: 1,
		UniformSpacing: true, LoopFilterAcrossTilesEnabled: true, SliceSegmentHeaderExtensionPresent: true}
	ps := NewParameterSets()
	if _, err := ps.AddSPS(WriteSPS(sps)); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.AddPPS(WritePPS(pps)); err != nil {
		t.Fatal(err)
	}

	w := bitstream.NewWriter()
	w.WriteFlag(true)  // first_slice_segment_in_pic_flag
	w.WriteFlag(false) // no_output_of_prior_pics_flag
	w.WriteUE(0)       // slice_pic_parameter_set_id
	w.WriteUE(SliceI)
	w.WriteSE(-2) // slice_qp_delta
	w.WriteUE(6)  // slice_segment_header_extension_length
	for i := 0; i < 6; i++ {
		w.WriteBits(0, 8)
	}
	w.Trailing()
	unescaped := 2 + len(w.Bytes())
	header := NALUnit(NALIDRNLP, 0, w.Bytes())
	epb := len(header) - unescaped
	if epb < 2 {
		t.Fatalf("the test header has %d emulation prevention bytes; it should need at least 2", epb)
	}
	nal := append(append([]byte(nil), header...), 0xAB, 0xCD, 0x80)
	sh, _, _, err := ParseSliceHeader(nal, ps, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sh.SliceQpDelta != -2 || sh.SliceType != SliceI {
		t.Errorf("header %+v", sh)
	}
	if sh.DataByteOffset() != unescaped || sh.EmulationPreventionBytes != epb {
		t.Errorf("slice data at unescaped byte %d with %d emulation prevention bytes, want %d and %d",
			sh.DataByteOffset(), sh.EmulationPreventionBytes, unescaped, epb)
	}
	if got := nal[sh.DataByteOffset()+sh.EmulationPreventionBytes]; got != 0xAB {
		t.Errorf("byte at the slice data position is %#x, want 0xAB", got)
	}
}
