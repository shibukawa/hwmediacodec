package h264

import (
	"math/bits"

	"github.com/shibukawa/hwmediacodec/internal/bitstream"
)

func newRBSPReader(payload []byte) *bitstream.Reader { return bitstream.NewRBSP(payload) }

// SliceType is slice_type % 5 (Table 7-6).
type SliceType uint8

// Slice types.
const (
	SliceP  SliceType = 0
	SliceB  SliceType = 1
	SliceI  SliceType = 2
	SliceSP SliceType = 3
	SliceSI SliceType = 4
)

func (t SliceType) String() string {
	switch t {
	case SliceP:
		return "P"
	case SliceB:
		return "B"
	case SliceI:
		return "I"
	case SliceSP:
		return "SP"
	case SliceSI:
		return "SI"
	}
	return "?"
}

// IsIntra reports I or SI.
func (t SliceType) IsIntra() bool { return t == SliceI || t == SliceSI }

// RefPicListModification is one entry of ref_pic_list_modification()
// (7.3.3.1): Idc is modification_of_pic_nums_idc and Value is
// abs_diff_pic_num_minus1 (idc 0, 1) or long_term_pic_num (idc 2).
type RefPicListModification struct {
	Idc   uint32
	Value uint32
}

// MMCO is one memory_management_control_operation (7.3.3.3).
type MMCO struct {
	Op                        uint32
	DifferenceOfPicNumsMinus1 uint32 // ops 1 and 3
	LongTermPicNum            uint32 // op 2
	LongTermFrameIdx          uint32 // ops 3 and 6
	MaxLongTermFrameIdxPlus1  uint32 // op 4
}

// WeightEntry holds the explicit prediction weights for one reference index.
// When the flags are false the entries hold the inferred default values
// (2^denom and 0).
type WeightEntry struct {
	LumaWeightFlag   bool
	LumaWeight       int16
	LumaOffset       int16
	ChromaWeightFlag bool
	ChromaWeight     [2]int16
	ChromaOffset     [2]int16
}

// PredWeightTable mirrors pred_weight_table() (7.3.3.2).
type PredWeightTable struct {
	LumaLog2WeightDenom   uint32
	ChromaLog2WeightDenom uint32
	L0, L1                [maxRefIdxActive]WeightEntry
	// AnyLumaL0 etc. report whether any entry of the list carries an
	// explicit weight (what VA-API's per-list flags mean).
	AnyLumaL0, AnyChromaL0, AnyLumaL1, AnyChromaL1 bool
}

// SliceHeader mirrors slice_header() (7.3.3) with the num_ref_idx values
// resolved against the PPS defaults.
type SliceHeader struct {
	NALType   int
	NALRefIdc int
	IDR       bool

	FirstMbInSlice uint32
	SliceTypeRaw   uint32
	SliceType      SliceType
	PPSID          uint32
	ColourPlaneID  uint8
	FrameNum       uint32
	FieldPic       bool
	BottomField    bool
	IdrPicID       uint32

	PicOrderCntLsb         uint32
	DeltaPicOrderCntBottom int32
	DeltaPicOrderCnt       [2]int32
	RedundantPicCnt        uint32

	DirectSpatialMvPred     bool
	NumRefIdxActiveOverride bool
	NumRefIdxL0Active       uint32 // resolved; 0 for I slices
	NumRefIdxL1Active       uint32 // resolved; 0 unless B

	RefPicListModificationL0 []RefPicListModification
	RefPicListModificationL1 []RefPicListModification
	PredWeights              *PredWeightTable

	// dec_ref_pic_marking
	NoOutputOfPriorPics   bool
	LongTermReference     bool
	AdaptiveRefPicMarking bool
	MMCOs                 []MMCO

	CabacInitIdc               uint32
	SliceQpDelta               int32
	SpForSwitch                bool
	SliceQsDelta               int32
	DisableDeblockingFilterIdc uint32
	SliceAlphaC0OffsetDiv2     int32
	SliceBetaOffsetDiv2        int32
	SliceGroupChangeCycle      uint32

	// HeaderBits is the size of the NAL header plus slice_header() in bits,
	// counted after emulation prevention removal; slice_data() starts here.
	HeaderBits int
}

// HasMMCO5 reports whether the marking contains operation 5.
func (h *SliceHeader) HasMMCO5() bool {
	for _, m := range h.MMCOs {
		if m.Op == 5 {
			return true
		}
	}
	return false
}

