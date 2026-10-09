package av1

import (
	"fmt"
	"math/bits"

	"github.com/shibukawa/hwmediacodec/internal/bitstream"
)

// Sizes and special values of the frame header (Section 3).
const (
	NumRefFrames      = 8 // reference frame slots
	RefsPerFrame      = 7 // references an inter frame names (LAST .. ALTREF)
	TotalRefsPerFrame = 8 // INTRA plus RefsPerFrame
	PrimaryRefNone    = 7
	MaxSegments       = 8
	SegLvlMax         = 8
	MaxTileCols       = 64
	MaxTileRows       = 64
	SuperresNum       = 8 // SuperresDenom of a frame without super-resolution

	superresDenomMin    = 9
	superresDenomBits   = 3
	maxTileWidth        = 4096
	maxTileArea         = 4096 * 2304
	maxLoopFilter       = 63
	warpedModelPrecBits = 16
	allFrames           = 1<<NumRefFrames - 1
)

// Reference frame names; RefFrameIdx, the global motion arrays and
// SkipModeFrame are indexed with them (RefFrameIdx with name - RefLast).
const (
	RefIntra = iota
	RefLast
	RefLast2
	RefLast3
	RefGolden
	RefBwdref
	RefAltref2
	RefAltref
)

// InterpolationFilterSwitchable is the InterpolationFilter of a frame whose
// blocks choose their own filter.
const InterpolationFilterSwitchable = 4

// Transform modes (TxMode).
const (
	TxModeOnly4x4 = iota
	TxModeLargest
	TxModeSelect
)

// Frame restoration types (FrameRestorationType), the values lr_type maps
// to.
const (
	RestoreNone = iota
	RestoreWiener
	RestoreSgrproj
	RestoreSwitchable
)

// Global motion types.
const (
	WarpIdentity = iota
	WarpTranslation
	WarpRotzoom
	WarpAffine
)

var (
	segmentationFeatureBits   = [SegLvlMax]int{8, 6, 6, 6, 6, 3, 0, 0}
	segmentationFeatureSigned = [SegLvlMax]bool{true, true, true, true, true, false, false, false}
	segmentationFeatureMax    = [SegLvlMax]int32{255, maxLoopFilter, maxLoopFilter, maxLoopFilter, maxLoopFilter, 7, 0, 0}

	defaultLoopFilterRefDeltas = [TotalRefsPerFrame]int8{1, 0, 0, 0, -1, 0, -1, -1}
	remapLrType                = [4]uint8{RestoreNone, RestoreSwitchable, RestoreWiener, RestoreSgrproj}
)

// defaultGmParams is the identity warp every reference starts from.
func defaultGmParams() (p [TotalRefsPerFrame][6]int32) {
	for ref := range p {
		p[ref][2] = 1 << warpedModelPrecBits
		p[ref][5] = 1 << warpedModelPrecBits
	}
	return p
}

// FilmGrain holds film_grain_params (Section 5.9.30). After a frame header
// is parsed it holds the parameters in effect for the frame, including
// those loaded from a reference frame (update_grain = 0, and
// show_existing_frame).
type FilmGrain struct {
	ApplyGrain            bool
	GrainSeed             uint16
	UpdateGrain           bool
	NumYPoints            uint8
	PointYValue           [14]uint8
	PointYScaling         [14]uint8
	ChromaScalingFromLuma bool
	NumCbPoints           uint8
	PointCbValue          [10]uint8
	PointCbScaling        [10]uint8
	NumCrPoints           uint8
	PointCrValue          [10]uint8
	PointCrScaling        [10]uint8
	GrainScalingMinus8    uint8
	ARCoeffLag            uint8
	ARCoeffsYPlus128      [24]uint8
	ARCoeffsCbPlus128     [25]uint8
	ARCoeffsCrPlus128     [25]uint8
	ARCoeffShiftMinus6    uint8
	GrainScaleShift       uint8
	CbMult                uint8
	CbLumaMult            uint8
	CbOffset              uint16
	CrMult                uint8
	CrLumaMult            uint8
	CrOffset              uint16
	OverlapFlag           bool
	ClipToRestrictedRange bool
}

// Header is a parsed uncompressed_header (Section 5.9.2) together with the
// values the decoding process derives from it and from the reference frame
// state: everything a slice-level hardware decoder is told about a frame.
type Header struct {
	ShowExistingFrame bool
	FrameToShowMapIdx uint8 // valid when ShowExistingFrame

	FrameType           FrameType
	ShowFrame           bool
	ShowableFrame       bool
	ErrorResilientMode  bool
	DisableCDFUpdate    bool
	AllowScreenContent  bool // allow_screen_content_tools
	ForceIntegerMV      bool
	CurrentFrameID      uint32
	FrameSizeOverride   bool
	OrderHint           uint8
	PrimaryRefFrame     uint8
	RefreshFrameFlags   uint8
	FrameRefsShort      bool // frame_refs_short_signaling
	RefFrameIdx         [RefsPerFrame]uint8
	AllowIntraBC        bool
	AllowHighPrecMV     bool
	InterpolationFilter uint8
	MotionModeSwitch    bool // is_motion_mode_switchable
	UseRefFrameMVs      bool
	DisableFrameEndCDF  bool // disable_frame_end_update_cdf
	AllowWarpedMotion   bool
	ReducedTxSet        bool

	// Frame size (Section 5.9.5 to 5.9.8). FrameWidth is the coded width,
	// UpscaledWidth the width after super-resolution, which is what a
	// decoder outputs.
	FrameWidth    int
	FrameHeight   int
	UpscaledWidth int
	RenderWidth   int
	RenderHeight  int
	UseSuperres   bool
	SuperresDenom uint8
	MiCols        int
	MiRows        int

	// Tile layout (Section 5.9.15). The start arrays hold TileCols+1 and
	// TileRows+1 positions in units of 4x4 luma samples; the *InSbsMinus1
	// arrays hold the tile sizes in superblocks.
	UniformTileSpacing  bool
	TileCols            int
	TileRows            int
	TileColsLog2        int
	TileRowsLog2        int
	MiColStarts         [MaxTileCols + 1]int
	MiRowStarts         [MaxTileRows + 1]int
	WidthInSbsMinus1    [MaxTileCols]uint16
	HeightInSbsMinus1   [MaxTileRows]uint16
	ContextUpdateTileID uint16
	TileSizeBytes       int

	// Quantisation (Section 5.9.12).
	BaseQIdx     uint8
	DeltaQYDc    int8
	DeltaQUDc    int8
	DeltaQUAc    int8
	DeltaQVDc    int8
	DeltaQVAc    int8
	UsingQMatrix bool
	QmY          uint8
	QmU          uint8
	QmV          uint8

	// Segmentation (Section 5.9.14). The feature arrays hold the values in
	// effect, including those carried over from the primary reference
	// frame.
	SegmentationEnabled        bool
	SegmentationUpdateMap      bool
	SegmentationTemporalUpdate bool
	SegmentationUpdateData     bool
	FeatureEnabled             [MaxSegments][SegLvlMax]bool
	FeatureData                [MaxSegments][SegLvlMax]int16

	DeltaQPresent  bool
	DeltaQRes      uint8
	DeltaLFPresent bool
	DeltaLFRes     uint8
	DeltaLFMulti   bool

	CodedLossless bool
	AllLossless   bool

	// Loop filter (Section 5.9.11); the deltas are those in effect.
	LoopFilterLevel        [4]uint8
	LoopFilterSharpness    uint8
	LoopFilterDeltaEnabled bool
	LoopFilterDeltaUpdate  bool
	LoopFilterRefDeltas    [TotalRefsPerFrame]int8
	LoopFilterModeDeltas   [2]int8

	// CDEF (Section 5.9.19). The secondary strengths are the two coded
	// bits, without the 3 -> 4 adjustment of the decoding process.
	CDEFDampingMinus3 uint8
	CDEFBits          uint8
	CDEFYPriStrength  [8]uint8
	CDEFYSecStrength  [8]uint8
	CDEFUVPriStrength [8]uint8
	CDEFUVSecStrength [8]uint8

	// Loop restoration (Section 5.9.20).
	FrameRestorationType [3]uint8
	LrUnitShift          uint8
	LrUVShift            uint8

	TxMode          uint8
	ReferenceSelect bool
	SkipModePresent bool
	SkipModeFrame   [2]uint8

	// Global motion (Section 5.9.24), indexed by reference frame name.
	// GmInvalid reports that the warp's shear parameters are not valid
	// (Section 7.11.3.6), so the decoder must not use the warp filter.
	GmType    [TotalRefsPerFrame]uint8
	GmParams  [TotalRefsPerFrame][6]int32
	GmInvalid [TotalRefsPerFrame]bool

	FilmGrain FilmGrain

	// HeaderBits is the length of the uncompressed header in bits. In a
	// FRAME OBU the tile group starts at the next byte boundary.
	HeaderBits int
}

