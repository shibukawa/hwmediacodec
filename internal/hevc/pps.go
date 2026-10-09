package hevc

// Tile grid limits of the highest level (A.4.2, Table A.8).
const (
	MaxTileColumns = 20
	MaxTileRows    = 22
)

// PPS is a picture parameter set (7.3.2.3). ParsePPSHead fills the fields
// up to and including NumExtraSliceHeaderBits; ParsePPS fills everything.
type PPS struct {
	ID                            uint32
	SPSID                         uint32
	DependentSliceSegmentsEnabled bool
	OutputFlagPresent             bool
	NumExtraSliceHeaderBits       int

	SignDataHidingEnabled       bool
	CabacInitPresent            bool
	NumRefIdxL0DefaultActive    uint32 // num_ref_idx_l0_default_active_minus1 + 1
	NumRefIdxL1DefaultActive    uint32
	InitQpMinus26               int32
	ConstrainedIntraPred        bool
	TransformSkipEnabled        bool
	CuQpDeltaEnabled            bool
	DiffCuQpDeltaDepth          uint32
	CbQpOffset                  int32
	CrQpOffset                  int32
	SliceChromaQpOffsetsPresent bool
	WeightedPred                bool
	WeightedBipred              bool
	TransquantBypassEnabled     bool
	TilesEnabled                bool
	EntropyCodingSyncEnabled    bool

	// Tile grid. NumTileColumns and NumTileRows are 1 without tiles. With
	// UniformSpacing the sizes follow from the picture size (see
	// TileColumnWidths); otherwise ColumnWidthMinus1 and RowHeightMinus1
	// hold the coded sizes of all but the last column and row.
	NumTileColumns               int
	NumTileRows                  int
	UniformSpacing               bool
	ColumnWidthMinus1            []uint32
	RowHeightMinus1              []uint32
	LoopFilterAcrossTilesEnabled bool

	LoopFilterAcrossSlicesEnabled   bool
	DeblockingFilterControlPresent  bool
	DeblockingFilterOverrideEnabled bool
	DeblockingFilterDisabled        bool
	BetaOffsetDiv2                  int32
	TcOffsetDiv2                    int32

	ScalingListDataPresent bool
	ScalingList            ScalingList

	ListsModificationPresent           bool
	Log2ParallelMergeLevelMinus2       uint32
	SliceSegmentHeaderExtensionPresent bool

	ExtensionPresent    bool
	RangeExtension      bool
	MultilayerExtension bool
	Extension3D         bool
	SCCExtension        bool
	Extension4Bits      uint8
	Range               PPSRangeExtension
}

// PPSRangeExtension is pps_range_extension() (7.3.2.3.2).
type PPSRangeExtension struct {
	Log2MaxTransformSkipBlockSizeMinus2 uint32
	CrossComponentPredictionEnabled     bool
	ChromaQpOffsetListEnabled           bool
	DiffCuChromaQpOffsetDepth           uint32
	ChromaQpOffsetListLenMinus1         uint32
	CbQpOffsetList                      [6]int32
	CrQpOffsetList                      [6]int32
	Log2SaoOffsetScaleLuma              uint32
	Log2SaoOffsetScaleChroma            uint32
}

// TileColumnWidths returns the width of every tile column in coding tree
// blocks (6-3).
func (p *PPS) TileColumnWidths(sps *SPS) []int {
	return tileSizes(p.NumTileColumns, sps.PicWidthInCtbs(), p.UniformSpacing, p.ColumnWidthMinus1)
}

// TileRowHeights returns the height of every tile row in coding tree blocks
// (6-4).
func (p *PPS) TileRowHeights(sps *SPS) []int {
	return tileSizes(p.NumTileRows, sps.PicHeightInCtbs(), p.UniformSpacing, p.RowHeightMinus1)
}

func tileSizes(n, total int, uniform bool, minus1 []uint32) []int {
	if n < 1 {
		n = 1
	}
	out := make([]int, n)
	if uniform || n == 1 {
		for i := 0; i < n; i++ {
			out[i] = (i+1)*total/n - i*total/n
		}
		return out
	}
	rest := total
	for i := 0; i < n-1; i++ {
		if i < len(minus1) {
			out[i] = int(minus1[i]) + 1
		}
		rest -= out[i]
	}
	out[n-1] = rest
	return out
}

// ParsePPSHead parses the leading fields of a picture parameter set NAL
// unit.
func ParsePPSHead(nal []byte) (*PPS, error) {
	r, err := ppsReader(nal)
	if err != nil {
		return nil, err
	}
	p := &PPS{}
	r.ppsHead(p)
	if r.err != nil {
		return nil, r.err
	}
	return p, nil
}

// ParsePPS parses a complete picture parameter set NAL unit.
func ParsePPS(nal []byte) (*PPS, error) {
	r, err := ppsReader(nal)
	if err != nil {
		return nil, err
	}
	p := &PPS{}
	r.ppsHead(p)
	r.ppsBody(p)
	if r.err != nil {
		return nil, r.err
	}
	return p, nil
}

func ppsReader(nal []byte) (*reader, error) {
	if Type(nal) != NALPPS || len(nal) < 3 {
		return nil, syntaxErr("not a PPS NAL unit")
	}
	return newReader(nal[2:]), nil
}

