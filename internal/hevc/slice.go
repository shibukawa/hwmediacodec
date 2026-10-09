package hevc

// MaxRefIdx bounds num_ref_idx_l0_active_minus1 + 1 and its L1 counterpart.
const MaxRefIdx = 15

// LongTermRef is one long-term reference picture signalled in a slice
// segment header, with the variables of 7.4.7.1 already derived.
type LongTermRef struct {
	// LtIdxSPS is lt_idx_sps for entries taken from the SPS (FromSPS set).
	FromSPS  bool
	LtIdxSPS uint32
	// POCLsb is PocLsbLt and UsedByCurrPic is UsedByCurrPicLt.
	POCLsb        uint32
	UsedByCurrPic bool
	// DeltaPOCMsbPresent is delta_poc_msb_present_flag; DeltaPOCMsbCycle is
	// the accumulated DeltaPocMsbCycleLt and DeltaPOCMsbCycleLt the coded
	// delta_poc_msb_cycle_lt.
	DeltaPOCMsbPresent bool
	DeltaPOCMsbCycle   uint32
	DeltaPOCMsbCycleLt uint32
}

// PredWeight is the weighted prediction entry of one reference index.
type PredWeight struct {
	LumaFlag          bool
	ChromaFlag        bool
	DeltaLumaWeight   int32
	LumaOffset        int32
	DeltaChromaWeight [2]int32
	DeltaChromaOffset [2]int32
	// ChromaOffset is the derived offset (7-56).
	ChromaOffset [2]int32
}

// PredWeightTable is pred_weight_table() (7.3.6.3).
type PredWeightTable struct {
	LumaLog2WeightDenom        uint32
	DeltaChromaLog2WeightDenom int32
	L0                         [MaxRefIdx]PredWeight
	L1                         [MaxRefIdx]PredWeight
}

// SliceHeader is a slice segment header (7.3.6.1). ParseSliceHead fills
// NALType, TemporalID, NoOutputOfPriorPics, PPSID, SliceType, PicOutput and
// POCLsb of the first segment of a picture; ParseSliceHeader fills
// everything, for dependent slice segments with the values of the
// independent segment they continue.
type SliceHeader struct {
	NALType    int
	TemporalID int

	FirstSliceSegmentInPic bool
	NoOutputOfPriorPics    bool
	PPSID                  uint32
	DependentSliceSegment  bool
	SliceSegmentAddress    uint32

	SliceType     uint32 // SliceB, SliceP or SliceI
	PicOutput     bool
	ColourPlaneID uint32
	POCLsb        uint32

	// ShortTermRPS is the set in effect: one of the SPS (SPSFlag set) or
	// the one coded in this header, which then took ShortTermRPSBits bits.
	// It is nil for IDR pictures.
	ShortTermRefPicSetSPSFlag bool
	ShortTermRefPicSetIdx     uint32
	ShortTermRPS              *ShortTermRPS
	ShortTermRPSBits          int

	NumLongTermSPS  uint32
	NumLongTermPics uint32
	LongTerm        []LongTermRef

	SliceTemporalMVPEnabled bool
	SAOLuma                 bool
	SAOChroma               bool

	NumRefIdxActiveOverride bool
	// NumRefIdxL0Active and NumRefIdxL1Active are the list sizes in effect
	// (0 for lists the slice type does not use).
	NumRefIdxL0Active uint32
	NumRefIdxL1Active uint32
	// NumPicTotalCurr is the number of pictures the slice may reference.
	NumPicTotalCurr int

	RefPicListModificationL0 bool
	RefPicListModificationL1 bool
	ListEntryL0              []uint32
	ListEntryL1              []uint32

	MvdL1Zero        bool
	CabacInit        bool
	CollocatedFromL0 bool
	CollocatedRefIdx uint32
	PredWeights      *PredWeightTable

	FiveMinusMaxNumMergeCand uint32
	SliceQpDelta             int32
	SliceCbQpOffset          int32
	SliceCrQpOffset          int32
	CuChromaQpOffsetEnabled  bool

	DeblockingFilterOverride      bool
	DeblockingFilterDisabled      bool
	BetaOffsetDiv2                int32
	TcOffsetDiv2                  int32
	LoopFilterAcrossSlicesEnabled bool

	NumEntryPointOffsets   uint32
	OffsetLenMinus1        uint32
	EntryPointOffsetMinus1 []uint32

	// HeaderBits is the size of the NAL unit header plus the slice segment
	// header including byte_alignment(), in bits after the removal of
	// emulation prevention bytes. slice_segment_data() starts at byte
	// HeaderBits/8 of the unescaped NAL unit.
	HeaderBits int
	// EmulationPreventionBytes is the number of emulation prevention bytes
	// inside the slice segment header: slice_segment_data() starts at byte
	// HeaderBits/8 + EmulationPreventionBytes of the NAL unit as it is in
	// the stream.
	EmulationPreventionBytes int
}