// IsIntra reports whether the frame is a key frame or an intra-only frame.
func (h *Header) IsIntra() bool {
	return h.FrameType == KeyFrame || h.FrameType == IntraOnlyFrame
}

// NumTiles returns the number of tiles of the frame.
func (h *Header) NumTiles() int { return h.TileCols * h.TileRows }

// RefFrame is the state saved in one reference frame slot by the reference
// frame update process (Section 7.20).
type RefFrame struct {
	Valid         bool
	FrameID       uint32
	UpscaledWidth int
	FrameWidth    int
	FrameHeight   int
	RenderWidth   int
	RenderHeight  int
	FrameType     FrameType
	ShowableFrame bool
	OrderHint     uint8

	LoopFilterRefDeltas  [TotalRefsPerFrame]int8
	LoopFilterModeDeltas [2]int8
	FeatureEnabled       [MaxSegments][SegLvlMax]bool
	FeatureData          [MaxSegments][SegLvlMax]int16
	GmParams             [TotalRefsPerFrame][6]int32
	FilmGrain            FilmGrain
}

// State is the part of the decoder state that frame headers are parsed
// against: the sequence header and the reference frame slots.
type State struct {
	Seq *SequenceHeader
	Ref [NumRefFrames]RefFrame
}

// Reset empties the reference slots, as at the start of a stream.
func (s *State) Reset() {
	s.Ref = [NumRefFrames]RefFrame{}
}

// hdrReader reads the bit-level descriptors of Section 4.10 and keeps the
// first error.
type hdrReader struct {
	r   *bitstream.Reader
	err error
}

func (b *hdrReader) u(n int) uint32 {
	if b.err != nil {
		return 0
	}
	v, err := b.r.ReadBits(n)
	if err != nil {
		b.err = err
	}
	return uint32(v)
}

func (b *hdrReader) f() bool { return b.u(1) == 1 }

// su reads a signed n-bit value (su(n)).
func (b *hdrReader) su(n int) int32 {
	v := int32(b.u(n))
	if sign := int32(1) << (n - 1); v&sign != 0 {
		v -= 2 * sign
	}
	return v
}

// ns reads a non-symmetric unsigned value below n (ns(n)).
func (b *hdrReader) ns(n int) uint32 {
	if n <= 1 {
		return 0
	}
	w := bits.Len(uint(n))
	m := uint32(1)<<w - uint32(n)
	v := b.u(w - 1)
	if v < m {
		return v
	}
	return v<<1 - m + b.u(1)
}

// tileLog2 returns the smallest k with blkSize << k >= target.
func tileLog2(blkSize, target int) int {
	k := 0
	for blkSize<<k < target {
		k++
	}
	return k
}

// relativeDist is get_relative_dist (Section 7.8.3).
func (s *State) relativeDist(a, b int) int {
	if !s.Seq.EnableOrderHint {
		return 0
	}
	diff := a - b
	m := 1 << (s.Seq.OrderHintBits - 1)
	return (diff & (m - 1)) - (diff & m)
}

// ParseHeader parses the uncompressed header at the start of the payload of
// a FRAME_HEADER or FRAME OBU against the current state. temporalID and
// spatialID come from the OBU extension header (0 without one). The state
// is updated only where the syntax itself changes it (a shown key frame and
// ref_order_hint invalidate slots); call Update once the frame is decoded.
func (s *State) ParseHeader(payload []byte, temporalID, spatialID uint8) (*Header, error) {
	if s.Seq == nil {
		return nil, fmt.Errorf("%w: frame header before the sequence header", ErrInvalid)
	}
	b := &hdrReader{r: bitstream.New(payload)}
	h := &Header{}
	if err := s.uncompressedHeader(b, h, temporalID, spatialID); err != nil {
		return nil, err
	}
	if b.err != nil {
		return nil, fmt.Errorf("%w: frame header: %v", ErrInvalid, b.err)
	}
	h.HeaderBits = b.r.Pos()
	return h, nil
}

