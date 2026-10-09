package h264

import (
	"testing"
)

type fakeSurfaces struct {
	next     int
	released []*Picture
}

func (f *fakeSurfaces) Allocate() (any, error) { f.next++; return f.next, nil }
func (f *fakeSurfaces) Release(p *Picture)     { f.released = append(f.released, p) }

func testSPS() *SPS {
	return &SPS{Log2MaxFrameNum: 4, PicOrderCntType: 0, Log2MaxPicOrderCntLsb: 6, MaxNumRefFrames: 3}
}

type pic struct {
	idr    bool
	ref    bool
	fn     uint32
	lsb    uint32
	typ    SliceType
	mmco   []MMCO
	ltRef  bool // long_term_reference_flag on IDR
	nRefL0 uint32
	nRefL1 uint32
	modL0  []RefPicListModification
	modL1  []RefPicListModification
}

func (p pic) header() *SliceHeader {
	h := &SliceHeader{IDR: p.idr, FrameNum: p.fn, PicOrderCntLsb: p.lsb, SliceType: p.typ, MMCOs: p.mmco, LongTermReference: p.ltRef,
		NumRefIdxL0Active: p.nRefL0, NumRefIdxL1Active: p.nRefL1, RefPicListModificationL0: p.modL0, RefPicListModificationL1: p.modL1}
	if p.ref {
		h.NALRefIdc = 1
	}
	if p.idr {
		h.NALType = NALSliceIDR
	} else {
		h.NALType = NALSlice
	}
	h.AdaptiveRefPicMarking = len(p.mmco) > 0
	return h
}

// decode runs one picture through the DPB and returns the picture and its
// reference lists (built before Finish).
func decode(t *testing.T, d *DPB, sps *SPS, p pic) (*Picture, []*Picture, []*Picture) {
	t.Helper()
	h := p.header()
	cur, err := d.Start(sps, h, int(p.fn))
	if err != nil {
		t.Fatalf("Start fn=%d: %v", p.fn, err)
	}
	l0, l1 := d.RefPicLists(h)
	if err := d.Finish(h); err != nil {
		t.Fatalf("Finish fn=%d: %v", p.fn, err)
	}
	return cur, l0, l1
}

func frameNums(l []*Picture) []int {
	out := make([]int, len(l))
	for i, p := range l {
		if p == nil {
			out[i] = -1
		} else {
			out[i] = p.FrameNum
		}
	}
	return out
}