// DataByteOffset is the offset of slice_segment_data() from the start of
// the NAL unit header, counted after the removal of emulation prevention
// bytes (VA-API's slice_data_byte_offset).
func (h *SliceHeader) DataByteOffset() int { return h.HeaderBits / 8 }

// ParseSliceHead parses the leading fields of the first slice segment of a
// picture. lookup resolves a PPS id to the PPS and its SPS (parsed at least
// with ParsePPSHead and ParseSPSHead); it returns nil when either is
// unknown.
func ParseSliceHead(nal []byte, lookup func(ppsID uint32) (*PPS, *SPS)) (*SliceHeader, error) {
	t := Type(nal)
	if !IsVCL(t) || len(nal) < 3 {
		return nil, ErrNotSlice
	}
	h := &SliceHeader{NALType: t, TemporalID: TemporalID(nal), PicOutput: true}
	r := newReader(nal[2:])
	h.FirstSliceSegmentInPic = r.flag()
	if r.err != nil {
		return nil, r.err
	}
	if !h.FirstSliceSegmentInPic {
		return nil, ErrNotFirstSegment
	}
	if IsIRAP(t) {
		h.NoOutputOfPriorPics = r.flag()
	}
	h.PPSID = r.ue()
	if r.err != nil {
		return nil, r.err
	}
	pps, sps := lookup(h.PPSID)
	if pps == nil || sps == nil {
		return nil, syntaxErr("slice refers to unknown PPS %d", h.PPSID)
	}
	r.sliceTypeAndPOC(h, sps, pps)
	if r.err != nil {
		return nil, r.err
	}
	return h, nil
}

// sliceTypeAndPOC reads the fields between slice_pic_parameter_set_id and
// slice_pic_order_cnt_lsb of an independent slice segment.
func (r *reader) sliceTypeAndPOC(h *SliceHeader, sps *SPS, pps *PPS) {
	r.skip(pps.NumExtraSliceHeaderBits) // slice_reserved_flag
	h.SliceType = r.ueMax("slice_type", 2)
	if pps.OutputFlagPresent {
		h.PicOutput = r.flag()
	}
	if sps.SeparateColourPlane {
		h.ColourPlaneID = r.u(2)
	}
	if !IsIDR(h.NALType) {
		h.POCLsb = r.u(sps.Log2MaxPOCLsb)
	}
}

