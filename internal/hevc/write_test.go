package hevc_test

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/shibukawa/hwmediacodec/internal/hevc"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
)

func writerPTL() hevc.ProfileTierLevel {
	return hevc.ProfileTierLevel{ProfileIDC: hevc.ProfileMain, CompatibilityFlags: 3 << 29,
		ProgressiveSource: true, NonPackedConstraint: true, FrameOnlyConstraint: true, LevelIDC: 93}
}

// writerSPS returns sequence parameter sets that exercise the writer: the
// kind a low-delay hardware encoder emits and a richer one.
func writerSPS() []*hevc.SPS {
	prev := hevc.ShortTermRPS{NumNegativePics: 1}
	prev.DeltaPocS0[0] = -1
	prev.UsedByCurrPicS0[0] = true
	simple := &hevc.SPS{
		TemporalIDNesting: true, PTL: writerPTL(), ChromaFormatIDC: 1, Width: 328, Height: 248,
		ConformanceWindow: true, ConfWinRight: 3, ConfWinBottom: 3,
		BitDepthLuma: 8, BitDepthChroma: 8, Log2MaxPOCLsb: 12,
		MaxDecPicBufferingMinus1: []uint32{1}, MaxNumReorderPics: []uint32{0}, MaxLatencyIncreasePlus1: []uint32{0},
		Log2DiffMaxMinLumaCodingBlockSize: 3, Log2DiffMaxMinLumaTransformBlockSize: 3,
		MaxTransformHierarchyDepthInter: 2, MaxTransformHierarchyDepthIntra: 2,
		AMPEnabled: true, SAOEnabled: true, ShortTermRPS: []hevc.ShortTermRPS{prev},
		VUIPresent: true,
		VUI: hevc.VUI{TimingInfoPresent: true, NumUnitsInTick: 1001, TimeScale: 30000,
			BitstreamRestriction: true, MotionVectorsOverPicBoundaries: true, RestrictedRefPicLists: true,
			Log2MaxMvLengthHorizontal: 15, Log2MaxMvLengthVertical: 15},
	}
	// Three sets: {-1}, {-1 -2 +1} and {-2 (unused) +2 +4}.
	b := hevc.ShortTermRPS{NumNegativePics: 2, NumPositivePics: 1}
	b.DeltaPocS0[0], b.DeltaPocS0[1], b.DeltaPocS1[0] = -1, -2, 1
	b.UsedByCurrPicS0[0], b.UsedByCurrPicS0[1], b.UsedByCurrPicS1[0] = true, true, true
	c := hevc.ShortTermRPS{NumNegativePics: 1, NumPositivePics: 2}
	c.DeltaPocS0[0], c.DeltaPocS1[0], c.DeltaPocS1[1] = -2, 2, 4
	c.UsedByCurrPicS1[0], c.UsedByCurrPicS1[1] = true, true
	rich := &hevc.SPS{
		ID: 3, VPSID: 2, TemporalIDNesting: true, PTL: writerPTL(), ChromaFormatIDC: 1, Width: 1920, Height: 1088,
		ConformanceWindow: true, ConfWinBottom: 4,
		BitDepthLuma: 8, BitDepthChroma: 8, Log2MaxPOCLsb: 8, SubLayerOrderingInfoPresent: true,
		MaxDecPicBufferingMinus1: []uint32{4}, MaxNumReorderPics: []uint32{2}, MaxLatencyIncreasePlus1: []uint32{5},
		Log2MinLumaCodingBlockSizeMinus3: 1, Log2DiffMaxMinLumaCodingBlockSize: 1,
		Log2MinLumaTransformBlockSizeMinus2: 0, Log2DiffMaxMinLumaTransformBlockSize: 3,
		MaxTransformHierarchyDepthInter: 1, MaxTransformHierarchyDepthIntra: 3,
		ScalingListEnabled: true, ScalingList: hevc.DefaultScalingList(),
		PCMEnabled: true, PCMSampleBitDepthLumaMinus1: 7, PCMSampleBitDepthChromaMinus1: 7,
		Log2MinPCMLumaCodingBlockSizeMinus3: 1, Log2DiffMaxMinPCMLumaCodingBlockSize: 1, PCMLoopFilterDisabled: true,
		ShortTermRPS:           []hevc.ShortTermRPS{simple.ShortTermRPS[0], b, c},
		LongTermRefPicsPresent: true, LtRefPicPOCLsbSPS: []uint32{0, 17}, UsedByCurrPicLtSPS: []bool{true, false},
		TemporalMVPEnabled: true, StrongIntraSmoothingEnabled: true,
		VUIPresent: true,
		VUI: hevc.VUI{AspectRatioInfoPresent: true, AspectRatioIDC: 255, SarWidth: 4, SarHeight: 3,
			OverscanInfoPresent: true, OverscanAppropriate: true,
			VideoSignalTypePresent: true, VideoFormat: 5, VideoFullRange: true, ColourDescriptionPresent: true,
			ColourPrimaries: 1, TransferCharacteristics: 1, MatrixCoeffs: 1,
			ChromaLocInfoPresent: true, ChromaSampleLocTypeTopField: 1, ChromaSampleLocTypeBottomField: 2,
			DefaultDisplayWindow: true, DefDispWinLeft: 1, DefDispWinRight: 2, DefDispWinTop: 3, DefDispWinBottom: 4,
			TimingInfoPresent: true, NumUnitsInTick: 1, TimeScale: 60, POCProportionalToTiming: true, NumTicksPOCDiffOneMinus1: 1},
	}
	return []*hevc.SPS{simple, rich}
}