func (r *reader) ppsHead(p *PPS) {
	p.ID = r.ueMax("pps_pic_parameter_set_id", maxPPSID)
	p.SPSID = r.ueMax("pps_seq_parameter_set_id", maxSPSID)
	p.DependentSliceSegmentsEnabled = r.flag()
	p.OutputFlagPresent = r.flag()
	p.NumExtraSliceHeaderBits = int(r.u(3))
}

func (r *reader) ppsBody(p *PPS) {
	p.SignDataHidingEnabled = r.flag()
	p.CabacInitPresent = r.flag()
	p.NumRefIdxL0DefaultActive = r.ueMax("num_ref_idx_l0_default_active_minus1", 14) + 1
	p.NumRefIdxL1DefaultActive = r.ueMax("num_ref_idx_l1_default_active_minus1", 14) + 1
	p.InitQpMinus26 = r.seRange("init_qp_minus26", -26-48, 25)
	p.ConstrainedIntraPred = r.flag()
	p.TransformSkipEnabled = r.flag()
	p.CuQpDeltaEnabled = r.flag()
	if p.CuQpDeltaEnabled {
		p.DiffCuQpDeltaDepth = r.ueMax("diff_cu_qp_delta_depth", 3)
	}
	p.CbQpOffset = r.seRange("pps_cb_qp_offset", -12, 12)
	p.CrQpOffset = r.seRange("pps_cr_qp_offset", -12, 12)
	p.SliceChromaQpOffsetsPresent = r.flag()
	p.WeightedPred = r.flag()
	p.WeightedBipred = r.flag()
	p.TransquantBypassEnabled = r.flag()
	p.TilesEnabled = r.flag()
	p.EntropyCodingSyncEnabled = r.flag()
	p.NumTileColumns, p.NumTileRows = 1, 1
	p.UniformSpacing = true
	p.LoopFilterAcrossTilesEnabled = true
	if p.TilesEnabled {
		p.NumTileColumns = int(r.ueMax("num_tile_columns_minus1", MaxTileColumns-1)) + 1
		p.NumTileRows = int(r.ueMax("num_tile_rows_minus1", MaxTileRows-1)) + 1
		p.UniformSpacing = r.flag()
		if r.err != nil {
			return
		}
		if !p.UniformSpacing {
			p.ColumnWidthMinus1 = make([]uint32, p.NumTileColumns-1)
			for i := range p.ColumnWidthMinus1 {
				p.ColumnWidthMinus1[i] = r.ueMax("column_width_minus1", 1<<16)
			}
			p.RowHeightMinus1 = make([]uint32, p.NumTileRows-1)
			for i := range p.RowHeightMinus1 {
				p.RowHeightMinus1[i] = r.ueMax("row_height_minus1", 1<<16)
			}
		}
		p.LoopFilterAcrossTilesEnabled = r.flag()
	}
	p.LoopFilterAcrossSlicesEnabled = r.flag()
	p.DeblockingFilterControlPresent = r.flag()
	if p.DeblockingFilterControlPresent {
		p.DeblockingFilterOverrideEnabled = r.flag()
		p.DeblockingFilterDisabled = r.flag()
		if !p.DeblockingFilterDisabled {
			p.BetaOffsetDiv2 = r.seRange("pps_beta_offset_div2", -6, 6)
			p.TcOffsetDiv2 = r.seRange("pps_tc_offset_div2", -6, 6)
		}
	}
	p.ScalingListDataPresent = r.flag()
	if p.ScalingListDataPresent {
		p.ScalingList = DefaultScalingList()
		r.scalingListData(&p.ScalingList)
	}
	p.ListsModificationPresent = r.flag()
	p.Log2ParallelMergeLevelMinus2 = r.ueMax("log2_parallel_merge_level_minus2", 4)
	p.SliceSegmentHeaderExtensionPresent = r.flag()
	p.ExtensionPresent = r.flag()
	if p.ExtensionPresent {
		p.RangeExtension = r.flag()
		p.MultilayerExtension = r.flag()
		p.Extension3D = r.flag()
		p.SCCExtension = r.flag()
		p.Extension4Bits = uint8(r.u(4))
	}
	if p.RangeExtension {
		e := &p.Range
		if p.TransformSkipEnabled {
			e.Log2MaxTransformSkipBlockSizeMinus2 = r.ueMax("log2_max_transform_skip_block_size_minus2", 3)
		}
		e.CrossComponentPredictionEnabled = r.flag()
		e.ChromaQpOffsetListEnabled = r.flag()
		if e.ChromaQpOffsetListEnabled {
			e.DiffCuChromaQpOffsetDepth = r.ueMax("diff_cu_chroma_qp_offset_depth", 3)
			e.ChromaQpOffsetListLenMinus1 = r.ueMax("chroma_qp_offset_list_len_minus1", 5)
			for i := 0; i <= int(e.ChromaQpOffsetListLenMinus1) && r.err == nil; i++ {
				e.CbQpOffsetList[i] = r.seRange("cb_qp_offset_list", -12, 12)
				e.CrQpOffsetList[i] = r.seRange("cr_qp_offset_list", -12, 12)
			}
		}
		e.Log2SaoOffsetScaleLuma = r.ue()
		e.Log2SaoOffsetScaleChroma = r.ue()
	}
	// The multilayer, 3D and screen content extensions are not modelled.
}