// ParseSliceHeader parses the header of a slice NAL unit (types 1 and 5,
// including the NAL header byte) using the parameter sets in ps. It returns
// the header together with the SPS and PPS the slice refers to.
func ParseSliceHeader(nal []byte, ps *ParameterSets) (*SliceHeader, *SPS, *PPS, error) {
	refIdc, typ, ok := NALHeader(nal)
	if !ok || (typ != NALSlice && typ != NALSliceIDR) {
		return nil, nil, nil, syntaxErr("not a slice NAL unit")
	}
	if len(nal) < 2 {
		return nil, nil, nil, syntaxErr("slice NAL unit too short")
	}
	h := &SliceHeader{NALType: typ, NALRefIdc: refIdc, IDR: typ == NALSliceIDR}
	r := bitstream.NewRBSP(nal[1:])
	var err error
	if h.FirstMbInSlice, err = r.ReadUE(); err != nil {
		return nil, nil, nil, err
	}
	if h.SliceTypeRaw, err = r.ReadUE(); err != nil {
		return nil, nil, nil, err
	}
	if h.SliceTypeRaw > 9 {
		return nil, nil, nil, syntaxErr("slice_type %d out of range", h.SliceTypeRaw)
	}
	h.SliceType = SliceType(h.SliceTypeRaw % 5)
	if h.PPSID, err = r.ReadUE(); err != nil {
		return nil, nil, nil, err
	}
	sps, pps, err := ps.Lookup(h.PPSID)
	if err != nil {
		return nil, nil, nil, err
	}
	if sps.SeparateColourPlane {
		v, err := r.ReadBits(2)
		if err != nil {
			return nil, nil, nil, err
		}
		h.ColourPlaneID = uint8(v)
	}
	v, err := r.ReadBits(int(sps.Log2MaxFrameNum))
	if err != nil {
		return nil, nil, nil, err
	}
	h.FrameNum = uint32(v)
	if !sps.FrameMbsOnly {
		if h.FieldPic, err = r.ReadFlag(); err != nil {
			return nil, nil, nil, err
		}
		if h.FieldPic {
			if h.BottomField, err = r.ReadFlag(); err != nil {
				return nil, nil, nil, err
			}
		}
	}
	if h.IDR {
		if h.IdrPicID, err = r.ReadUE(); err != nil {
			return nil, nil, nil, err
		}
	}
	switch sps.PicOrderCntType {
	case 0:
		v, err := r.ReadBits(int(sps.Log2MaxPicOrderCntLsb))
		if err != nil {
			return nil, nil, nil, err
		}
		h.PicOrderCntLsb = uint32(v)
		if pps.BottomFieldPicOrderInFramePresent && !h.FieldPic {
			if h.DeltaPicOrderCntBottom, err = r.ReadSE(); err != nil {
				return nil, nil, nil, err
			}
		}
	case 1:
		if !sps.DeltaPicOrderAlwaysZero {
			if h.DeltaPicOrderCnt[0], err = r.ReadSE(); err != nil {
				return nil, nil, nil, err
			}
			if pps.BottomFieldPicOrderInFramePresent && !h.FieldPic {
				if h.DeltaPicOrderCnt[1], err = r.ReadSE(); err != nil {
					return nil, nil, nil, err
				}
			}
		}
	}
	if pps.RedundantPicCntPresent {
		if h.RedundantPicCnt, err = r.ReadUE(); err != nil {
			return nil, nil, nil, err
		}
	}
	if h.SliceType == SliceB {
		if h.DirectSpatialMvPred, err = r.ReadFlag(); err != nil {
			return nil, nil, nil, err
		}
	}
	if h.SliceType == SliceP || h.SliceType == SliceSP || h.SliceType == SliceB {
		h.NumRefIdxL0Active = pps.NumRefIdxL0DefaultActive
		if h.SliceType == SliceB {
			h.NumRefIdxL1Active = pps.NumRefIdxL1DefaultActive
		}
		if h.NumRefIdxActiveOverride, err = r.ReadFlag(); err != nil {
			return nil, nil, nil, err
		}
		if h.NumRefIdxActiveOverride {
			if h.NumRefIdxL0Active, err = r.ReadUE(); err != nil {
				return nil, nil, nil, err
			}
			h.NumRefIdxL0Active++
			if h.SliceType == SliceB {
				if h.NumRefIdxL1Active, err = r.ReadUE(); err != nil {
					return nil, nil, nil, err
				}
				h.NumRefIdxL1Active++
			}
		}
		limit := uint32(maxRefIdxActive)
		if !h.FieldPic {
			limit = maxRefIdxActive / 2
		}
		if h.NumRefIdxL0Active > limit || h.NumRefIdxL1Active > limit {
			return nil, nil, nil, syntaxErr("num_ref_idx_active out of range")
		}
	}
	// ref_pic_list_modification()
	if !h.SliceType.IsIntra() {
		if h.RefPicListModificationL0, err = parseRefPicListModification(r); err != nil {
			return nil, nil, nil, err
		}
	}
	if h.SliceType == SliceB {
		if h.RefPicListModificationL1, err = parseRefPicListModification(r); err != nil {
			return nil, nil, nil, err
		}
	}
	if (pps.WeightedPred && (h.SliceType == SliceP || h.SliceType == SliceSP)) ||
		(pps.WeightedBipredIdc == 1 && h.SliceType == SliceB) {
		h.PredWeights = &PredWeightTable{}
		if err := parsePredWeightTable(r, h, sps.ChromaArrayType()); err != nil {
			return nil, nil, nil, err
		}
	}
	if h.NALRefIdc != 0 {
		if err := parseDecRefPicMarking(r, h); err != nil {
			return nil, nil, nil, err
		}
	}
	if pps.EntropyCodingMode && !h.SliceType.IsIntra() {
		if h.CabacInitIdc, err = r.ReadUE(); err != nil {
			return nil, nil, nil, err
		}
		if h.CabacInitIdc > 2 {
			return nil, nil, nil, syntaxErr("cabac_init_idc %d out of range", h.CabacInitIdc)
		}
	}
	if h.SliceQpDelta, err = r.ReadSE(); err != nil {
		return nil, nil, nil, err
	}
	if h.SliceType == SliceSP || h.SliceType == SliceSI {
		if h.SliceType == SliceSP {
			if h.SpForSwitch, err = r.ReadFlag(); err != nil {
				return nil, nil, nil, err
			}
		}
		if h.SliceQsDelta, err = r.ReadSE(); err != nil {
			return nil, nil, nil, err
		}
	}
	if pps.DeblockingFilterControlPresent {
		if h.DisableDeblockingFilterIdc, err = r.ReadUE(); err != nil {
			return nil, nil, nil, err
		}
		if h.DisableDeblockingFilterIdc > 2 {
			return nil, nil, nil, syntaxErr("disable_deblocking_filter_idc %d out of range", h.DisableDeblockingFilterIdc)
		}
		if h.DisableDeblockingFilterIdc != 1 {
			if h.SliceAlphaC0OffsetDiv2, err = r.ReadSE(); err != nil {
				return nil, nil, nil, err
			}
			if h.SliceBetaOffsetDiv2, err = r.ReadSE(); err != nil {
				return nil, nil, nil, err
			}
		}
	}
	if pps.NumSliceGroups > 1 && pps.SliceGroupMapType >= 3 && pps.SliceGroupMapType <= 5 {
		picSizeInMapUnits := sps.PicWidthInMbs * sps.PicHeightInMapUnits
		n := bits.Len32(picSizeInMapUnits / pps.SliceGroupChangeRate) // Ceil(Log2(x + 1))
		v, err := r.ReadBits(n)
		if err != nil {
			return nil, nil, nil, err
		}
		h.SliceGroupChangeCycle = uint32(v)
	}
	h.HeaderBits = 8 + r.Pos()
	return h, sps, pps, nil
}