func writerPPS(spsID uint32, rich bool) *hevc.PPS {
	p := &hevc.PPS{SPSID: spsID, NumRefIdxL0DefaultActive: 1, NumRefIdxL1DefaultActive: 1,
		NumTileColumns: 1, NumTileRows: 1, UniformSpacing: true, LoopFilterAcrossTilesEnabled: true,
		LoopFilterAcrossSlicesEnabled: true}
	if rich {
		p.ID = 5
		p.DependentSliceSegmentsEnabled = true
		p.OutputFlagPresent = true
		p.NumExtraSliceHeaderBits = 2
		p.SignDataHidingEnabled = true
		p.CabacInitPresent = true
		p.NumRefIdxL0DefaultActive, p.NumRefIdxL1DefaultActive = 3, 2
		p.InitQpMinus26 = -5
		p.ConstrainedIntraPred, p.TransformSkipEnabled = true, true
		p.CuQpDeltaEnabled, p.DiffCuQpDeltaDepth = true, 1
		p.CbQpOffset, p.CrQpOffset = 2, -3
		p.SliceChromaQpOffsetsPresent = true
		p.TransquantBypassEnabled = true
		p.TilesEnabled, p.EntropyCodingSyncEnabled = true, true
		p.NumTileColumns, p.NumTileRows, p.UniformSpacing = 3, 2, false
		p.ColumnWidthMinus1, p.RowHeightMinus1 = []uint32{9, 4}, []uint32{7}
		p.LoopFilterAcrossTilesEnabled = false
		p.DeblockingFilterControlPresent, p.DeblockingFilterOverrideEnabled = true, true
		p.BetaOffsetDiv2, p.TcOffsetDiv2 = 2, -1
		p.ListsModificationPresent = true
		p.Log2ParallelMergeLevelMinus2 = 1
		p.SliceSegmentHeaderExtensionPresent = true
	}
	return p
}

// TestWriteParameterSetsRoundTrip serialises SPS/PPS and parses them back.
func TestWriteParameterSetsRoundTrip(t *testing.T) {
	for i, sps := range writerSPS() {
		got, err := hevc.ParseSPS(hevc.WriteSPS(sps))
		if err != nil {
			t.Fatalf("SPS %d: parse written SPS: %v", i, err)
		}
		if !reflect.DeepEqual(got, sps) {
			t.Errorf("SPS %d: round trip differs:\n got %+v\nwant %+v", i, *got, *sps)
		}
		pps := writerPPS(sps.ID, i == 1)
		gp, err := hevc.ParsePPS(hevc.WritePPS(pps))
		if err != nil {
			t.Fatalf("PPS %d: parse written PPS: %v", i, err)
		}
		if !reflect.DeepEqual(gp, pps) {
			t.Errorf("PPS %d: round trip differs:\n got %+v\nwant %+v", i, *gp, *pps)
		}
	}
}