// ParseSliceHeader parses a complete slice segment header. prev is the
// header of the preceding slice segment of the same picture; it is needed
// for dependent slice segments and may be nil for the first segment.
func ParseSliceHeader(nal []byte, ps *ParameterSets, prev *SliceHeader) (*SliceHeader, *SPS, *PPS, error) {
	t := Type(nal)
	if !IsSlice(t) || len(nal) < 3 {
		return nil, nil, nil, ErrNotSlice
	}
	r := newReader(nal[2:])
	first := r.flag()
	noOutputOfPriorPics := false
	if IsIRAP(t) {
		noOutputOfPriorPics = r.flag()
	}
	ppsID := r.ueMax("slice_pic_parameter_set_id", maxPPSID)
	if r.err != nil {
		return nil, nil, nil, r.err
	}
	sps, pps, err := ps.Lookup(ppsID)
	if err != nil {
		return nil, nil, nil, err
	}

	dependent := false
	address := uint32(0)
	if !first {
		if pps.DependentSliceSegmentsEnabled {
			dependent = r.flag()
		}
		address = r.u(ceilLog2(sps.PicSizeInCtbs()))
		if r.err == nil && int(address) >= sps.PicSizeInCtbs() {
			r.fail("slice_segment_address %d out of range", address)
		}
	}
	if r.err != nil {
		return nil, nil, nil, r.err
	}

	var h *SliceHeader
	if dependent {
		if prev == nil || prev.PPSID != ppsID {
			return nil, nil, nil, syntaxErr("dependent slice segment without the slice segment it continues")
		}
		c := *prev
		h = &c
		h.EntryPointOffsetMinus1 = nil
	} else {
		h = &SliceHeader{PicOutput: true, CollocatedFromL0: true}
	}
	h.NALType = t
	h.TemporalID = TemporalID(nal)
	h.FirstSliceSegmentInPic = first
	h.NoOutputOfPriorPics = noOutputOfPriorPics
	h.PPSID = ppsID
	h.DependentSliceSegment = dependent
	h.SliceSegmentAddress = address

	if !dependent {
		r.sliceBody(h, sps, pps)
	}

	h.NumEntryPointOffsets, h.OffsetLenMinus1 = 0, 0
	if pps.TilesEnabled || pps.EntropyCodingSyncEnabled {
		h.NumEntryPointOffsets = r.ueMax("num_entry_point_offsets", uint32(sps.PicSizeInCtbs()))
		if h.NumEntryPointOffsets > 0 {
			h.OffsetLenMinus1 = r.ueMax("offset_len_minus1", 31)
			if r.err == nil {
				h.EntryPointOffsetMinus1 = make([]uint32, h.NumEntryPointOffsets)
				for i := range h.EntryPointOffsetMinus1 {
					h.EntryPointOffsetMinus1[i] = r.u(int(h.OffsetLenMinus1) + 1)
				}
			}
		}
	}
	if pps.SliceSegmentHeaderExtensionPresent {
		n := r.ueMax("slice_segment_header_extension_length", 256)
		r.skip(8 * int(n))
	}
	// byte_alignment()
	if !r.flag() && r.err == nil {
		r.fail("alignment_bit_equal_to_one is 0")
	}
	if r.err != nil {
		return nil, nil, nil, r.err
	}
	h.HeaderBits = 16 + (r.pos()+7)/8*8
	h.EmulationPreventionBytes = emulationPreventionBytes(nal[2:], h.HeaderBits/8-2)
	return h, sps, pps, nil
}

// emulationPreventionBytes counts the emulation prevention bytes among the
// escaped bytes that hold the first n bytes of an RBSP.
func emulationPreventionBytes(payload []byte, n int) int {
	count, zeros := 0, 0
	for _, b := range payload {
		if n == 0 {
			break
		}
		if zeros >= 2 && b == 3 {
			count++
			zeros = 0
			continue
		}
		if b == 0 {
			zeros++
		} else {
			zeros = 0
		}
		n--
	}
	return count
}

