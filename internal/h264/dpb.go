package h264

import (
	"errors"
	"sort"
)

// Errors reported by the DPB.
var (
	// ErrFieldCoding is returned for field (interlaced PAFF) pictures, which
	// this implementation does not handle.
	ErrFieldCoding = errors.New("h264: field pictures are not supported")
	// ErrNoKeyframe is returned when decoding is started on a non-IDR
	// picture.
	ErrNoKeyframe = errors.New("h264: stream does not start with an IDR picture")
	// ErrPictureInProgress is returned when Start is called twice without
	// Finish.
	ErrPictureInProgress = errors.New("h264: previous picture was not finished")
)

// Picture is one decoded frame tracked by the DPB.
type Picture struct {
	FrameNum int
	// FrameNumWrap and PicNum are valid for short-term references while a
	// picture is being decoded (8.2.4.1).
	FrameNumWrap int
	PicNum       int
	// LongTermFrameIdx and LongTermPicNum are valid for long-term references.
	LongTermFrameIdx int
	LongTermPicNum   int

	TopPOC    int
	BottomPOC int

	IDR         bool
	Reference   bool // nal_ref_idc != 0; stays true while the picture is in the DPB
	LongTerm    bool
	NonExisting bool // inferred for a frame_num gap (8.2.5.2); never output
	MMCO5       bool

	// Handle is the backend's storage for the picture (for example a VA
	// surface). The DPB only passes it around.
	Handle any

	pocMsb, pocLsb int
	frameNumOffset int
}

// POC is PicOrderCnt(frame) = Min(TopFieldOrderCnt, BottomFieldOrderCnt).
func (p *Picture) POC() int {
	if p.TopPOC < p.BottomPOC {
		return p.TopPOC
	}
	return p.BottomPOC
}

// Surfaces lets the DPB allocate storage for pictures it has to invent
// (frame_num gaps) and reports when any picture stops being a reference.
type Surfaces interface {
	// Allocate returns a handle for a non-existing frame.
	Allocate() (handle any, err error)
	// Release is called exactly once for every picture that was a reference
	// when it leaves the DPB (through marking, an IDR, or Reset).
	Release(p *Picture)
}

// DPB implements the decoded picture buffer bookkeeping of the decoding
// process for frames: picture order count (8.2.1), frame_num gaps (8.2.5.2),
// reference picture marking (8.2.5) and reference picture list construction
// (8.2.4). Output order is not managed here; pictures are handed back to the
// caller in decoding order.
type DPB struct {
	surfaces Surfaces
	refs     []*Picture
	cur      *Picture
	sps      *SPS

	maxLongTermFrameIdx int // -1 means "no long-term frame indices"
	initialized         bool

	prevPOCMsb, prevPOCLsb int // of the previous reference picture
	prevFrameNum           int
	prevFrameNumOffset     int
	prevRefFrameNum        int
	prevMMCO5              bool
}

// NewDPB returns an empty DPB. surfaces may be nil when frame_num gaps need
// no backend storage.
func NewDPB(surfaces Surfaces) *DPB {
	return &DPB{surfaces: surfaces, maxLongTermFrameIdx: -1}
}

// Refs returns the current reference pictures (short- and long-term,
// including non-existing frames) in insertion order.
func (d *DPB) Refs() []*Picture { return d.refs }

// Current returns the picture between Start and Finish, or nil.
func (d *DPB) Current() *Picture { return d.cur }