// writerSlices returns slice segment headers valid for the given parameter
// sets: the IDR and P pictures a hardware encoder writes plus B pictures.
func writerSlices(sps *hevc.SPS, pps *hevc.PPS) []*hevc.SliceHeader {
	base := func(nalType int, sliceType uint32) *hevc.SliceHeader {
		return &hevc.SliceHeader{NALType: nalType, FirstSliceSegmentInPic: true, PPSID: pps.ID, SliceType: sliceType,
			PicOutput: true, SAOLuma: sps.SAOEnabled, SAOChroma: sps.SAOEnabled, CollocatedFromL0: true,
			LoopFilterAcrossSlicesEnabled: pps.LoopFilterAcrossSlicesEnabled,
			DeblockingFilterDisabled:      pps.DeblockingFilterDisabled, BetaOffsetDiv2: pps.BetaOffsetDiv2, TcOffsetDiv2: pps.TcOffsetDiv2}
	}
	fromSPS := func(h *hevc.SliceHeader, idx int) {
		h.ShortTermRefPicSetSPSFlag = true
		h.ShortTermRefPicSetIdx = uint32(idx)
		h.ShortTermRPS = &sps.ShortTermRPS[idx]
		h.NumPicTotalCurr = h.ShortTermRPS.NumUsedByCurr()
	}
	idr := base(hevc.NALIDRWRADL, hevc.SliceI)
	idr.SliceQpDelta = -3
	p := base(hevc.NALTrailR, hevc.SliceP)
	p.POCLsb = 7
	fromSPS(p, 0)
	p.NumRefIdxL0Active = pps.NumRefIdxL0DefaultActive
	p.SliceQpDelta = 2
	out := []*hevc.SliceHeader{idr, p}
	if len(sps.ShortTermRPS) > 1 {
		// A B picture with its own lists sizes, and one that codes its
		// reference picture set in the header.
		b := base(hevc.NALTrailN, hevc.SliceB)
		b.TemporalID = 1
		b.POCLsb = 201
		fromSPS(b, 1)
		b.NumRefIdxActiveOverride = true
		b.NumRefIdxL0Active, b.NumRefIdxL1Active = 2, 1
		b.MvdL1Zero, b.CabacInit = true, true
		b.SliceTemporalMVPEnabled = true
		b.CollocatedFromL0, b.CollocatedRefIdx = true, 1
		b.FiveMinusMaxNumMergeCand = 3
		b.SliceCbQpOffset, b.SliceCrQpOffset = -2, 1
		b.DeblockingFilterOverride = true
		b.BetaOffsetDiv2, b.TcOffsetDiv2 = -4, 5
		b.PicOutput = false
		b.LoopFilterAcrossSlicesEnabled = false
		own := hevc.ShortTermRPS{NumNegativePics: 2, NumPositivePics: 2}
		own.DeltaPocS0[0], own.DeltaPocS0[1], own.DeltaPocS1[0], own.DeltaPocS1[1] = -1, -5, 3, 4
		own.UsedByCurrPicS0[0], own.UsedByCurrPicS1[1] = true, true
		b2 := base(hevc.NALRASLR, hevc.SliceB)
		b2.POCLsb = 40
		b2.ShortTermRPS = &own
		b2.NumPicTotalCurr = 2
		b2.NumRefIdxL0Active, b2.NumRefIdxL1Active = pps.NumRefIdxL0DefaultActive, pps.NumRefIdxL1DefaultActive
		b2.SliceTemporalMVPEnabled = true
		b2.CollocatedFromL0, b2.CollocatedRefIdx = false, 1
		// Deblocking switched off for the slice: the offsets keep the
		// values of the PPS.
		b2.DeblockingFilterOverride, b2.DeblockingFilterDisabled = true, true
		b2.SAOLuma, b2.SAOChroma = false, false
		// A second slice segment of the same picture.
		b3 := *b2
		b3.FirstSliceSegmentInPic = false
		b3.SliceSegmentAddress = 100
		out = append(out, b, b2, &b3)
	}
	return out
}

