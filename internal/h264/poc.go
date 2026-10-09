package h264

// POCState carries the decoding-order state needed to derive the picture
// order count of successive pictures (8.2.1) without the rest of the DPB.
// It is what the display-order reorder layer uses.
type POCState struct {
	prevPOCMsb         int32
	prevPOCLsb         int32
	prevFrameNum       int32
	prevFrameNumOffset int32
	prevMMCO5          bool
	started            bool
}

// Reset forgets the previous pictures; the next picture must be an IDR.
func (s *POCState) Reset() { *s = POCState{} }

// Next derives the picture order count of the picture whose first slice is
// h and advances the state. A reference picture carrying
// memory_management_control_operation 5 resets the count: its own POC is
// reported relative to the reset (0 for a frame), as the following
// pictures are.
func (s *POCState) Next(sps *SPS, h *SliceHeader) int32 {
	var top, bottom int32
	ref := h.NALRefIdc != 0
	mmco5 := ref && !h.IDR && h.HasMMCO5()
	maxFrameNum := int32(1) << sps.Log2MaxFrameNum
	switch sps.PicOrderCntType {
	case 0:
		maxLsb := int32(1) << sps.Log2MaxPicOrderCntLsb
		lsb := int32(h.PicOrderCntLsb)
		if h.IDR || !s.started {
			s.prevPOCMsb, s.prevPOCLsb = 0, 0
		}
		prevLsb := s.prevPOCLsb
		var msb int32
		switch {
		case lsb < prevLsb && prevLsb-lsb >= maxLsb/2:
			msb = s.prevPOCMsb + maxLsb
		case lsb > prevLsb && lsb-prevLsb > maxLsb/2:
			msb = s.prevPOCMsb - maxLsb
		default:
			msb = s.prevPOCMsb
		}
		switch {
		case !h.FieldPic:
			top = msb + lsb
			bottom = top + h.DeltaPicOrderCntBottom
		case !h.BottomField:
			top = msb + lsb
			bottom = top
		default:
			bottom = msb + lsb
			top = bottom
		}
		if ref {
			s.prevPOCMsb, s.prevPOCLsb = msb, lsb
		}
	case 1, 2:
		var frameNumOffset int32
		switch {
		case h.IDR || !s.started:
			frameNumOffset = 0
		case s.prevFrameNum > int32(h.FrameNum):
			frameNumOffset = s.prevFrameNumOffset + maxFrameNum
		default:
			frameNumOffset = s.prevFrameNumOffset
		}
		if s.prevMMCO5 && !h.IDR {
			frameNumOffset = 0
			if s.prevFrameNum > int32(h.FrameNum) {
				frameNumOffset = maxFrameNum
			}
		}
		s.prevFrameNumOffset = frameNumOffset
		if sps.PicOrderCntType == 1 {
			cycle := int32(len(sps.OffsetForRefFrame))
			var absFrameNum int32
			if cycle != 0 {
				absFrameNum = frameNumOffset + int32(h.FrameNum)
			}
			if !ref && absFrameNum > 0 {
				absFrameNum--
			}
			var expected int32
			if absFrameNum > 0 {
				cycleCnt := (absFrameNum - 1) / cycle
				inCycle := (absFrameNum - 1) % cycle
				var perCycle int32
				for _, o := range sps.OffsetForRefFrame {
					perCycle += o
				}
				expected = cycleCnt * perCycle
				for i := int32(0); i <= inCycle; i++ {
					expected += sps.OffsetForRefFrame[i]
				}
			}
			if !ref {
				expected += sps.OffsetForNonRefPic
			}
			switch {
			case !h.FieldPic:
				top = expected + h.DeltaPicOrderCnt[0]
				bottom = top + sps.OffsetForTopToBottomField + h.DeltaPicOrderCnt[1]
			case !h.BottomField:
				top = expected + h.DeltaPicOrderCnt[0]
				bottom = top
			default:
				bottom = expected + sps.OffsetForTopToBottomField + h.DeltaPicOrderCnt[0]
				top = bottom
			}
		} else {
			var temp int32
			if !h.IDR {
				temp = 2 * (frameNumOffset + int32(h.FrameNum))
				if !ref {
					temp--
				}
			}
			top, bottom = temp, temp
		}
	}
	s.prevFrameNum = int32(h.FrameNum)
	s.prevMMCO5 = false
	s.started = true
	poc := top
	if bottom < top {
		poc = bottom
	}
	if mmco5 {
		// 8.2.1: after the marking the picture counts as POC 0 (frame) and
		// frame_num 0, and the next picture's prediction starts from it.
		top -= poc
		bottom -= poc
		s.prevPOCMsb, s.prevPOCLsb = 0, top
		if h.FieldPic && h.BottomField {
			s.prevPOCLsb = 0
		}
		s.prevFrameNum = 0
		s.prevFrameNumOffset = 0
		s.prevMMCO5 = true
		poc = top
		if bottom < top {
			poc = bottom
		}
	}
	return poc
}

// ConstraintSet3 reports constraint_set3_flag.
func (s *SPS) ConstraintSet3() bool { return s.ConstraintSet(3) }

// maxDpbMbs maps level_idc to MaxDpbMbs (Table A-1). Level 1b shares the
// value of level 1.
var maxDpbMbs = map[uint8]int{
	9: 396, 10: 396, 11: 900, 12: 2376, 13: 2376, 20: 2376, 21: 4752, 22: 8100,
	30: 8100, 31: 18000, 32: 20480, 40: 32768, 41: 32768, 42: 34816,
	50: 110400, 51: 184320, 52: 184320, 60: 696320, 61: 696320, 62: 696320,
}

// MaxReorderFrames returns how many frames may precede a frame in decoding
// order and follow it in output order: max_num_reorder_frames when the VUI
// carries it, 0 when the stream cannot reorder (pic_order_cnt_type 2,
// intra-only, or a constrained profile), and otherwise the DPB size the
// level allows for the picture size (A.3.1 and E.2.1).
func (s *SPS) MaxReorderFrames() int {
	if s.VUIPresent && s.VUI.BitstreamRestriction {
		n := int(s.VUI.MaxNumReorderFrames)
		if dpb := int(s.VUI.MaxDecFrameBuffering); dpb < n {
			n = dpb
		}
		if n > 16 {
			n = 16
		}
		return n
	}
	if s.PicOrderCntType == 2 || s.MaxNumRefFrames == 0 {
		return 0
	}
	switch s.ProfileIDC {
	case 44, 86, 100, 110, 122, 244:
		if s.ConstraintSet3() {
			return 0
		}
	}
	level := s.LevelIDC
	if level == 11 && s.ConstraintSet3() && (s.ProfileIDC == 66 || s.ProfileIDC == 77 || s.ProfileIDC == 88) {
		level = 9 // level 1b
	}
	mbs, ok := maxDpbMbs[level]
	if !ok {
		return 16
	}
	frameMbs := int(s.PicWidthInMbs) * s.FrameHeightInMbs()
	if frameMbs <= 0 {
		return 16
	}
	n := mbs / frameMbs
	if n > 16 {
		n = 16
	}
	if n < 1 {
		n = 1
	}
	return n
}