// Start begins decoding a picture whose first slice header is sh. handle is
// the backend storage for the new picture. It computes the picture order
// count, inserts frames for a frame_num gap, and assigns PicNum values to
// the references.
func (d *DPB) Start(sps *SPS, sh *SliceHeader, handle any) (*Picture, error) {
	if d.cur != nil {
		return nil, ErrPictureInProgress
	}
	if sh.FieldPic {
		return nil, ErrFieldCoding
	}
	cur := &Picture{FrameNum: int(sh.FrameNum), IDR: sh.IDR, Reference: sh.NALRefIdc != 0, Handle: handle}
	switch {
	case sh.IDR:
		d.removeAll()
		d.maxLongTermFrameIdx = -1
	case !d.initialized:
		return nil, ErrNoKeyframe
	default:
		maxFrameNum := sps.MaxFrameNum()
		if cur.FrameNum != d.prevRefFrameNum && cur.FrameNum != (d.prevRefFrameNum+1)%maxFrameNum {
			if err := d.fillFrameNumGap(sps, cur.FrameNum); err != nil {
				return nil, err
			}
		}
	}
	d.computePOC(sps, sh, cur)
	d.assignPicNums(sps, cur.FrameNum)
	d.cur = cur
	d.sps = sps
	return cur, nil
}

// fillFrameNumGap infers the "non-existing" frames of 8.2.5.2.
func (d *DPB) fillFrameNumGap(sps *SPS, frameNum int) error {
	maxFrameNum := sps.MaxFrameNum()
	unused := (d.prevRefFrameNum + 1) % maxFrameNum
	for unused != frameNum {
		p := &Picture{FrameNum: unused, Reference: true, NonExisting: true}
		if d.surfaces != nil {
			h, err := d.surfaces.Allocate()
			if err != nil {
				return err
			}
			p.Handle = h
		}
		// Keep the FrameNumOffset chain going for POC types 1 and 2.
		p.frameNumOffset = d.frameNumOffset(sps, false, unused)
		switch sps.PicOrderCntType {
		case 1:
			p.TopPOC = d.expectedPOC(sps, p.frameNumOffset, unused, true)
			p.BottomPOC = p.TopPOC + int(sps.OffsetForTopToBottomField)
		case 2:
			p.TopPOC = 2 * (p.frameNumOffset + unused)
			p.BottomPOC = p.TopPOC
		}
		d.slidingWindow(sps, unused)
		d.refs = append(d.refs, p)
		d.prevRefFrameNum = unused
		d.prevFrameNum = unused
		d.prevFrameNumOffset = p.frameNumOffset
		d.prevMMCO5 = false
		unused = (unused + 1) % maxFrameNum
	}
	return nil
}

// frameNumOffset derives FrameNumOffset (8.2.1.2, 8.2.1.3).
func (d *DPB) frameNumOffset(sps *SPS, idr bool, frameNum int) int {
	if idr {
		return 0
	}
	prev := d.prevFrameNumOffset
	if d.prevMMCO5 {
		prev = 0
	}
	if d.prevFrameNum > frameNum {
		return prev + sps.MaxFrameNum()
	}
	return prev
}

// expectedPOC derives expectedPicOrderCnt for pic_order_cnt_type 1.
func (d *DPB) expectedPOC(sps *SPS, frameNumOffset, frameNum int, reference bool) int {
	n := len(sps.OffsetForRefFrame)
	absFrameNum := 0
	if n != 0 {
		absFrameNum = frameNumOffset + frameNum
	}
	if !reference && absFrameNum > 0 {
		absFrameNum--
	}
	expected := 0
	if absFrameNum > 0 {
		cycleCnt := (absFrameNum - 1) / n
		inCycle := (absFrameNum - 1) % n
		delta := 0
		for _, o := range sps.OffsetForRefFrame {
			delta += int(o)
		}
		expected = cycleCnt * delta
		for i := 0; i <= inCycle; i++ {
			expected += int(sps.OffsetForRefFrame[i])
		}
	}
	if !reference {
		expected += int(sps.OffsetForNonRefPic)
	}
	return expected
}

