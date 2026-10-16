package hevc_test

import (
	"testing"

	"github.com/shibukawa/hwmediacodec/internal/hevc"
)

func parseSlice(t *testing.T, sps *hevc.SPS, pps *hevc.PPS, h *hevc.SliceHeader) (*hevc.SliceHeader, error) {
	t.Helper()
	ps := hevc.NewParameterSets()
	if _, err := ps.AddSPS(hevc.WriteSPS(sps)); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.AddPPS(hevc.WritePPS(pps)); err != nil {
		t.Fatal(err)
	}
	// The writers produce whole NAL units; a few bytes of slice data follow.
	nal := append(hevc.WriteSliceHeader(h, sps, pps), 0xAB, 0xCD, 0xEF, 0x80)
	sh, _, _, err := hevc.ParseSliceHeader(nal, ps, nil)
	return sh, err
}

// Explicit tile sizes that do not fit the picture are refused when the
// slice ties the PPS to its SPS, instead of producing a negative last
// column that becomes 65535 in the driver's picture parameters.
func TestSliceHeaderRejectsTilesLargerThanPicture(t *testing.T) {
	sps := writerSPS()[0]
	ctbs := uint32(sps.PicWidthInCtbs())
	pps := writerPPS(sps.ID, false)
	pps.TilesEnabled, pps.UniformSpacing = true, false
	pps.NumTileColumns, pps.ColumnWidthMinus1 = 2, []uint32{ctbs + 100}
	h := writerSlices(sps, pps)[0]
	if _, err := parseSlice(t, sps, pps, h); err == nil {
		t.Fatalf("accepted a tile column wider than the picture: %v", err)
	}
	pps.ColumnWidthMinus1 = []uint32{ctbs - 1} // leaves no block for the second column
	if _, err := parseSlice(t, sps, pps, h); err == nil {
		t.Fatalf("accepted an empty last tile column: %v", err)
	}
	pps.ColumnWidthMinus1 = []uint32{ctbs/2 - 1}
	if _, err := parseSlice(t, sps, pps, h); err != nil {
		t.Fatalf("rejected two tile columns that fit: %v", err)
	}
}