func parseRefPicListModification(r *bitstream.Reader) ([]RefPicListModification, error) {
	flag, err := r.ReadFlag()
	if err != nil || !flag {
		return nil, err
	}
	// Non-nil even when empty, so that a set flag survives a round trip.
	out := []RefPicListModification{}
	for {
		idc, err := r.ReadUE()
		if err != nil {
			return nil, err
		}
		if idc == 3 {
			return out, nil
		}
		if idc > 3 {
			return nil, syntaxErr("modification_of_pic_nums_idc %d out of range", idc)
		}
		v, err := r.ReadUE()
		if err != nil {
			return nil, err
		}
		out = append(out, RefPicListModification{Idc: idc, Value: v})
		if len(out) > maxRefIdxActive+1 {
			return nil, syntaxErr("too many ref_pic_list_modification entries")
		}
	}
}

func parsePredWeightTable(r *bitstream.Reader, h *SliceHeader, chromaArrayType uint32) error {
	w := h.PredWeights
	var err error
	if w.LumaLog2WeightDenom, err = r.ReadUE(); err != nil {
		return err
	}
	if w.LumaLog2WeightDenom > 7 {
		return syntaxErr("luma_log2_weight_denom %d out of range", w.LumaLog2WeightDenom)
	}
	if chromaArrayType != 0 {
		if w.ChromaLog2WeightDenom, err = r.ReadUE(); err != nil {
			return err
		}
		if w.ChromaLog2WeightDenom > 7 {
			return syntaxErr("chroma_log2_weight_denom %d out of range", w.ChromaLog2WeightDenom)
		}
	}
	parseList := func(list *[maxRefIdxActive]WeightEntry, n uint32, anyLuma, anyChroma *bool) error {
		for i := uint32(0); i < n; i++ {
			e := &list[i]
			e.LumaWeight = int16(1 << w.LumaLog2WeightDenom)
			e.ChromaWeight = [2]int16{int16(1 << w.ChromaLog2WeightDenom), int16(1 << w.ChromaLog2WeightDenom)}
			if e.LumaWeightFlag, err = r.ReadFlag(); err != nil {
				return err
			}
			if e.LumaWeightFlag {
				*anyLuma = true
				wt, err := r.ReadSE()
				if err != nil {
					return err
				}
				off, err := r.ReadSE()
				if err != nil {
					return err
				}
				if wt < -128 || wt > 127 || off < -128 || off > 127 {
					return syntaxErr("luma weight out of range")
				}
				e.LumaWeight, e.LumaOffset = int16(wt), int16(off)
			}
			if chromaArrayType != 0 {
				if e.ChromaWeightFlag, err = r.ReadFlag(); err != nil {
					return err
				}
				if e.ChromaWeightFlag {
					*anyChroma = true
					for j := 0; j < 2; j++ {
						wt, err := r.ReadSE()
						if err != nil {
							return err
						}
						off, err := r.ReadSE()
						if err != nil {
							return err
						}
						if wt < -128 || wt > 127 || off < -128 || off > 127 {
							return syntaxErr("chroma weight out of range")
						}
						e.ChromaWeight[j], e.ChromaOffset[j] = int16(wt), int16(off)
					}
				}
			}
		}
		return nil
	}
	if err := parseList(&w.L0, h.NumRefIdxL0Active, &w.AnyLumaL0, &w.AnyChromaL0); err != nil {
		return err
	}
	if h.SliceType == SliceB {
		if err := parseList(&w.L1, h.NumRefIdxL1Active, &w.AnyLumaL1, &w.AnyChromaL1); err != nil {
			return err
		}
	}
	return nil
}