// computePOC implements 8.2.1 for frames.
func (d *DPB) computePOC(sps *SPS, sh *SliceHeader, cur *Picture) {
	ref := sh.NALRefIdc != 0
	switch sps.PicOrderCntType {
	case 0:
		prevMsb, prevLsb := d.prevPOCMsb, d.prevPOCLsb
		if sh.IDR {
			prevMsb, prevLsb = 0, 0
		}
		maxLsb := sps.MaxPicOrderCntLsb()
		lsb := int(sh.PicOrderCntLsb)
		msb := prevMsb
		if lsb < prevLsb && prevLsb-lsb >= maxLsb/2 {
			msb = prevMsb + maxLsb
		} else if lsb > prevLsb && lsb-prevLsb > maxLsb/2 {
			msb = prevMsb - maxLsb
		}
		cur.pocMsb, cur.pocLsb = msb, lsb
		cur.TopPOC = msb + lsb
		cur.BottomPOC = cur.TopPOC + int(sh.DeltaPicOrderCntBottom)
	case 1:
		cur.frameNumOffset = d.frameNumOffset(sps, sh.IDR, cur.FrameNum)
		cur.TopPOC = d.expectedPOC(sps, cur.frameNumOffset, cur.FrameNum, ref) + int(sh.DeltaPicOrderCnt[0])
		cur.BottomPOC = cur.TopPOC + int(sps.OffsetForTopToBottomField) + int(sh.DeltaPicOrderCnt[1])
	case 2:
		cur.frameNumOffset = d.frameNumOffset(sps, sh.IDR, cur.FrameNum)
		switch {
		case sh.IDR:
			cur.TopPOC = 0
		case !ref:
			cur.TopPOC = 2*(cur.frameNumOffset+cur.FrameNum) - 1
		default:
			cur.TopPOC = 2 * (cur.frameNumOffset + cur.FrameNum)
		}
		cur.BottomPOC = cur.TopPOC
	}
}

// assignPicNums implements 8.2.4.1 for frames.
func (d *DPB) assignPicNums(sps *SPS, frameNum int) {
	maxFrameNum := sps.MaxFrameNum()
	for _, p := range d.refs {
		if p.LongTerm {
			p.LongTermPicNum = p.LongTermFrameIdx
			continue
		}
		p.FrameNumWrap = p.FrameNum
		if p.FrameNum > frameNum {
			p.FrameNumWrap -= maxFrameNum
		}
		p.PicNum = p.FrameNumWrap
	}
}

// slidingWindow implements 8.2.5.3 relative to the given frame_num.
func (d *DPB) slidingWindow(sps *SPS, frameNum int) {
	limit := int(sps.MaxNumRefFrames)
	if limit < 1 {
		limit = 1
	}
	maxFrameNum := sps.MaxFrameNum()
	for len(d.refs) >= limit {
		victim := -1
		minWrap := 0
		for i, p := range d.refs {
			if p.LongTerm {
				continue
			}
			wrap := p.FrameNum
			if p.FrameNum > frameNum {
				wrap -= maxFrameNum
			}
			if victim < 0 || wrap < minWrap {
				victim, minWrap = i, wrap
			}
		}
		if victim < 0 {
			return // only long-term references left
		}
		d.remove(victim)
	}
}

func (d *DPB) remove(i int) {
	p := d.refs[i]
	d.refs = append(d.refs[:i], d.refs[i+1:]...)
	p.Reference = false
	if d.surfaces != nil {
		d.surfaces.Release(p)
	}
}

func (d *DPB) removeAll() {
	for len(d.refs) > 0 {
		d.remove(len(d.refs) - 1)
	}
}

func (d *DPB) findShortTerm(picNum int) int {
	for i, p := range d.refs {
		if !p.LongTerm && p.PicNum == picNum {
			return i
		}
	}
	return -1
}

func (d *DPB) findLongTerm(longTermPicNum int) int {
	for i, p := range d.refs {
		if p.LongTerm && p.LongTermPicNum == longTermPicNum {
			return i
		}
	}
	return -1
}

func (d *DPB) findLongTermIdx(idx int) int {
	for i, p := range d.refs {
		if p.LongTerm && p.LongTermFrameIdx == idx {
			return i
		}
	}
	return -1
}