func writtenStream(t *testing.T, sps *hevc.SPS, pps *hevc.PPS) (stream []byte, slices []*hevc.SliceHeader) {
	t.Helper()
	var buf bytes.Buffer
	add := func(nal []byte) {
		buf.Write([]byte{0, 0, 0, 1})
		buf.Write(nal)
	}
	add(hevc.WriteVPS(&hevc.VPS{ID: sps.VPSID, TemporalIDNesting: sps.TemporalIDNesting, PTL: sps.PTL,
		MaxDecPicBufferingMinus1: sps.MaxDecPicBufferingMinus1[0], MaxNumReorderPics: sps.MaxNumReorderPics[0],
		MaxLatencyIncreasePlus1: sps.MaxLatencyIncreasePlus1[0],
		TimingInfoPresent:       sps.VUI.TimingInfoPresent, NumUnitsInTick: sps.VUI.NumUnitsInTick, TimeScale: sps.VUI.TimeScale}))
	add(hevc.WriteSPS(sps))
	add(hevc.WritePPS(pps))
	slices = writerSlices(sps, pps)
	for _, sh := range slices {
		// Fake slice data (non-zero so that no emulation issue arises).
		add(append(hevc.WriteSliceHeader(sh, sps, pps), 0xAB, 0xCD, 0xEF, 0x80))
	}
	return buf.Bytes(), slices
}

// TestWriteSliceHeaderRoundTrip writes slice segment headers, appends fake
// slice data and parses them back, checking the header size too.
func TestWriteSliceHeaderRoundTrip(t *testing.T) {
	for i, sps := range writerSPS() {
		pps := writerPPS(sps.ID, i == 1)
		ps := hevc.NewParameterSets()
		if _, err := ps.AddSPS(hevc.WriteSPS(sps)); err != nil {
			t.Fatal(err)
		}
		if _, err := ps.AddPPS(hevc.WritePPS(pps)); err != nil {
			t.Fatal(err)
		}
		parsedSPS := ps.SPS(sps.ID)
		for j, sh := range writerSlices(sps, pps) {
			hdr := hevc.WriteSliceHeader(sh, sps, pps)
			nal := append(append([]byte(nil), hdr...), 0xAB, 0xCD, 0xEF, 0x80)
			got, _, _, err := hevc.ParseSliceHeader(nal, ps, nil)
			if err != nil {
				t.Fatalf("SPS %d slice %d: parse: %v", i, j, err)
			}
			want := *sh
			want.HeaderBits = 8 * len(hdr)
			if want.ShortTermRefPicSetSPSFlag {
				want.ShortTermRPS = &parsedSPS.ShortTermRPS[want.ShortTermRefPicSetIdx]
			} else if want.ShortTermRPS != nil {
				want.ShortTermRPSBits = got.ShortTermRPSBits
				if got.ShortTermRPSBits == 0 {
					t.Errorf("SPS %d slice %d: no size recorded for the reference picture set in the header", i, j)
				}
			}
			if !reflect.DeepEqual(got, &want) {
				t.Errorf("SPS %d slice %d: round trip differs:\n got %+v\nwant %+v", i, j, *got, want)
			}
			if got.DataByteOffset() != len(hdr) {
				t.Errorf("SPS %d slice %d: slice data at byte %d, the header is %d bytes", i, j, got.DataByteOffset(), len(hdr))
			}
		}
	}
}

// TestWrittenHeadersMatchFFmpeg feeds written parameter sets and slice
// segment headers (with fake slice data) to ffmpeg's trace_headers and
// compares every element.
func TestWrittenHeadersMatchFFmpeg(t *testing.T) {
	testutil.RequireFFmpeg(t)
	for i, sps := range writerSPS() {
		pps := writerPPS(sps.ID, i == 1)
		stream, slices := writtenStream(t, sps, pps)
		path := filepath.Join(t.TempDir(), "written.hevc")
		if err := os.WriteFile(path, stream, 0o644); err != nil {
			t.Fatal(err)
		}
		n, _ := compareWithTrace(t, path, parseStream(t, stream))
		if n != len(slices) {
			t.Errorf("SPS %d: ffmpeg traced %d slice segments, wrote %d", i, n, len(slices))
		}
	}
}