func (s *State) uncompressedHeader(b *hdrReader, h *Header, temporalID, spatialID uint8) error {
	seq := s.Seq
	idLen := 0
	if seq.FrameIDNumbersPresent {
		idLen = int(seq.AdditionalFrameIDLength) + int(seq.DeltaFrameIDLength)
	}
	temporalPointInfo := seq.DecoderModelInfoPresent && !seq.EqualPictureInterval

	if seq.ReducedStillPictureHeader {
		h.FrameType = KeyFrame
		h.ShowFrame = true
	} else {
		h.ShowExistingFrame = b.f()
		if h.ShowExistingFrame {
			h.FrameToShowMapIdx = uint8(b.u(3))
			if temporalPointInfo {
				b.u(int(seq.FramePresentationTimeLength))
			}
			if seq.FrameIDNumbersPresent {
				b.u(idLen) // display_frame_id
			}
			if b.err != nil {
				return nil
			}
			ref := &s.Ref[h.FrameToShowMapIdx]
			if !ref.Valid {
				return fmt.Errorf("%w: show_existing_frame names the empty slot %d", ErrInvalid, h.FrameToShowMapIdx)
			}
			h.FrameType = ref.FrameType
			h.ShowFrame = true
			h.OrderHint = ref.OrderHint
			h.CurrentFrameID = ref.FrameID
			h.FrameWidth, h.FrameHeight, h.UpscaledWidth = ref.FrameWidth, ref.FrameHeight, ref.UpscaledWidth
			h.RenderWidth, h.RenderHeight = ref.RenderWidth, ref.RenderHeight
			if h.FrameType == KeyFrame {
				h.RefreshFrameFlags = allFrames
			}
			if seq.FilmGrainParamsPresent {
				h.FilmGrain = ref.FilmGrain
			}
			return nil
		}
		h.FrameType = FrameType(b.u(2))
		h.ShowFrame = b.f()
		if h.ShowFrame && temporalPointInfo {
			b.u(int(seq.FramePresentationTimeLength))
		}
		if h.ShowFrame {
			h.ShowableFrame = h.FrameType != KeyFrame
		} else {
			h.ShowableFrame = b.f()
		}
		if h.FrameType == SwitchFrame || (h.FrameType == KeyFrame && h.ShowFrame) {
			h.ErrorResilientMode = true
		} else {
			h.ErrorResilientMode = b.f()
		}
	}
	if b.err != nil {
		return nil
	}
	intra := h.IsIntra()
	if h.FrameType == KeyFrame && h.ShowFrame {
		for i := range s.Ref {
			s.Ref[i].Valid = false
			s.Ref[i].OrderHint = 0
		}
	}
	h.DisableCDFUpdate = b.f()
	if seq.SeqForceScreenContentTools == SelectScreenContentTools {
		h.AllowScreenContent = b.f()
	} else {
		h.AllowScreenContent = seq.SeqForceScreenContentTools != 0
	}
	if h.AllowScreenContent {
		if seq.SeqForceIntegerMV == SelectIntegerMV {
			h.ForceIntegerMV = b.f()
		} else {
			h.ForceIntegerMV = seq.SeqForceIntegerMV != 0
		}
	}
	if intra {
		h.ForceIntegerMV = true
	}
	if seq.FrameIDNumbersPresent {
		h.CurrentFrameID = b.u(idLen)
		s.markRefFrames(h, idLen)
	}
	switch {
	case h.FrameType == SwitchFrame:
		h.FrameSizeOverride = true
	case seq.ReducedStillPictureHeader:
	default:
		h.FrameSizeOverride = b.f()
	}
	h.OrderHint = uint8(b.u(int(seq.OrderHintBits)))
	if intra || h.ErrorResilientMode {
		h.PrimaryRefFrame = PrimaryRefNone
	} else {
		h.PrimaryRefFrame = uint8(b.u(3))
	}
	if seq.DecoderModelInfoPresent && b.f() { // buffer_removal_time_present_flag
		for op, idc := range seq.OperatingPointIdcs {
			if !seq.DecoderModelPresentForOp[op] {
				continue
			}
			inTemporal := idc>>temporalID&1 != 0
			inSpatial := idc>>(spatialID+8)&1 != 0
			if idc == 0 || (inTemporal && inSpatial) {
				b.u(int(seq.BufferRemovalTimeLength))
			}
		}
	}
	if h.FrameType == SwitchFrame || (h.FrameType == KeyFrame && h.ShowFrame) {
		h.RefreshFrameFlags = allFrames
	} else {
		h.RefreshFrameFlags = uint8(b.u(8))
	}
	if (!intra || h.RefreshFrameFlags != allFrames) && h.ErrorResilientMode && seq.EnableOrderHint {
		for i := range s.Ref {
			hint := uint8(b.u(int(seq.OrderHintBits)))
			if b.err == nil && (hint != s.Ref[i].OrderHint || !s.Ref[i].Valid) {
				// The frame the encoder had in this slot is missing; what
				// remains is its order hint.
				s.Ref[i].Valid = false
				s.Ref[i].OrderHint = hint
			}
		}
	}
	if b.err != nil {
		return nil
	}

	if intra {
		s.frameSize(b, h)
		s.renderSize(b, h)
		if h.AllowScreenContent && h.UpscaledWidth == h.FrameWidth {
			h.AllowIntraBC = b.f()
		}
	} else {
		if seq.EnableOrderHint {
			h.FrameRefsShort = b.f()
			if h.FrameRefsShort {
				last := int(b.u(3))
				gold := int(b.u(3))
				s.setFrameRefs(h, last, gold)
			}
		}
		for i := range h.RefFrameIdx {
			if !h.FrameRefsShort {
				h.RefFrameIdx[i] = uint8(b.u(3))
			}
			if seq.FrameIDNumbersPresent {
				b.u(int(seq.DeltaFrameIDLength)) // delta_frame_id_minus_1
			}
		}
		if b.err != nil {
			return nil
		}
		if h.FrameSizeOverride && !h.ErrorResilientMode {
			s.frameSizeWithRefs(b, h)
		} else {
			s.frameSize(b, h)
			s.renderSize(b, h)
		}
		if !h.ForceIntegerMV {
			h.AllowHighPrecMV = b.f()
		}
		if b.f() { // is_filter_switchable
			h.InterpolationFilter = InterpolationFilterSwitchable
		} else {
			h.InterpolationFilter = uint8(b.u(2))
		}
		h.MotionModeSwitch = b.f()
		if !h.ErrorResilientMode && seq.EnableRefFrameMVs {
			h.UseRefFrameMVs = b.f()
		}
	}
	if b.err != nil {
		return nil
	}
	if h.FrameWidth <= 0 || h.FrameHeight <= 0 {
		return fmt.Errorf("%w: frame size %dx%d", ErrInvalid, h.FrameWidth, h.FrameHeight)
	}
	if seq.ReducedStillPictureHeader || h.DisableCDFUpdate {
		h.DisableFrameEndCDF = true
	} else {
		h.DisableFrameEndCDF = b.f()
	}

	// setup_past_independence or load_previous: the values carried over
	// from the primary reference frame.
	prevGm := defaultGmParams()
	h.LoopFilterRefDeltas = defaultLoopFilterRefDeltas
	var prev *RefFrame
	if h.PrimaryRefFrame != PrimaryRefNone {
		prev = &s.Ref[h.RefFrameIdx[h.PrimaryRefFrame]]
		prevGm = prev.GmParams
		h.LoopFilterRefDeltas = prev.LoopFilterRefDeltas
		h.LoopFilterModeDeltas = prev.LoopFilterModeDeltas
	}

	if err := s.tileInfo(b, h); err != nil {
		return err
	}
	s.quantizationParams(b, h)
	s.segmentationParams(b, h, prev)
	if h.BaseQIdx > 0 {
		h.DeltaQPresent = b.f()
	}
	if h.DeltaQPresent {
		h.DeltaQRes = uint8(b.u(2))
		if !h.AllowIntraBC {
			h.DeltaLFPresent = b.f()
		}
		if h.DeltaLFPresent {
			h.DeltaLFRes = uint8(b.u(2))
			h.DeltaLFMulti = b.f()
		}
	}
	h.CodedLossless = true
	for seg := 0; seg < MaxSegments; seg++ {
		qindex := int(h.BaseQIdx)
		if h.SegmentationEnabled && h.FeatureEnabled[seg][0] {
			qindex = min(max(qindex+int(h.FeatureData[seg][0]), 0), 255)
		}
		if qindex != 0 || h.DeltaQYDc != 0 || h.DeltaQUAc != 0 || h.DeltaQUDc != 0 || h.DeltaQVAc != 0 || h.DeltaQVDc != 0 {
			h.CodedLossless = false
		}
	}
	h.AllLossless = h.CodedLossless && h.FrameWidth == h.UpscaledWidth
	s.loopFilterParams(b, h)
	s.cdefParams(b, h)
	s.lrParams(b, h)
	if h.CodedLossless {
		h.TxMode = TxModeOnly4x4
	} else if b.f() { // tx_mode_select
		h.TxMode = TxModeSelect
	} else {
		h.TxMode = TxModeLargest
	}
	if !intra {
		h.ReferenceSelect = b.f()
	}
	if s.skipModeAllowed(h) {
		h.SkipModePresent = b.f()
	}
	if !intra && !h.ErrorResilientMode && seq.EnableWarpedMotion {
		h.AllowWarpedMotion = b.f()
	}
	h.ReducedTxSet = b.f()
	s.globalMotionParams(b, h, &prevGm)
	s.filmGrainParams(b, h)
	return nil
}