// applyMMCO implements 8.2.5.4 for frames.
func (d *DPB) applyMMCO(sps *SPS, sh *SliceHeader, cur *Picture) {
	for _, m := range sh.MMCOs {
		switch m.Op {
		case 1:
			picNumX := cur.FrameNum - (int(m.DifferenceOfPicNumsMinus1) + 1)
			if i := d.findShortTerm(picNumX); i >= 0 {
				d.remove(i)
			}
		case 2:
			if i := d.findLongTerm(int(m.LongTermPicNum)); i >= 0 {
				d.remove(i)
			}
		case 3:
			picNumX := cur.FrameNum - (int(m.DifferenceOfPicNumsMinus1) + 1)
			idx := int(m.LongTermFrameIdx)
			if j := d.findLongTermIdx(idx); j >= 0 && d.refs[j].PicNum != picNumX {
				d.remove(j)
			}
			if i := d.findShortTerm(picNumX); i >= 0 {
				p := d.refs[i]
				p.LongTerm = true
				p.LongTermFrameIdx = idx
				p.LongTermPicNum = idx
			}
		case 4:
			d.maxLongTermFrameIdx = int(m.MaxLongTermFrameIdxPlus1) - 1
			for i := 0; i < len(d.refs); {
				if p := d.refs[i]; p.LongTerm && p.LongTermFrameIdx > d.maxLongTermFrameIdx {
					d.remove(i)
					continue
				}
				i++
			}
		case 5:
			d.removeAll()
			d.maxLongTermFrameIdx = -1
			cur.MMCO5 = true
		case 6:
			idx := int(m.LongTermFrameIdx)
			if j := d.findLongTermIdx(idx); j >= 0 {
				d.remove(j)
			}
			cur.LongTerm = true
			cur.LongTermFrameIdx = idx
			cur.LongTermPicNum = idx
		}
	}
}

// Finish completes the current picture: reference marking (8.2.5) and the
// state carried to the next picture. sh is the (first) slice header of the
// picture.
func (d *DPB) Finish(sh *SliceHeader) error {
	cur := d.cur
	if cur == nil {
		return errors.New("h264: no picture in progress")
	}
	sps := d.sps
	d.cur = nil
	if cur.Reference {
		switch {
		case sh.IDR:
			if sh.LongTermReference {
				cur.LongTerm = true
				cur.LongTermFrameIdx = 0
				cur.LongTermPicNum = 0
				d.maxLongTermFrameIdx = 0
			} else {
				d.maxLongTermFrameIdx = -1
			}
		case sh.AdaptiveRefPicMarking:
			d.applyMMCO(sps, sh, cur)
			// A conforming stream never exceeds max_num_ref_frames after
			// adaptive marking; shed the oldest short-term frames if it does.
			limit := int(sps.MaxNumRefFrames)
			if limit < 1 {
				limit = 1
			}
			for len(d.refs) >= limit {
				n := len(d.refs)
				d.slidingWindow(sps, cur.FrameNum)
				if len(d.refs) == n {
					break
				}
			}
		default:
			d.slidingWindow(sps, cur.FrameNum)
		}
		if cur.MMCO5 {
			temp := cur.POC()
			cur.TopPOC -= temp
			cur.BottomPOC -= temp
			cur.FrameNum = 0
		}
		d.refs = append(d.refs, cur)
		if cur.MMCO5 {
			d.prevPOCMsb, d.prevPOCLsb = 0, cur.TopPOC
		} else {
			d.prevPOCMsb, d.prevPOCLsb = cur.pocMsb, cur.pocLsb
		}
		d.prevRefFrameNum = cur.FrameNum
	}
	d.prevFrameNum = cur.FrameNum
	d.prevFrameNumOffset = cur.frameNumOffset
	d.prevMMCO5 = cur.MMCO5
	d.initialized = true
	return nil
}

// Reset drops every reference (for a seek or flush). The next picture must
// be an IDR picture.
func (d *DPB) Reset() {
	if d.cur != nil {
		d.cur = nil
	}
	d.removeAll()
	d.maxLongTermFrameIdx = -1
	d.initialized = false
}