// sliceBody reads the part of an independent slice segment header between
// slice_segment_address and num_entry_point_offsets.
func (r *reader) sliceBody(h *SliceHeader, sps *SPS, pps *PPS) {
	r.sliceTypeAndPOC(h, sps, pps)
	if r.err != nil {
		return
	}
	chroma := sps.ChromaFormatIDC != 0 && !sps.SeparateColourPlane // ChromaArrayType != 0

	if !IsIDR(h.NALType) {
		h.ShortTermRefPicSetSPSFlag = r.flag()
		if !h.ShortTermRefPicSetSPSFlag {
			start := r.pos()
			rps := r.shortTermRPS(len(sps.ShortTermRPS), sps.ShortTermRPS, true)
			h.ShortTermRPSBits = r.pos() - start
			h.ShortTermRPS = &rps
		} else {
			n := len(sps.ShortTermRPS)
			if n == 0 {
				r.fail("short_term_ref_pic_set_sps_flag is set but the SPS has no sets")
				return
			}
			if n > 1 {
				h.ShortTermRefPicSetIdx = r.u(ceilLog2(n))
			}
			if int(h.ShortTermRefPicSetIdx) >= n {
				r.fail("short_term_ref_pic_set_idx %d out of range", h.ShortTermRefPicSetIdx)
				return
			}
			h.ShortTermRPS = &sps.ShortTermRPS[h.ShortTermRefPicSetIdx]
		}
		if r.err != nil {
			return
		}
		if sps.LongTermRefPicsPresent {
			r.longTermRefs(h, sps)
		}
		if sps.TemporalMVPEnabled {
			h.SliceTemporalMVPEnabled = r.flag()
		}
	}
	if r.err != nil {
		return
	}
	h.NumPicTotalCurr = 0
	if h.ShortTermRPS != nil {
		h.NumPicTotalCurr = h.ShortTermRPS.NumUsedByCurr()
	}
	for _, lt := range h.LongTerm {
		if lt.UsedByCurrPic {
			h.NumPicTotalCurr++
		}
	}

	if sps.SAOEnabled {
		h.SAOLuma = r.flag()
		if chroma {
			h.SAOChroma = r.flag()
		}
	}
	if h.SliceType == SliceP || h.SliceType == SliceB {
		h.NumRefIdxL0Active = pps.NumRefIdxL0DefaultActive
		if h.SliceType == SliceB {
			h.NumRefIdxL1Active = pps.NumRefIdxL1DefaultActive
		}
		h.NumRefIdxActiveOverride = r.flag()
		if h.NumRefIdxActiveOverride {
			h.NumRefIdxL0Active = r.ueMax("num_ref_idx_l0_active_minus1", MaxRefIdx-1) + 1
			if h.SliceType == SliceB {
				h.NumRefIdxL1Active = r.ueMax("num_ref_idx_l1_active_minus1", MaxRefIdx-1) + 1
			}
		}
		if r.err != nil {
			return
		}
		if pps.ListsModificationPresent && h.NumPicTotalCurr > 1 {
			bits := ceilLog2(h.NumPicTotalCurr)
			h.RefPicListModificationL0 = r.flag()
			if h.RefPicListModificationL0 {
				h.ListEntryL0 = make([]uint32, h.NumRefIdxL0Active)
				for i := range h.ListEntryL0 {
					h.ListEntryL0[i] = r.u(bits)
				}
			}
			if h.SliceType == SliceB {
				h.RefPicListModificationL1 = r.flag()
				if h.RefPicListModificationL1 {
					h.ListEntryL1 = make([]uint32, h.NumRefIdxL1Active)
					for i := range h.ListEntryL1 {
						h.ListEntryL1[i] = r.u(bits)
					}
				}
			}
		}
		if h.SliceType == SliceB {
			h.MvdL1Zero = r.flag()
		}
		if pps.CabacInitPresent {
			h.CabacInit = r.flag()
		}
		if h.SliceTemporalMVPEnabled {
			if h.SliceType == SliceB {
				h.CollocatedFromL0 = r.flag()
			}
			if (h.CollocatedFromL0 && h.NumRefIdxL0Active > 1) || (!h.CollocatedFromL0 && h.NumRefIdxL1Active > 1) {
				h.CollocatedRefIdx = r.ueMax("collocated_ref_idx", MaxRefIdx-1)
			}
		}
		if (pps.WeightedPred && h.SliceType == SliceP) || (pps.WeightedBipred && h.SliceType == SliceB) {
			h.PredWeights = r.predWeightTable(h, sps, chroma)
		}
		h.FiveMinusMaxNumMergeCand = r.ueMax("five_minus_max_num_merge_cand", 4)
	}
	h.SliceQpDelta = r.seRange("slice_qp_delta", -128, 127)
	if pps.SliceChromaQpOffsetsPresent {
		h.SliceCbQpOffset = r.seRange("slice_cb_qp_offset", -12, 12)
		h.SliceCrQpOffset = r.seRange("slice_cr_qp_offset", -12, 12)
	}
	if pps.Range.ChromaQpOffsetListEnabled {
		h.CuChromaQpOffsetEnabled = r.flag()
	}
	h.DeblockingFilterDisabled = pps.DeblockingFilterDisabled
	h.BetaOffsetDiv2 = pps.BetaOffsetDiv2
	h.TcOffsetDiv2 = pps.TcOffsetDiv2
	if pps.DeblockingFilterOverrideEnabled {
		h.DeblockingFilterOverride = r.flag()
	}
	if h.DeblockingFilterOverride {
		h.DeblockingFilterDisabled = r.flag()
		if !h.DeblockingFilterDisabled {
			h.BetaOffsetDiv2 = r.seRange("slice_beta_offset_div2", -6, 6)
			h.TcOffsetDiv2 = r.seRange("slice_tc_offset_div2", -6, 6)
		}
	}
	h.LoopFilterAcrossSlicesEnabled = pps.LoopFilterAcrossSlicesEnabled
	if pps.LoopFilterAcrossSlicesEnabled && (h.SAOLuma || h.SAOChroma || !h.DeblockingFilterDisabled) {
		h.LoopFilterAcrossSlicesEnabled = r.flag()
	}
}