// markRefFrames invalidates the slots whose frame id is too far from the
// current one (Section 5.9.2, mark_ref_frames).
func (s *State) markRefFrames(h *Header, idLen int) {
	diffLen := uint(s.Seq.DeltaFrameIDLength)
	cur := h.CurrentFrameID
	for i := range s.Ref {
		id := s.Ref[i].FrameID
		if cur > 1<<diffLen {
			if id > cur || id < cur-1<<diffLen {
				s.Ref[i].Valid = false
			}
		} else if id > cur && id < 1<<uint(idLen)+cur-1<<diffLen {
			s.Ref[i].Valid = false
		}
	}
}

// setFrameRefs is the set frame refs process (Section 7.8): it derives the
// seven references from the two that are signalled.
func (s *State) setFrameRefs(h *Header, lastIdx, goldIdx int) {
	const (
		last   = RefLast - RefLast
		golden = RefGolden - RefLast
	)
	var idx [RefsPerFrame]int
	for i := range idx {
		idx[i] = -1
	}
	idx[last], idx[golden] = lastIdx, goldIdx
	var used [NumRefFrames]bool
	used[lastIdx], used[goldIdx] = true, true

	curFrameHint := 1 << (s.Seq.OrderHintBits - 1)
	var shifted [NumRefFrames]int
	for i := range s.Ref {
		shifted[i] = curFrameHint + s.relativeDist(int(s.Ref[i].OrderHint), int(h.OrderHint))
	}
	// find returns the unused slot that is backward (hint >= current) or
	// forward of the current frame and latest or earliest among those.
	find := func(backward, latest bool) int {
		ref, best := -1, 0
		for i, hint := range shifted {
			if used[i] || (hint >= curFrameHint) != backward {
				continue
			}
			if ref < 0 || (latest && hint >= best) || (!latest && hint < best) {
				ref, best = i, hint
			}
		}
		return ref
	}
	take := func(name, ref int) {
		if ref >= 0 {
			idx[name-RefLast] = ref
			used[ref] = true
		}
	}
	take(RefAltref, find(true, true))
	take(RefBwdref, find(true, false))
	take(RefAltref2, find(true, false))
	for _, name := range []int{RefLast2, RefLast3, RefBwdref, RefAltref2, RefAltref} {
		if idx[name-RefLast] < 0 {
			take(name, find(false, true))
		}
	}
	ref, earliest := -1, 0
	for i, hint := range shifted {
		if ref < 0 || hint < earliest {
			ref, earliest = i, hint
		}
	}
	for i := range idx {
		if idx[i] < 0 {
			idx[i] = ref
		}
		h.RefFrameIdx[i] = uint8(idx[i])
	}
}

func (s *State) frameSize(b *hdrReader, h *Header) {
	seq := s.Seq
	if h.FrameSizeOverride {
		h.FrameWidth = int(b.u(int(seq.FrameWidthBits))) + 1
		h.FrameHeight = int(b.u(int(seq.FrameHeightBits))) + 1
	} else {
		h.FrameWidth, h.FrameHeight = seq.MaxFrameWidth, seq.MaxFrameHeight
	}
	s.superresParams(b, h)
}

// superresParams also runs compute_image_size.
func (s *State) superresParams(b *hdrReader, h *Header) {
	h.SuperresDenom = SuperresNum
	if s.Seq.EnableSuperres {
		h.UseSuperres = b.f()
	}
	if h.UseSuperres {
		h.SuperresDenom = uint8(b.u(superresDenomBits)) + superresDenomMin
	}
	h.UpscaledWidth = h.FrameWidth
	h.FrameWidth = (h.UpscaledWidth*SuperresNum + int(h.SuperresDenom)/2) / int(h.SuperresDenom)
	h.MiCols = 2 * ((h.FrameWidth + 7) >> 3)
	h.MiRows = 2 * ((h.FrameHeight + 7) >> 3)
}