// RefPicLists builds RefPicList0 and RefPicList1 for a slice of the current
// picture (8.2.4.2 and 8.2.4.3). The lists have exactly NumRefIdxL0Active /
// NumRefIdxL1Active entries; an entry is nil when the stream refers to a
// picture that is not available ("no reference picture").
func (d *DPB) RefPicLists(sh *SliceHeader) (l0, l1 []*Picture) {
	if d.cur == nil || sh.SliceType.IsIntra() {
		return nil, nil
	}
	var short, long []*Picture
	for _, p := range d.refs {
		if p.LongTerm {
			long = append(long, p)
		} else {
			short = append(short, p)
		}
	}
	sort.SliceStable(long, func(i, j int) bool { return long[i].LongTermPicNum < long[j].LongTermPicNum })

	switch sh.SliceType {
	case SliceB:
		curPOC := d.cur.POC()
		var before, after []*Picture
		for _, p := range short {
			if p.POC() <= curPOC {
				before = append(before, p)
			} else {
				after = append(after, p)
			}
		}
		sort.SliceStable(before, func(i, j int) bool { return before[i].POC() > before[j].POC() })
		sort.SliceStable(after, func(i, j int) bool { return after[i].POC() < after[j].POC() })
		l0 = make([]*Picture, 0, len(short)+len(long))
		l0 = append(append(append(l0, before...), after...), long...)
		l1 = make([]*Picture, 0, len(short)+len(long))
		l1 = append(append(append(l1, after...), before...), long...)
		if len(l1) > 1 && samePictures(l0, l1) {
			l1[0], l1[1] = l1[1], l1[0]
		}
	default:
		sort.SliceStable(short, func(i, j int) bool { return short[i].PicNum > short[j].PicNum })
		l0 = append(append(make([]*Picture, 0, len(short)+len(long)), short...), long...)
	}

	l0 = d.modifyList(l0, int(sh.NumRefIdxL0Active), sh.RefPicListModificationL0)
	if sh.SliceType == SliceB {
		l1 = d.modifyList(l1, int(sh.NumRefIdxL1Active), sh.RefPicListModificationL1)
	} else {
		l1 = nil
	}
	return l0, l1
}

func samePictures(a, b []*Picture) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// modifyList truncates or pads the initial list to n entries and applies
// ref_pic_list_modification() (8.2.4.3) for frames.
func (d *DPB) modifyList(initial []*Picture, n int, mods []RefPicListModification) []*Picture {
	if n == 0 {
		return nil
	}
	tmp := make([]*Picture, n+1)
	copy(tmp, initial)
	if len(mods) == 0 {
		return tmp[:n]
	}
	maxPicNum := d.sps.MaxFrameNum()
	curPicNum := d.cur.FrameNum
	picNumPred := curPicNum
	refIdx := 0
	for _, m := range mods {
		if refIdx >= n {
			break
		}
		var target *Picture
		var matches func(*Picture) bool
		switch m.Idc {
		case 0, 1:
			absDiff := int(m.Value) + 1
			noWrap := picNumPred
			if m.Idc == 0 {
				noWrap -= absDiff
				if noWrap < 0 {
					noWrap += maxPicNum
				}
			} else {
				noWrap += absDiff
				if noWrap >= maxPicNum {
					noWrap -= maxPicNum
				}
			}
			picNumPred = noWrap
			picNum := noWrap
			if noWrap > curPicNum {
				picNum -= maxPicNum
			}
			if i := d.findShortTerm(picNum); i >= 0 {
				target = d.refs[i]
			}
			matches = func(p *Picture) bool { return !p.LongTerm && p.PicNum == picNum }
		case 2:
			ltpn := int(m.Value)
			if i := d.findLongTerm(ltpn); i >= 0 {
				target = d.refs[i]
			}
			matches = func(p *Picture) bool { return p.LongTerm && p.LongTermPicNum == ltpn }
		default:
			continue
		}
		for c := n; c > refIdx; c-- {
			tmp[c] = tmp[c-1]
		}
		tmp[refIdx] = target
		refIdx++
		nIdx := refIdx
		for c := refIdx; c <= n; c++ {
			if tmp[c] == nil || !matches(tmp[c]) {
				tmp[nIdx] = tmp[c]
				nIdx++
			}
		}
	}
	return tmp[:n]
}
