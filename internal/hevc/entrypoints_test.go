package hevc

import (
	"runtime"
	"testing"

	"github.com/shibukawa/hwmediacodec/internal/bitstream"
)

// A slice header announcing millions of entry points is refused by the
// bound of 7.4.7.1 before anything is allocated for them: with a
// 65536x65536 SPS a 13-byte slice used to make the parser allocate 64 MiB.
func TestSliceHeaderBoundsEntryPoints(t *testing.T) {
	sps := &SPS{
		TemporalIDNesting: true, ChromaFormatIDC: 1, Width: 1 << 16, Height: 1 << 16, BitDepthLuma: 8, BitDepthChroma: 8, Log2MaxPOCLsb: 8,
		MaxDecPicBufferingMinus1: []uint32{0}, MaxNumReorderPics: []uint32{0}, MaxLatencyIncreasePlus1: []uint32{0},
		Log2DiffMaxMinLumaCodingBlockSize: 3, Log2DiffMaxMinLumaTransformBlockSize: 3,
	}
	pps := &PPS{NumRefIdxL0DefaultActive: 1, NumRefIdxL1DefaultActive: 1, NumTileColumns: 1, NumTileRows: 1,
		UniformSpacing: true, LoopFilterAcrossTilesEnabled: true, EntropyCodingSyncEnabled: true}
	ps := NewParameterSets()
	if _, err := ps.AddSPS(WriteSPS(sps)); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.AddPPS(WritePPS(pps)); err != nil {
		t.Fatal(err)
	}
	slice := func(entryPoints uint32, offsets int) []byte {
		w := bitstream.NewWriter()
		w.WriteFlag(true)  // first_slice_segment_in_pic_flag
		w.WriteFlag(false) // no_output_of_prior_pics_flag
		w.WriteUE(0)       // slice_pic_parameter_set_id
		w.WriteUE(SliceI)
		w.WriteSE(0) // slice_qp_delta
		w.WriteUE(entryPoints)
		if entryPoints > 0 {
			w.WriteUE(0) // offset_len_minus1
			for range offsets {
				w.WriteBit(0)
			}
		}
		w.Trailing()
		return append(NALUnit(NALIDRNLP, 0, w.Bytes()), 0xAB, 0xCD, 0x80)
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, _, _, err := ParseSliceHeader(slice(1<<24, 0), ps, nil)
	runtime.ReadMemStats(&after)
	if err == nil {
		t.Fatal("accepted 16777216 entry points for a picture of 1024 coding tree block rows")
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 1<<20 {
		t.Fatalf("parsing the slice header allocated %d bytes", grew)
	}
	// The bound itself: one entry point per coding tree block row.
	rows := uint32(sps.PicHeightInCtbs())
	if _, _, _, err := ParseSliceHeader(slice(rows, 0), ps, nil); err == nil {
		t.Fatalf("accepted %d entry points for %d rows", rows, rows)
	}
	sh, _, _, err := ParseSliceHeader(slice(rows-1, int(rows-1)), ps, nil)
	if err != nil || int(sh.NumEntryPointOffsets) != len(sh.EntryPointOffsetMinus1) || sh.NumEntryPointOffsets != rows-1 {
		t.Fatalf("a slice with %d entry points: %v (%d read)", rows-1, err, len(sh.EntryPointOffsetMinus1))
	}
	// A header cut short stops where the data ends instead of allocating
	// for the announced count.
	runtime.ReadMemStats(&before)
	if _, _, _, err := ParseSliceHeader(slice(rows-1, 8), ps, nil); err == nil {
		t.Fatal("accepted a slice header truncated inside its entry points")
	}
	runtime.ReadMemStats(&after)
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 64<<10 {
		t.Fatalf("a truncated header allocated %d bytes", grew)
	}
}