func (s *State) renderSize(b *hdrReader, h *Header) {
	if b.f() { // render_and_frame_size_different
		h.RenderWidth = int(b.u(16)) + 1
		h.RenderHeight = int(b.u(16)) + 1
	} else {
		h.RenderWidth, h.RenderHeight = h.UpscaledWidth, h.FrameHeight
	}
}

func (s *State) frameSizeWithRefs(b *hdrReader, h *Header) {
	for i := range h.RefFrameIdx {
		if !b.f() { // found_ref
			continue
		}
		ref := &s.Ref[h.RefFrameIdx[i]]
		h.FrameWidth, h.FrameHeight = ref.UpscaledWidth, ref.FrameHeight
		h.RenderWidth, h.RenderHeight = ref.RenderWidth, ref.RenderHeight
		s.superresParams(b, h)
		return
	}
	s.frameSize(b, h)
	s.renderSize(b, h)
}

func (s *State) tileInfo(b *hdrReader, h *Header) error {
	sbShift := 4
	if s.Seq.Use128x128Superblock {
		sbShift = 5
	}
	sbCols := (h.MiCols + 1<<sbShift - 1) >> sbShift
	sbRows := (h.MiRows + 1<<sbShift - 1) >> sbShift
	sbSize := sbShift + 2
	maxTileWidthSb := maxTileWidth >> sbSize
	maxTileAreaSb := maxTileArea >> (2 * sbSize)
	minLog2TileCols := tileLog2(maxTileWidthSb, sbCols)
	maxLog2TileCols := tileLog2(1, min(sbCols, MaxTileCols))
	maxLog2TileRows := tileLog2(1, min(sbRows, MaxTileRows))
	minLog2Tiles := max(minLog2TileCols, tileLog2(maxTileAreaSb, sbRows*sbCols))

	h.UniformTileSpacing = b.f()
	if h.UniformTileSpacing {
		h.TileColsLog2 = minLog2TileCols
		for h.TileColsLog2 < maxLog2TileCols && b.f() {
			h.TileColsLog2++
		}
		tileWidthSb := (sbCols + 1<<h.TileColsLog2 - 1) >> h.TileColsLog2
		i := 0
		for startSb := 0; startSb < sbCols; startSb += tileWidthSb {
			if i >= MaxTileCols {
				return fmt.Errorf("%w: more than %d tile columns", ErrInvalid, MaxTileCols)
			}
			h.MiColStarts[i] = startSb << sbShift
			i++
		}
		h.MiColStarts[i] = h.MiCols
		h.TileCols = i

		h.TileRowsLog2 = max(minLog2Tiles-h.TileColsLog2, 0)
		for h.TileRowsLog2 < maxLog2TileRows && b.f() {
			h.TileRowsLog2++
		}
		tileHeightSb := (sbRows + 1<<h.TileRowsLog2 - 1) >> h.TileRowsLog2
		i = 0
		for startSb := 0; startSb < sbRows; startSb += tileHeightSb {
			if i >= MaxTileRows {
				return fmt.Errorf("%w: more than %d tile rows", ErrInvalid, MaxTileRows)
			}
			h.MiRowStarts[i] = startSb << sbShift
			i++
		}
		h.MiRowStarts[i] = h.MiRows
		h.TileRows = i
	} else {
		widestTileSb := 0
		i := 0
		for startSb := 0; startSb < sbCols; i++ {
			if i >= MaxTileCols || b.err != nil {
				return fmt.Errorf("%w: bad tile column layout", ErrInvalid)
			}
			h.MiColStarts[i] = startSb << sbShift
			sizeSb := int(b.ns(min(sbCols-startSb, maxTileWidthSb))) + 1
			widestTileSb = max(widestTileSb, sizeSb)
			startSb += sizeSb
		}
		h.MiColStarts[i] = h.MiCols
		h.TileCols = i
		h.TileColsLog2 = tileLog2(1, h.TileCols)

		if minLog2Tiles > 0 {
			maxTileAreaSb = (sbRows * sbCols) >> (minLog2Tiles + 1)
		} else {
			maxTileAreaSb = sbRows * sbCols
		}
		maxTileHeightSb := max(maxTileAreaSb/max(widestTileSb, 1), 1)
		i = 0
		for startSb := 0; startSb < sbRows; i++ {
			if i >= MaxTileRows || b.err != nil {
				return fmt.Errorf("%w: bad tile row layout", ErrInvalid)
			}
			h.MiRowStarts[i] = startSb << sbShift
			startSb += int(b.ns(min(sbRows-startSb, maxTileHeightSb))) + 1
		}
		h.MiRowStarts[i] = h.MiRows
		h.TileRows = i
		h.TileRowsLog2 = tileLog2(1, h.TileRows)
	}
	for i := 0; i < h.TileCols; i++ {
		end := min((h.MiColStarts[i+1]+1<<sbShift-1)>>sbShift, sbCols)
		h.WidthInSbsMinus1[i] = uint16(end - h.MiColStarts[i]>>sbShift - 1)
	}
	for i := 0; i < h.TileRows; i++ {
		end := min((h.MiRowStarts[i+1]+1<<sbShift-1)>>sbShift, sbRows)
		h.HeightInSbsMinus1[i] = uint16(end - h.MiRowStarts[i]>>sbShift - 1)
	}
	if h.TileColsLog2 > 0 || h.TileRowsLog2 > 0 {
		h.ContextUpdateTileID = uint16(b.u(h.TileRowsLog2 + h.TileColsLog2))
		h.TileSizeBytes = int(b.u(2)) + 1
	}
	return nil
}

func (s *State) numPlanes() int {
	if s.Seq.MonoChrome {
		return 1
	}
	return 3
}

func (b *hdrReader) deltaQ() int8 {
	if b.f() { // delta_coded
		return int8(b.su(7))
	}
	return 0
}

func (s *State) quantizationParams(b *hdrReader, h *Header) {
	h.BaseQIdx = uint8(b.u(8))
	h.DeltaQYDc = b.deltaQ()
	if s.numPlanes() > 1 {
		diffUV := s.Seq.SeparateUVDeltaQ && b.f()
		h.DeltaQUDc = b.deltaQ()
		h.DeltaQUAc = b.deltaQ()
		if diffUV {
			h.DeltaQVDc = b.deltaQ()
			h.DeltaQVAc = b.deltaQ()
		} else {
			h.DeltaQVDc, h.DeltaQVAc = h.DeltaQUDc, h.DeltaQUAc
		}
	}
	h.UsingQMatrix = b.f()
	if h.UsingQMatrix {
		h.QmY = uint8(b.u(4))
		h.QmU = uint8(b.u(4))
		if s.Seq.SeparateUVDeltaQ {
			h.QmV = uint8(b.u(4))
		} else {
			h.QmV = h.QmU
		}
	}
}