func equalInts(a, b []int) bool {
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

func TestPListOrderAndSlidingWindow(t *testing.T) {
	sps := testSPS()
	fs := &fakeSurfaces{}
	d := NewDPB(fs)
	decode(t, d, sps, pic{idr: true, ref: true, fn: 0, lsb: 0, typ: SliceI})
	decode(t, d, sps, pic{ref: true, fn: 1, lsb: 2, typ: SliceP, nRefL0: 1})
	decode(t, d, sps, pic{ref: true, fn: 2, lsb: 4, typ: SliceP, nRefL0: 2})
	// Three references: PicNum descending.
	_, l0, _ := decode(t, d, sps, pic{ref: true, fn: 3, lsb: 6, typ: SliceP, nRefL0: 3})
	if got := frameNums(l0); !equalInts(got, []int{2, 1, 0}) {
		t.Fatalf("P list = %v, want [2 1 0]", got)
	}
	// max_num_ref_frames = 3: frame 0 was dropped by the sliding window.
	if got := frameNums(d.Refs()); !equalInts(got, []int{1, 2, 3}) {
		t.Fatalf("refs = %v, want [1 2 3]", got)
	}
	if len(fs.released) != 1 || fs.released[0].FrameNum != 0 {
		t.Fatalf("released = %v", frameNums(fs.released))
	}
	// A list longer than the number of references is padded with nil.
	_, l0, _ = decode(t, d, sps, pic{ref: false, fn: 4, lsb: 8, typ: SliceP, nRefL0: 5})
	if got := frameNums(l0); !equalInts(got, []int{3, 2, 1, -1, -1}) {
		t.Fatalf("padded P list = %v", got)
	}
	// Non-reference pictures do not enter the DPB.
	if got := frameNums(d.Refs()); !equalInts(got, []int{1, 2, 3}) {
		t.Fatalf("refs after non-ref = %v", got)
	}
}

func TestFrameNumWrap(t *testing.T) {
	sps := testSPS()
	d := NewDPB(nil)
	decode(t, d, sps, pic{idr: true, ref: true, fn: 0, typ: SliceI})
	// Jump to frame_num 13 with gaps: frames 1..12 are inferred, the
	// sliding window keeps the last three (10, 11, 12), then 13 replaces 10.
	decode(t, d, sps, pic{ref: true, fn: 13, lsb: 26, typ: SliceP, nRefL0: 1})
	decode(t, d, sps, pic{ref: true, fn: 14, lsb: 28, typ: SliceP, nRefL0: 1})
	decode(t, d, sps, pic{ref: true, fn: 15, lsb: 30, typ: SliceP, nRefL0: 1})
	// frame_num wraps to 0: FrameNumWrap of 15, 14, 13 is -1, -2, -3.
	_, l0, _ := decode(t, d, sps, pic{ref: true, fn: 0, lsb: 32, typ: SliceP, nRefL0: 3})
	if got := frameNums(l0); !equalInts(got, []int{15, 14, 13}) {
		t.Fatalf("P list across wrap = %v, want [15 14 13]", got)
	}
	if got := frameNums(d.Refs()); !equalInts(got, []int{14, 15, 0}) {
		t.Fatalf("refs = %v, want [14 15 0]", got)
	}
}

func TestFrameNumGapInsertsNonExistingFrames(t *testing.T) {
	sps := testSPS()
	sps.GapsInFrameNumAllowed = true
	fs := &fakeSurfaces{}
	d := NewDPB(fs)
	decode(t, d, sps, pic{idr: true, ref: true, fn: 0, typ: SliceI})
	decode(t, d, sps, pic{ref: true, fn: 3, lsb: 6, typ: SliceP, nRefL0: 1})
	refs := d.Refs()
	if got := frameNums(refs); !equalInts(got, []int{1, 2, 3}) {
		t.Fatalf("refs = %v, want [1 2 3]", got)
	}
	if !refs[0].NonExisting || !refs[1].NonExisting || refs[2].NonExisting {
		t.Fatal("frames 1 and 2 must be non-existing, 3 must not")
	}
	if fs.next != 2 {
		t.Fatalf("allocated %d surfaces for gap frames, want 2", fs.next)
	}
	if refs[0].Handle != 1 || refs[1].Handle != 2 {
		t.Fatalf("gap frame handles = %v %v", refs[0].Handle, refs[1].Handle)
	}
	// Frame 0 was pushed out by the window (limit 3) and released.
	if len(fs.released) != 1 || fs.released[0].FrameNum != 0 {
		t.Fatalf("released = %v", frameNums(fs.released))
	}
}

func TestBListOrder(t *testing.T) {
	sps := testSPS()
	sps.MaxNumRefFrames = 4
	d := NewDPB(nil)
	decode(t, d, sps, pic{idr: true, ref: true, fn: 0, lsb: 0, typ: SliceI})            // POC 0
	decode(t, d, sps, pic{ref: true, fn: 1, lsb: 8, typ: SliceP, nRefL0: 1})            // POC 8
	decode(t, d, sps, pic{ref: true, fn: 2, lsb: 4, typ: SliceB, nRefL0: 1, nRefL1: 1}) // POC 4
	// Current POC 6: before = {4, 0} descending, after = {8} ascending.
	_, l0, l1 := decode(t, d, sps, pic{ref: false, fn: 3, lsb: 6, typ: SliceB, nRefL0: 3, nRefL1: 3})
	if got := frameNums(l0); !equalInts(got, []int{2, 0, 1}) {
		t.Fatalf("B L0 = %v, want [2 0 1]", got)
	}
	if got := frameNums(l1); !equalInts(got, []int{1, 2, 0}) {
		t.Fatalf("B L1 = %v, want [1 2 0]", got)
	}
	// Current POC 10: every reference precedes it, so L1 would equal L0
	// and its first two entries are swapped.
	_, l0, l1 = decode(t, d, sps, pic{ref: false, fn: 3, lsb: 10, typ: SliceB, nRefL0: 3, nRefL1: 3})
	if got := frameNums(l0); !equalInts(got, []int{1, 2, 0}) {
		t.Fatalf("B L0 = %v, want [1 2 0]", got)
	}
	if got := frameNums(l1); !equalInts(got, []int{2, 1, 0}) {
		t.Fatalf("B L1 = %v, want [2 1 0]", got)
	}
	// With a single entry no swap happens.
	_, l0, l1 = decode(t, d, sps, pic{ref: false, fn: 3, lsb: 10, typ: SliceB, nRefL0: 1, nRefL1: 1})
	if got := frameNums(l0); !equalInts(got, []int{1}) {
		t.Fatalf("B L0 = %v", got)
	}
	if got := frameNums(l1); !equalInts(got, []int{2}) {
		t.Fatalf("B L1 = %v, want [2] (swap happens on the full initial list)", got)
	}
}

func TestRefPicListModification(t *testing.T) {
	sps := testSPS()
	sps.MaxNumRefFrames = 4
	d := NewDPB(nil)
	decode(t, d, sps, pic{idr: true, ref: true, fn: 0, typ: SliceI})
	decode(t, d, sps, pic{ref: true, fn: 1, lsb: 2, typ: SliceP, nRefL0: 1})
	decode(t, d, sps, pic{ref: true, fn: 2, lsb: 4, typ: SliceP, nRefL0: 1})
	decode(t, d, sps, pic{ref: true, fn: 3, lsb: 6, typ: SliceP, nRefL0: 1})
	// CurrPicNum 4. Initial L0 = [3 2 1 0].
	// idc 0, abs_diff_pic_num_minus1 = 2: picNum 4-3 = 1 -> [1 3 2 0]
	// idc 1, abs_diff_pic_num_minus1 = 1: pred 1 + 2 = 3 -> [1 3 2 0]
	// idc 0, abs_diff_pic_num_minus1 = 2: pred 3 - 3 = 0 -> [1 3 0 2]
	mods := []RefPicListModification{{Idc: 0, Value: 2}, {Idc: 1, Value: 1}, {Idc: 0, Value: 2}}
	_, l0, _ := decode(t, d, sps, pic{ref: true, fn: 4, lsb: 8, typ: SliceP, nRefL0: 4, modL0: mods})
	if got := frameNums(l0); !equalInts(got, []int{1, 3, 0, 2}) {
		t.Fatalf("modified L0 = %v, want [1 3 0 2]", got)
	}
	// A short list: modification can bring in a picture beyond the
	// truncated initial list. Initial [4 3] (n=2); idc 0 abs_diff 3 ->
	// picNum 5-4 = 1 -> [1 4].
	_, l0, _ = decode(t, d, sps, pic{ref: false, fn: 5, lsb: 10, typ: SliceP, nRefL0: 2, modL0: []RefPicListModification{{Idc: 0, Value: 3}}})
	if got := frameNums(l0); !equalInts(got, []int{1, 4}) {
		t.Fatalf("modified short L0 = %v, want [1 4]", got)
	}
	// Wrap-around: abs_diff larger than CurrPicNum wraps modulo MaxPicNum (16):
	// picNumNoWrap = 5 - 6 + 16 = 15 > CurrPicNum -> picNum = -1 (no such
	// picture) -> nil entry, rest of the list unchanged.
	_, l0, _ = decode(t, d, sps, pic{ref: false, fn: 5, lsb: 10, typ: SliceP, nRefL0: 2, modL0: []RefPicListModification{{Idc: 0, Value: 5}}})
	if got := frameNums(l0); !equalInts(got, []int{-1, 4}) {
		t.Fatalf("modified L0 with missing picture = %v, want [-1 4]", got)
	}
}

func TestLongTermReferences(t *testing.T) {
	sps := testSPS()
	sps.MaxNumRefFrames = 4
	fs := &fakeSurfaces{}
	d := NewDPB(fs)
	// IDR marked as long-term (LongTermFrameIdx 0).
	decode(t, d, sps, pic{idr: true, ref: true, fn: 0, typ: SliceI, ltRef: true})
	if r := d.Refs(); len(r) != 1 || !r[0].LongTerm || r[0].LongTermFrameIdx != 0 {
		t.Fatalf("after long-term IDR: %+v", r)
	}
	decode(t, d, sps, pic{ref: true, fn: 1, lsb: 2, typ: SliceP, nRefL0: 1})
	decode(t, d, sps, pic{ref: true, fn: 2, lsb: 4, typ: SliceP, nRefL0: 1})
	// P list: short-term by PicNum descending, then long-term.
	_, l0, _ := decode(t, d, sps, pic{ref: true, fn: 3, lsb: 6, typ: SliceP, nRefL0: 3})
	if got := frameNums(l0); !equalInts(got, []int{2, 1, 0}) {
		t.Fatalf("P list = %v, want [2 1 0] (long-term last)", got)
	}
	// Modification idc 2 selects the long-term picture by LongTermPicNum.
	_, l0, _ = decode(t, d, sps, pic{ref: false, fn: 4, lsb: 8, typ: SliceP, nRefL0: 2, modL0: []RefPicListModification{{Idc: 2, Value: 0}}})
	if got := frameNums(l0); !equalInts(got, []int{0, 3}) {
		t.Fatalf("L0 with long-term first = %v, want [0 3]", got)
	}
	// MMCO 3: frame 2 (picNumX = 4 - (1+1) = 2) becomes long-term idx 1.
	// MMCO 1: frame 1 (picNumX = 4 - (2+1) = 1) is dropped.
	decode(t, d, sps, pic{ref: true, fn: 4, lsb: 8, typ: SliceP, nRefL0: 1, mmco: []MMCO{
		{Op: 3, DifferenceOfPicNumsMinus1: 1, LongTermFrameIdx: 1},
		{Op: 1, DifferenceOfPicNumsMinus1: 2},
	}})
	refs := d.Refs()
	if got := frameNums(refs); !equalInts(got, []int{0, 2, 3, 4}) {
		t.Fatalf("refs = %v, want [0 2 3 4]", got)
	}
	if !refs[1].LongTerm || refs[1].LongTermFrameIdx != 1 || refs[2].LongTerm || refs[3].LongTerm {
		t.Fatalf("long-term flags wrong: %+v", refs)
	}
	// Long-term entries are ordered by LongTermPicNum.
	_, l0, _ = decode(t, d, sps, pic{ref: false, fn: 5, lsb: 10, typ: SliceP, nRefL0: 4})
	if got := frameNums(l0); !equalInts(got, []int{4, 3, 0, 2}) {
		t.Fatalf("P list = %v, want [4 3 0 2]", got)
	}
	// MMCO 2 removes long-term pic num 0; MMCO 6 makes the current picture
	// long-term idx 1, replacing frame 2.
	decode(t, d, sps, pic{ref: true, fn: 5, lsb: 10, typ: SliceP, nRefL0: 1, mmco: []MMCO{
		{Op: 2, LongTermPicNum: 0},
		{Op: 6, LongTermFrameIdx: 1},
	}})
	refs = d.Refs()
	if got := frameNums(refs); !equalInts(got, []int{3, 4, 5}) {
		t.Fatalf("refs = %v, want [3 4 5]", got)
	}
	if !refs[2].LongTerm || refs[2].LongTermFrameIdx != 1 {
		t.Fatalf("current picture should be long-term idx 1: %+v", refs[2])
	}
	// MMCO 4 with max_long_term_frame_idx_plus1 = 0 drops all long-term refs.
	decode(t, d, sps, pic{ref: true, fn: 6, lsb: 12, typ: SliceP, nRefL0: 1, mmco: []MMCO{{Op: 4, MaxLongTermFrameIdxPlus1: 0}}})
	if got := frameNums(d.Refs()); !equalInts(got, []int{3, 4, 6}) {
		t.Fatalf("refs after MMCO 4 = %v, want [3 4 6]", got)
	}
	// MMCO 5 empties the DPB, the picture's POC becomes 0 and its
	// frame_num counts as 0 for the following pictures.
	cur, _, _ := decode(t, d, sps, pic{ref: true, fn: 7, lsb: 40, typ: SliceP, nRefL0: 1, mmco: []MMCO{{Op: 5}}})
	if got := frameNums(d.Refs()); !equalInts(got, []int{0}) {
		t.Fatalf("refs after MMCO 5 = %v, want [0]", got)
	}
	if cur.POC() != 0 || cur.FrameNum != 0 {
		t.Fatalf("after MMCO 5: POC %d frame_num %d", cur.POC(), cur.FrameNum)
	}
	// Next picture: frame_num 1, POC lsb 2 relative to prevPicOrderCntLsb 0.
	next, l0, _ := decode(t, d, sps, pic{ref: true, fn: 1, lsb: 2, typ: SliceP, nRefL0: 1})
	if next.POC() != 2 {
		t.Fatalf("POC after MMCO 5 = %d, want 2", next.POC())
	}
	if got := frameNums(l0); !equalInts(got, []int{0}) {
		t.Fatalf("L0 after MMCO 5 = %v", got)
	}
	// Every picture that left the DPB was released exactly once.
	seen := map[*Picture]int{}
	for _, p := range fs.released {
		seen[p]++
		if seen[p] > 1 {
			t.Fatalf("picture fn=%d released twice", p.FrameNum)
		}
		if p.Reference {
			t.Fatalf("released picture fn=%d still marked as reference", p.FrameNum)
		}
	}
}

func TestPOCType0Wrap(t *testing.T) {
	sps := testSPS() // MaxPicOrderCntLsb 64
	d := NewDPB(nil)
	decode(t, d, sps, pic{idr: true, ref: true, fn: 0, lsb: 0, typ: SliceI})
	decode(t, d, sps, pic{ref: true, fn: 1, lsb: 30, typ: SliceP, nRefL0: 1})
	p, _, _ := decode(t, d, sps, pic{ref: true, fn: 2, lsb: 60, typ: SliceP, nRefL0: 1})
	if p.POC() != 60 {
		t.Fatalf("POC = %d, want 60", p.POC())
	}
	// lsb wraps from 60 to 2: the difference (58) is at least MaxLsb/2, so
	// the MSB advances by 64.
	p, _, _ = decode(t, d, sps, pic{ref: true, fn: 3, lsb: 2, typ: SliceP, nRefL0: 1})
	if p.POC() != 66 {
		t.Fatalf("POC after wrap = %d, want 66", p.POC())
	}
	// A B-frame before the wrap point resolves against the previous
	// reference picture (msb 64, lsb 2): 62 - 2 > 32, so the MSB steps back.
	p, _, _ = decode(t, d, sps, pic{ref: false, fn: 4, lsb: 62, typ: SliceB, nRefL0: 1, nRefL1: 1})
	if p.POC() != 62 {
		t.Fatalf("B POC = %d, want 62", p.POC())
	}
	// A jump of more than MaxLsb/2 in one step is read as going backwards.
	p, _, _ = decode(t, d, sps, pic{ref: false, fn: 4, lsb: 40, typ: SliceB, nRefL0: 1, nRefL1: 1})
	if p.POC() != 40 {
		t.Fatalf("POC = %d, want 40", p.POC())
	}
	// IDR resets everything.
	p, _, _ = decode(t, d, sps, pic{idr: true, ref: true, fn: 0, lsb: 0, typ: SliceI})
	if p.POC() != 0 {
		t.Fatalf("IDR POC = %d", p.POC())
	}
}

func TestPOCType1(t *testing.T) {
	sps := &SPS{Log2MaxFrameNum: 4, PicOrderCntType: 1, MaxNumRefFrames: 4,
		OffsetForRefFrame: []int32{4, 2}, OffsetForNonRefPic: -1}
	d := NewDPB(nil)
	want := []struct {
		p   pic
		poc int
	}{
		{pic{idr: true, ref: true, fn: 0, typ: SliceI}, 0},
		{pic{ref: true, fn: 1, typ: SliceP, nRefL0: 1}, 4},
		{pic{ref: true, fn: 2, typ: SliceP, nRefL0: 1}, 6},
		{pic{ref: true, fn: 3, typ: SliceP, nRefL0: 1}, 10},
		{pic{ref: false, fn: 4, typ: SliceP, nRefL0: 1}, 9},
		{pic{ref: true, fn: 4, typ: SliceP, nRefL0: 1}, 12},
	}
	for i, w := range want {
		p, _, _ := decode(t, d, sps, w.p)
		if p.POC() != w.poc {
			t.Fatalf("picture %d: POC %d, want %d", i, p.POC(), w.poc)
		}
	}
	// frame_num wrap adds MaxFrameNum to FrameNumOffset.
	for fn := uint32(5); fn <= 15; fn++ {
		decode(t, d, sps, pic{ref: true, fn: fn, typ: SliceP, nRefL0: 1})
	}
	p, _, _ := decode(t, d, sps, pic{ref: true, fn: 0, typ: SliceP, nRefL0: 1})
	// absFrameNum 16: cycleCnt 7, inCycle 1 -> 7*6 + 4 + 2 = 48
	if p.POC() != 48 {
		t.Fatalf("POC after frame_num wrap = %d, want 48", p.POC())
	}
}

func TestPOCType2(t *testing.T) {
	sps := &SPS{Log2MaxFrameNum: 4, PicOrderCntType: 2, MaxNumRefFrames: 1}
	d := NewDPB(nil)
	p, _, _ := decode(t, d, sps, pic{idr: true, ref: true, fn: 0, typ: SliceI})
	if p.POC() != 0 {
		t.Fatal("IDR POC")
	}
	p, _, _ = decode(t, d, sps, pic{ref: true, fn: 1, typ: SliceP, nRefL0: 1})
	if p.POC() != 2 {
		t.Fatalf("POC = %d, want 2", p.POC())
	}
	p, _, _ = decode(t, d, sps, pic{ref: false, fn: 2, typ: SliceP, nRefL0: 1})
	if p.POC() != 3 {
		t.Fatalf("non-ref POC = %d, want 3", p.POC())
	}
	for fn := uint32(2); fn <= 15; fn++ {
		decode(t, d, sps, pic{ref: true, fn: fn, typ: SliceP, nRefL0: 1})
	}
	p, _, _ = decode(t, d, sps, pic{ref: true, fn: 0, typ: SliceP, nRefL0: 1})
	if p.POC() != 32 {
		t.Fatalf("POC after wrap = %d, want 32", p.POC())
	}
}

func TestStartErrors(t *testing.T) {
	sps := testSPS()
	d := NewDPB(nil)
	if _, err := d.Start(sps, pic{ref: true, fn: 1, typ: SliceP}.header(), nil); err != ErrNoKeyframe {
		t.Fatalf("non-IDR first: %v", err)
	}
	if _, err := d.Start(sps, pic{idr: true, ref: true, typ: SliceI}.header(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Start(sps, pic{ref: true, fn: 1, typ: SliceP}.header(), nil); err != ErrPictureInProgress {
		t.Fatalf("double start: %v", err)
	}
	h := pic{ref: true, fn: 1, typ: SliceP}.header()
	h.FieldPic = true
	d.Reset()
	if _, err := d.Start(sps, pic{idr: true, ref: true, typ: SliceI}.header(), nil); err != nil {
		t.Fatal(err)
	}
	d.Finish(pic{idr: true, ref: true, typ: SliceI}.header())
	if _, err := d.Start(sps, h, nil); err != ErrFieldCoding {
		t.Fatalf("field picture: %v", err)
	}
}

func TestResetReleasesEverything(t *testing.T) {
	sps := testSPS()
	fs := &fakeSurfaces{}
	d := NewDPB(fs)
	decode(t, d, sps, pic{idr: true, ref: true, fn: 0, typ: SliceI})
	decode(t, d, sps, pic{ref: true, fn: 1, lsb: 2, typ: SliceP, nRefL0: 1})
	d.Reset()
	if len(d.Refs()) != 0 || len(fs.released) != 2 {
		t.Fatalf("refs %d released %d", len(d.Refs()), len(fs.released))
	}
	if _, err := d.Start(sps, pic{ref: true, fn: 2, typ: SliceP}.header(), nil); err != ErrNoKeyframe {
		t.Fatalf("after Reset a non-IDR must be rejected: %v", err)
	}
}