func parseDecRefPicMarking(r *bitstream.Reader, h *SliceHeader) error {
	var err error
	if h.IDR {
		if h.NoOutputOfPriorPics, err = r.ReadFlag(); err != nil {
			return err
		}
		if h.LongTermReference, err = r.ReadFlag(); err != nil {
			return err
		}
		return nil
	}
	if h.AdaptiveRefPicMarking, err = r.ReadFlag(); err != nil {
		return err
	}
	if !h.AdaptiveRefPicMarking {
		return nil
	}
	for {
		var m MMCO
		if m.Op, err = r.ReadUE(); err != nil {
			return err
		}
		if m.Op == 0 {
			return nil
		}
		if m.Op > 6 {
			return syntaxErr("memory_management_control_operation %d out of range", m.Op)
		}
		if m.Op == 1 || m.Op == 3 {
			if m.DifferenceOfPicNumsMinus1, err = r.ReadUE(); err != nil {
				return err
			}
		}
		if m.Op == 2 {
			if m.LongTermPicNum, err = r.ReadUE(); err != nil {
				return err
			}
		}
		if m.Op == 3 || m.Op == 6 {
			if m.LongTermFrameIdx, err = r.ReadUE(); err != nil {
				return err
			}
		}
		if m.Op == 4 {
			if m.MaxLongTermFrameIdxPlus1, err = r.ReadUE(); err != nil {
				return err
			}
		}
		h.MMCOs = append(h.MMCOs, m)
		if len(h.MMCOs) > 66 {
			return syntaxErr("too many memory management control operations")
		}
	}
}