func (s *State) segmentationParams(b *hdrReader, h *Header, prev *RefFrame) {
	h.SegmentationEnabled = b.f()
	if !h.SegmentationEnabled {
		return
	}
	if h.PrimaryRefFrame == PrimaryRefNone {
		h.SegmentationUpdateMap = true
		h.SegmentationUpdateData = true
	} else {
		h.SegmentationUpdateMap = b.f()
		if h.SegmentationUpdateMap {
			h.SegmentationTemporalUpdate = b.f()
		}
		h.SegmentationUpdateData = b.f()
	}
	if !h.SegmentationUpdateData {
		if prev != nil {
			h.FeatureEnabled, h.FeatureData = prev.FeatureEnabled, prev.FeatureData
		}
		return
	}
	for i := 0; i < MaxSegments; i++ {
		for j := 0; j < SegLvlMax; j++ {
			if !b.f() { // feature_enabled
				continue
			}
			h.FeatureEnabled[i][j] = true
			n, limit := segmentationFeatureBits[j], segmentationFeatureMax[j]
			var v int32
			if segmentationFeatureSigned[j] {
				v = min(max(b.su(1+n), -limit), limit)
			} else {
				v = min(int32(b.u(n)), limit)
			}
			h.FeatureData[i][j] = int16(v)
		}
	}
}

func (s *State) loopFilterParams(b *hdrReader, h *Header) {
	if h.CodedLossless || h.AllowIntraBC {
		h.LoopFilterRefDeltas = defaultLoopFilterRefDeltas
		h.LoopFilterModeDeltas = [2]int8{}
		return
	}
	h.LoopFilterLevel[0] = uint8(b.u(6))
	h.LoopFilterLevel[1] = uint8(b.u(6))
	if s.numPlanes() > 1 && (h.LoopFilterLevel[0] != 0 || h.LoopFilterLevel[1] != 0) {
		h.LoopFilterLevel[2] = uint8(b.u(6))
		h.LoopFilterLevel[3] = uint8(b.u(6))
	}
	h.LoopFilterSharpness = uint8(b.u(3))
	h.LoopFilterDeltaEnabled = b.f()
	if h.LoopFilterDeltaEnabled {
		h.LoopFilterDeltaUpdate = b.f()
	}
	if !h.LoopFilterDeltaUpdate {
		return
	}
	for i := range h.LoopFilterRefDeltas {
		if b.f() { // update_ref_delta
			h.LoopFilterRefDeltas[i] = int8(b.su(7))
		}
	}
	for i := range h.LoopFilterModeDeltas {
		if b.f() { // update_mode_delta
			h.LoopFilterModeDeltas[i] = int8(b.su(7))
		}
	}
}

func (s *State) cdefParams(b *hdrReader, h *Header) {
	if h.CodedLossless || h.AllowIntraBC || !s.Seq.EnableCDEF {
		return
	}
	h.CDEFDampingMinus3 = uint8(b.u(2))
	h.CDEFBits = uint8(b.u(2))
	for i := 0; i < 1<<h.CDEFBits; i++ {
		h.CDEFYPriStrength[i] = uint8(b.u(4))
		h.CDEFYSecStrength[i] = uint8(b.u(2))
		if s.numPlanes() > 1 {
			h.CDEFUVPriStrength[i] = uint8(b.u(4))
			h.CDEFUVSecStrength[i] = uint8(b.u(2))
		}
	}
}

func (s *State) lrParams(b *hdrReader, h *Header) {
	if h.AllLossless || h.AllowIntraBC || !s.Seq.EnableRestoration {
		return
	}
	usesLr, usesChromaLr := false, false
	for i := 0; i < s.numPlanes(); i++ {
		h.FrameRestorationType[i] = remapLrType[b.u(2)]
		if h.FrameRestorationType[i] != RestoreNone {
			usesLr = true
			usesChromaLr = usesChromaLr || i > 0
		}
	}
	if !usesLr {
		return
	}
	if s.Seq.Use128x128Superblock {
		h.LrUnitShift = uint8(b.u(1)) + 1
	} else if b.f() {
		h.LrUnitShift = 1 + uint8(b.u(1))
	}
	if s.Seq.SubsamplingX == 1 && s.Seq.SubsamplingY == 1 && usesChromaLr {
		h.LrUVShift = uint8(b.u(1))
	}
}

// skipModeAllowed is the first part of skip_mode_params (Section 5.9.22);
// it also sets SkipModeFrame.
func (s *State) skipModeAllowed(h *Header) bool {
	if h.IsIntra() || !h.ReferenceSelect || !s.Seq.EnableOrderHint {
		return false
	}
	cur := int(h.OrderHint)
	hint := func(i int) int { return int(s.Ref[h.RefFrameIdx[i]].OrderHint) }
	forwardIdx, backwardIdx := -1, -1
	forwardHint, backwardHint := 0, 0
	for i := 0; i < RefsPerFrame; i++ {
		refHint := hint(i)
		switch d := s.relativeDist(refHint, cur); {
		case d < 0:
			if forwardIdx < 0 || s.relativeDist(refHint, forwardHint) > 0 {
				forwardIdx, forwardHint = i, refHint
			}
		case d > 0:
			if backwardIdx < 0 || s.relativeDist(refHint, backwardHint) < 0 {
				backwardIdx, backwardHint = i, refHint
			}
		}
	}
	if forwardIdx < 0 {
		return false
	}
	second := backwardIdx
	if backwardIdx < 0 {
		secondHint := 0
		for i := 0; i < RefsPerFrame; i++ {
			refHint := hint(i)
			if s.relativeDist(refHint, forwardHint) < 0 && (second < 0 || s.relativeDist(refHint, secondHint) > 0) {
				second, secondHint = i, refHint
			}
		}
		if second < 0 {
			return false
		}
	}
	h.SkipModeFrame[0] = uint8(RefLast + min(forwardIdx, second))
	h.SkipModeFrame[1] = uint8(RefLast + max(forwardIdx, second))
	return true
}