// longTermRefs reads the long-term reference pictures of a slice segment
// header and derives PocLsbLt, UsedByCurrPicLt and DeltaPocMsbCycleLt.
func (r *reader) longTermRefs(h *SliceHeader, sps *SPS) {
	numSPS := len(sps.LtRefPicPOCLsbSPS)
	if numSPS > 0 {
		h.NumLongTermSPS = r.ueMax("num_long_term_sps", uint32(numSPS))
	}
	h.NumLongTermPics = r.ueMax("num_long_term_pics", maxRPSPics)
	if r.err != nil {
		return
	}
	n := int(h.NumLongTermSPS + h.NumLongTermPics)
	if n > maxRPSPics {
		r.fail("%d long-term reference pictures", n)
		return
	}
	if n == 0 {
		return
	}
	h.LongTerm = make([]LongTermRef, n)
	for i := range h.LongTerm {
		lt := &h.LongTerm[i]
		if i < int(h.NumLongTermSPS) {
			lt.FromSPS = true
			if numSPS > 1 {
				lt.LtIdxSPS = r.u(ceilLog2(numSPS))
			}
			if int(lt.LtIdxSPS) >= numSPS {
				r.fail("lt_idx_sps %d out of range", lt.LtIdxSPS)
				return
			}
			lt.POCLsb = sps.LtRefPicPOCLsbSPS[lt.LtIdxSPS]
			lt.UsedByCurrPic = sps.UsedByCurrPicLtSPS[lt.LtIdxSPS]
		} else {
			lt.POCLsb = r.u(sps.Log2MaxPOCLsb)
			lt.UsedByCurrPic = r.flag()
		}
		lt.DeltaPOCMsbPresent = r.flag()
		if lt.DeltaPOCMsbPresent {
			lt.DeltaPOCMsbCycleLt = r.ue()
		}
		lt.DeltaPOCMsbCycle = lt.DeltaPOCMsbCycleLt
		if i != 0 && i != int(h.NumLongTermSPS) {
			lt.DeltaPOCMsbCycle += h.LongTerm[i-1].DeltaPOCMsbCycle
		}
		if r.err != nil {
			return
		}
	}
}

// predWeightTable reads pred_weight_table() for a single-layer stream.
func (r *reader) predWeightTable(h *SliceHeader, sps *SPS, chroma bool) *PredWeightTable {
	t := &PredWeightTable{}
	t.LumaLog2WeightDenom = r.ueMax("luma_log2_weight_denom", 7)
	if chroma {
		t.DeltaChromaLog2WeightDenom = r.seRange("delta_chroma_log2_weight_denom", -7, 7)
	}
	chromaDenom := int32(t.LumaLog2WeightDenom) + t.DeltaChromaLog2WeightDenom
	if r.err == nil && (chromaDenom < 0 || chromaDenom > 7) {
		r.fail("ChromaLog2WeightDenom %d out of range", chromaDenom)
	}
	if r.err != nil {
		return t
	}
	// wpOffsetHalfRangeC (7-55)
	halfRange := int32(1) << 7
	if sps.Range.HighPrecisionOffsetsEnabled {
		halfRange = int32(1) << (sps.BitDepthChroma - 1)
	}
	list := func(entries []PredWeight, n int) {
		for i := 0; i < n; i++ {
			entries[i].LumaFlag = r.flag()
		}
		if chroma {
			for i := 0; i < n; i++ {
				entries[i].ChromaFlag = r.flag()
			}
		}
		for i := 0; i < n && r.err == nil; i++ {
			e := &entries[i]
			if e.LumaFlag {
				e.DeltaLumaWeight = r.seRange("delta_luma_weight", -128, 127)
				e.LumaOffset = r.se()
			}
			if e.ChromaFlag {
				for j := 0; j < 2; j++ {
					e.DeltaChromaWeight[j] = r.seRange("delta_chroma_weight", -128, 127)
					e.DeltaChromaOffset[j] = r.se()
					weight := (int32(1) << uint(chromaDenom)) + e.DeltaChromaWeight[j]
					off := halfRange + e.DeltaChromaOffset[j] - ((halfRange * weight) >> uint(chromaDenom))
					e.ChromaOffset[j] = min(max(off, -halfRange), halfRange-1)
				}
			}
		}
	}
	list(t.L0[:], int(h.NumRefIdxL0Active))
	if h.SliceType == SliceB {
		list(t.L1[:], int(h.NumRefIdxL1Active))
	}
	return t
}