func (s *State) globalMotionParams(b *hdrReader, h *Header, prev *[TotalRefsPerFrame][6]int32) {
	h.GmParams = defaultGmParams()
	if h.IsIntra() {
		return
	}
	for ref := RefLast; ref <= RefAltref; ref++ {
		typ := uint8(WarpIdentity)
		if b.f() { // is_global
			switch {
			case b.f(): // is_rot_zoom
				typ = WarpRotzoom
			case b.f(): // is_translation
				typ = WarpTranslation
			default:
				typ = WarpAffine
			}
		}
		h.GmType[ref] = typ
		p := &h.GmParams[ref]
		if typ >= WarpRotzoom {
			p[2] = s.globalParam(b, h, typ, prev[ref][2], 2)
			p[3] = s.globalParam(b, h, typ, prev[ref][3], 3)
			if typ == WarpAffine {
				p[4] = s.globalParam(b, h, typ, prev[ref][4], 4)
				p[5] = s.globalParam(b, h, typ, prev[ref][5], 5)
			} else {
				p[4], p[5] = -p[3], p[2]
			}
		}
		if typ >= WarpTranslation {
			p[0] = s.globalParam(b, h, typ, prev[ref][0], 0)
			p[1] = s.globalParam(b, h, typ, prev[ref][1], 1)
		}
		h.GmInvalid[ref] = !shearParamsValid(p)
	}
}

// globalParam is read_global_param (Section 5.9.25).
func (s *State) globalParam(b *hdrReader, h *Header, typ uint8, prev int32, idx int) int32 {
	absBits, precBits := 12, 15 // GM_ABS_ALPHA_BITS, GM_ALPHA_PREC_BITS
	if idx < 2 {
		if typ == WarpTranslation {
			lowPrec := 0
			if !h.AllowHighPrecMV {
				lowPrec = 1
			}
			absBits, precBits = 9-lowPrec, 3-lowPrec // GM_ABS_TRANS_ONLY_BITS, GM_TRANS_ONLY_PREC_BITS
		} else {
			absBits, precBits = 12, 6 // GM_ABS_TRANS_BITS, GM_TRANS_PREC_BITS
		}
	}
	precDiff := warpedModelPrecBits - precBits
	var round, sub int32
	if idx%3 == 2 {
		round = 1 << warpedModelPrecBits
		sub = 1 << precBits
	}
	mx := int32(1) << absBits
	r := prev>>precDiff - sub
	return b.signedSubexpWithRef(-mx, mx+1, r)<<precDiff + round
}

func (b *hdrReader) signedSubexpWithRef(low, high, r int32) int32 {
	return b.unsignedSubexpWithRef(high-low, r-low) + low
}

func (b *hdrReader) unsignedSubexpWithRef(mx, r int32) int32 {
	v := b.subexp(mx)
	if r<<1 <= mx {
		return inverseRecenter(r, v)
	}
	return mx - 1 - inverseRecenter(mx-1-r, v)
}

func (b *hdrReader) subexp(numSyms int32) int32 {
	var i, mk int32
	const k = 3
	for b.err == nil {
		b2 := int32(k)
		if i > 0 {
			b2 = k + i - 1
		}
		a := int32(1) << b2
		if numSyms <= mk+3*a {
			return int32(b.ns(int(numSyms-mk))) + mk
		}
		if !b.f() { // subexp_more_bits
			return int32(b.u(int(b2))) + mk
		}
		i++
		mk += a
	}
	return 0
}

func inverseRecenter(r, v int32) int32 {
	switch {
	case v > 2*r:
		return v
	case v&1 != 0:
		return r - (v+1)>>1
	}
	return r + v>>1
}

func (s *State) filmGrainParams(b *hdrReader, h *Header) {
	seq := s.Seq
	if !seq.FilmGrainParamsPresent || (!h.ShowFrame && !h.ShowableFrame) {
		return
	}
	g := &h.FilmGrain
	g.ApplyGrain = b.f()
	if !g.ApplyGrain {
		return
	}
	g.GrainSeed = uint16(b.u(16))
	g.UpdateGrain = true
	if h.FrameType == InterFrame {
		g.UpdateGrain = b.f()
	}
	if !g.UpdateGrain {
		seed := g.GrainSeed
		*g = s.Ref[b.u(3)].FilmGrain // film_grain_params_ref_idx
		g.ApplyGrain = true
		g.UpdateGrain = false
		g.GrainSeed = seed
		return
	}
	g.NumYPoints = uint8(min(b.u(4), 14))
	for i := 0; i < int(g.NumYPoints); i++ {
		g.PointYValue[i] = uint8(b.u(8))
		g.PointYScaling[i] = uint8(b.u(8))
	}
	if !seq.MonoChrome {
		g.ChromaScalingFromLuma = b.f()
	}
	if !seq.MonoChrome && !g.ChromaScalingFromLuma && !(seq.SubsamplingX == 1 && seq.SubsamplingY == 1 && g.NumYPoints == 0) {
		g.NumCbPoints = uint8(min(b.u(4), 10))
		for i := 0; i < int(g.NumCbPoints); i++ {
			g.PointCbValue[i] = uint8(b.u(8))
			g.PointCbScaling[i] = uint8(b.u(8))
		}
		g.NumCrPoints = uint8(min(b.u(4), 10))
		for i := 0; i < int(g.NumCrPoints); i++ {
			g.PointCrValue[i] = uint8(b.u(8))
			g.PointCrScaling[i] = uint8(b.u(8))
		}
	}
	g.GrainScalingMinus8 = uint8(b.u(2))
	g.ARCoeffLag = uint8(b.u(2))
	numPosLuma := 2 * int(g.ARCoeffLag) * (int(g.ARCoeffLag) + 1)
	numPosChroma := numPosLuma
	if g.NumYPoints > 0 {
		numPosChroma = numPosLuma + 1
		for i := 0; i < numPosLuma; i++ {
			g.ARCoeffsYPlus128[i] = uint8(b.u(8))
		}
	}
	if g.ChromaScalingFromLuma || g.NumCbPoints > 0 {
		for i := 0; i < numPosChroma; i++ {
			g.ARCoeffsCbPlus128[i] = uint8(b.u(8))
		}
	}
	if g.ChromaScalingFromLuma || g.NumCrPoints > 0 {
		for i := 0; i < numPosChroma; i++ {
			g.ARCoeffsCrPlus128[i] = uint8(b.u(8))
		}
	}
	g.ARCoeffShiftMinus6 = uint8(b.u(2))
	g.GrainScaleShift = uint8(b.u(2))
	if g.NumCbPoints > 0 {
		g.CbMult = uint8(b.u(8))
		g.CbLumaMult = uint8(b.u(8))
		g.CbOffset = uint16(b.u(9))
	}
	if g.NumCrPoints > 0 {
		g.CrMult = uint8(b.u(8))
		g.CrLumaMult = uint8(b.u(8))
		g.CrOffset = uint16(b.u(9))
	}
	g.OverlapFlag = b.f()
	g.ClipToRestrictedRange = b.f()
}

// Update is the reference frame update process (Section 7.20): it saves the
// state of the frame described by h into the slots its refresh_frame_flags
// name. For a key frame shown with show_existing_frame, which restarts the
// stream from that frame, every slot takes the shown frame's state.
func (s *State) Update(h *Header) {
	var saved RefFrame
	if h.ShowExistingFrame {
		saved = s.Ref[h.FrameToShowMapIdx]
	} else {
		saved = RefFrame{
			Valid:                true,
			FrameID:              h.CurrentFrameID,
			UpscaledWidth:        h.UpscaledWidth,
			FrameWidth:           h.FrameWidth,
			FrameHeight:          h.FrameHeight,
			RenderWidth:          h.RenderWidth,
			RenderHeight:         h.RenderHeight,
			FrameType:            h.FrameType,
			ShowableFrame:        h.ShowableFrame,
			OrderHint:            h.OrderHint,
			LoopFilterRefDeltas:  h.LoopFilterRefDeltas,
			LoopFilterModeDeltas: h.LoopFilterModeDeltas,
			FeatureEnabled:       h.FeatureEnabled,
			FeatureData:          h.FeatureData,
			GmParams:             h.GmParams,
			FilmGrain:            h.FilmGrain,
		}
	}
	for i := range s.Ref {
		if h.RefreshFrameFlags>>uint(i)&1 != 0 {
			s.Ref[i] = saved
		}
	}
}

// divLut is Div_Lut of Section 7.11.3.7: divLut[i] = round(2^22 / (256 + i)).
var divLut = func() (t [257]int32) {
	for i := range t {
		d := int32(256 + i)
		t[i] = (1<<22 + d/2) / d
	}
	return t
}()

func roundTwoSigned(x int64, n uint) int64 {
	if n == 0 {
		return x
	}
	if x < 0 {
		return -((-x + 1<<(n-1)) >> n)
	}
	return (x + 1<<(n-1)) >> n
}

func clipInt16(v int64) int64 { return min(max(v, -32768), 32767) }

// shearParamsValid is the validity result of the shear setup process
// (Section 7.11.3.6) for a warp matrix.
func shearParamsValid(p *[6]int32) bool {
	const (
		divLutBits      = 8
		divLutPrecBits  = 14
		warpReduceBits  = 6
		warpedPrecision = 1 << warpedModelPrecBits
	)
	if p[2] <= 0 {
		return false
	}
	alpha := clipInt16(int64(p[2]) - warpedPrecision)
	beta := clipInt16(int64(p[3]))

	// resolve_divisor(p[2])
	d := uint32(p[2])
	shift := uint(bits.Len32(d) - 1)
	e := int64(d) - 1<<shift
	var f int64
	if shift > divLutBits {
		f = roundTwoSigned(e, shift-divLutBits)
	} else {
		f = e << (divLutBits - shift)
	}
	shift += divLutPrecBits
	div := int64(divLut[f])

	v := int64(p[4]) * warpedPrecision
	w := int64(p[3]) * int64(p[4])
	gamma := clipInt16(roundTwoSigned(v*div, shift))
	delta := clipInt16(int64(p[5]) - roundTwoSigned(w*div, shift) - warpedPrecision)

	reduce := func(x int64) int64 { return roundTwoSigned(x, warpReduceBits) << warpReduceBits }
	alpha, beta, gamma, delta = reduce(alpha), reduce(beta), reduce(gamma), reduce(delta)
	abs := func(x int64) int64 {
		if x < 0 {
			return -x
		}
		return x
	}
	return 4*abs(alpha)+7*abs(beta) < warpedPrecision && 4*abs(gamma)+4*abs(delta) < warpedPrecision
}

// Tile is one tile of a tile group: its position in the tile grid and the
// place of its coded data inside TileGroup.Data.
type Tile struct {
	Row, Col int
	Offset   int
	Size     int
}

// TileGroup is a parsed tile group (Section 5.11.1).
type TileGroup struct {
	Start, End int // tg_start, tg_end
	// Data is everything after the tile group header: the tile size fields
	// and the tile data, which is what Tile.Offset counts from.
	Data  []byte
	Tiles []Tile
}

// ParseTileGroup parses the tile group that begins at data: the payload of
// a TILE_GROUP OBU, or the part of a FRAME OBU that follows the frame
// header (payload[(h.HeaderBits+7)/8:]).
func (h *Header) ParseTileGroup(data []byte) (*TileGroup, error) {
	numTiles := h.NumTiles()
	if numTiles == 0 {
		return nil, fmt.Errorf("%w: tile group of a frame without tiles", ErrInvalid)
	}
	tg := &TileGroup{End: numTiles - 1}
	b := &hdrReader{r: bitstream.New(data)}
	if numTiles > 1 && b.f() { // tile_start_and_end_present_flag
		tileBits := h.TileColsLog2 + h.TileRowsLog2
		tg.Start = int(b.u(tileBits))
		tg.End = int(b.u(tileBits))
	}
	if b.err != nil {
		return nil, fmt.Errorf("%w: truncated tile group header", ErrInvalid)
	}
	if tg.Start > tg.End || tg.End >= numTiles {
		return nil, fmt.Errorf("%w: tile group covers tiles %d to %d of %d", ErrInvalid, tg.Start, tg.End, numTiles)
	}
	tg.Data = data[(b.r.Pos()+7)/8:]
	off := 0
	for n := tg.Start; n <= tg.End; n++ {
		size := len(tg.Data) - off
		if n < tg.End {
			if len(tg.Data)-off < h.TileSizeBytes {
				return nil, fmt.Errorf("%w: truncated size of tile %d", ErrInvalid, n)
			}
			size = 0
			for i := 0; i < h.TileSizeBytes; i++ {
				size |= int(tg.Data[off+i]) << (8 * uint(i))
			}
			size++
			off += h.TileSizeBytes
		}
		if size <= 0 || size > len(tg.Data)-off {
			return nil, fmt.Errorf("%w: tile %d has %d bytes, %d remain", ErrInvalid, n, size, len(tg.Data)-off)
		}
		tg.Tiles = append(tg.Tiles, Tile{Row: n / h.TileCols, Col: n % h.TileCols, Offset: off, Size: size})
		off += size
	}
	return tg, nil
}
