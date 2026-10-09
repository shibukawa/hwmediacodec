package hevc_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/shibukawa/hwmediacodec/internal/hevc"
)

func pocsOf(l []*hevc.Picture) []int32 {
	var out []int32
	for _, p := range l {
		if p == nil {
			out = append(out, -999)
			continue
		}
		out = append(out, p.POC)
	}
	return out
}

func explicitRPS(neg, pos []int32, usedNeg, usedPos []bool) *hevc.ShortTermRPS {
	s := &hevc.ShortTermRPS{NumNegativePics: len(neg), NumPositivePics: len(pos)}
	for i, d := range neg {
		s.DeltaPocS0[i] = d
		s.UsedByCurrPicS0[i] = usedNeg == nil || usedNeg[i]
	}
	for i, d := range pos {
		s.DeltaPocS1[i] = d
		s.UsedByCurrPicS1[i] = usedPos == nil || usedPos[i]
	}
	return s
}

func testSPS() *hevc.SPS {
	return &hevc.SPS{Log2MaxPOCLsb: 4, MaxDecPicBufferingMinus1: []uint32{5}, MaxNumReorderPics: []uint32{0}, MaxLatencyIncreasePlus1: []uint32{0}}
}

// decode runs one picture through the DPB and returns its handle-carrying
// picture.
func decode(t *testing.T, d *hevc.DPB, sps *hevc.SPS, sh *hevc.SliceHeader) *hevc.Picture {
	t.Helper()
	p, err := d.Start(sps, sh, int(sh.POCLsb))
	if err != nil {
		t.Fatalf("Start(POC lsb %d): %v", sh.POCLsb, err)
	}
	d.Finish()
	return p
}

// TestDPBLongTermAndModification covers long-term references (with and
// without delta_poc_msb_present_flag), reference list modification, list
// wrap-around when a list is longer than the reference picture set, and
// the release of pictures that leave the set.
func TestDPBLongTermAndModification(t *testing.T) {
	sps := testSPS()
	var released []int32
	d := hevc.NewDPB()
	d.Release = func(p *hevc.Picture) { released = append(released, p.POC) }

	decode(t, d, sps, &hevc.SliceHeader{NALType: hevc.NALIDRWRADL, SliceType: hevc.SliceI})
	for poc := uint32(1); poc <= 3; poc++ {
		neg := []int32{-1, -2, -3}[:poc]
		decode(t, d, sps, &hevc.SliceHeader{NALType: hevc.NALTrailR, SliceType: hevc.SliceP, POCLsb: poc,
			ShortTermRPS: explicitRPS(neg, nil, nil, nil), NumRefIdxL0Active: poc})
	}

	// POC 4: short-term 3 and 2, long-term POC 0 by its lsb; POC 1 leaves.
	sh := &hevc.SliceHeader{NALType: hevc.NALTrailR, SliceType: hevc.SliceP, POCLsb: 4,
		ShortTermRPS:      explicitRPS([]int32{-1, -2}, nil, nil, nil),
		LongTerm:          []hevc.LongTermRef{{POCLsb: 0, UsedByCurrPic: true}},
		NumRefIdxL0Active: 5,
	}
	if _, err := d.Start(sps, sh, 4); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(released, []int32{1}) {
		t.Errorf("released %v, want [1]", released)
	}
	before, after, lt := d.RefPicSets()
	if !reflect.DeepEqual(pocsOf(before), []int32{3, 2}) || len(after) != 0 || !reflect.DeepEqual(pocsOf(lt), []int32{0}) {
		t.Errorf("sets: before %v after %v long-term %v", pocsOf(before), pocsOf(after), pocsOf(lt))
	}
	if !lt[0].LongTerm || before[0].LongTerm {
		t.Error("long-term marking is wrong")
	}
	// Five entries from three pictures: the list wraps around.
	l0, l1 := d.RefPicLists(sh)
	if !reflect.DeepEqual(pocsOf(l0), []int32{3, 2, 0, 3, 2}) || l1 != nil {
		t.Errorf("RefPicList0 %v, RefPicList1 %v", pocsOf(l0), pocsOf(l1))
	}
	// The same slice with list modification: entries index the temporary
	// list.
	mod := *sh
	mod.NumRefIdxL0Active = 3
	mod.RefPicListModificationL0 = true
	mod.ListEntryL0 = []uint32{2, 0, 0}
	if l0, _ := d.RefPicLists(&mod); !reflect.DeepEqual(pocsOf(l0), []int32{0, 3, 3}) {
		t.Errorf("modified RefPicList0 %v", pocsOf(l0))
	}
	if refs := d.Refs(); !reflect.DeepEqual(pocsOf(refs), []int32{3, 2, 0}) {
		t.Errorf("Refs %v", pocsOf(refs))
	}
	d.Finish()

	// POC 6 comes first in decoding order so that POC 5 can be a B picture
	// with a reference on either side. It keeps the long-term picture
	// without using it.
	decode(t, d, sps, &hevc.SliceHeader{NALType: hevc.NALTrailR, SliceType: hevc.SliceP, POCLsb: 6,
		ShortTermRPS: explicitRPS([]int32{-2}, nil, nil, nil),
		LongTerm:     []hevc.LongTermRef{{POCLsb: 0, UsedByCurrPic: false}}, NumRefIdxL0Active: 1})
	// POC 5: B picture between 4 and 6. The long-term picture is named with
	// its full POC (delta_poc_msb_present_flag, cycle 0), kept but unused.
	b := &hevc.SliceHeader{NALType: hevc.NALTrailN, SliceType: hevc.SliceB, POCLsb: 5,
		ShortTermRPS:      explicitRPS([]int32{-1}, []int32{1}, nil, nil),
		LongTerm:          []hevc.LongTermRef{{POCLsb: 0, DeltaPOCMsbPresent: true}},
		NumRefIdxL0Active: 2, NumRefIdxL1Active: 1,
	}
	if _, err := d.Start(sps, b, 5); err != nil {
		t.Fatal(err)
	}
	l0, l1 = d.RefPicLists(b)
	if !reflect.DeepEqual(pocsOf(l0), []int32{4, 6}) || !reflect.DeepEqual(pocsOf(l1), []int32{6}) {
		t.Errorf("B lists %v %v", pocsOf(l0), pocsOf(l1))
	}
	if refs := d.Refs(); !reflect.DeepEqual(pocsOf(refs), []int32{4, 6, 0}) {
		t.Errorf("B Refs %v (the unused long-term picture comes last)", pocsOf(refs))
	}
	d.Finish()

	// The picture order count wraps (4 bits): POC 16 has lsb 0, and the
	// long-term picture POC 0 is told apart by the msb cycle.
	for poc := uint32(7); poc < 16; poc++ {
		decode(t, d, sps, &hevc.SliceHeader{NALType: hevc.NALTrailR, SliceType: hevc.SliceP, POCLsb: poc,
			ShortTermRPS: explicitRPS([]int32{-1}, nil, nil, nil),
			LongTerm:     []hevc.LongTermRef{{POCLsb: 0}}, NumRefIdxL0Active: 1})
	}
	wrap := &hevc.SliceHeader{NALType: hevc.NALTrailR, SliceType: hevc.SliceP, POCLsb: 1,
		ShortTermRPS:      explicitRPS([]int32{-1}, nil, nil, nil),
		LongTerm:          []hevc.LongTermRef{{POCLsb: 0, UsedByCurrPic: true, DeltaPOCMsbPresent: true, DeltaPOCMsbCycle: 1}},
		NumRefIdxL0Active: 2,
	}
	decode(t, d, sps, &hevc.SliceHeader{NALType: hevc.NALTrailR, SliceType: hevc.SliceP, POCLsb: 0,
		ShortTermRPS: explicitRPS([]int32{-1}, nil, nil, nil),
		LongTerm:     []hevc.LongTermRef{{POCLsb: 0, DeltaPOCMsbPresent: true, DeltaPOCMsbCycle: 1}}, NumRefIdxL0Active: 1})
	cur, err := d.Start(sps, wrap, 17)
	if err != nil {
		t.Fatal(err)
	}
	if cur.POC != 17 {
		t.Fatalf("POC after the wrap = %d, want 17", cur.POC)
	}
	l0, _ = d.RefPicLists(wrap)
	if !reflect.DeepEqual(pocsOf(l0), []int32{16, 0}) {
		t.Errorf("lists after the wrap %v, want [16 0]", pocsOf(l0))
	}
	d.Finish()

	released = nil
	d.Reset()
	if len(released) != 3 { // POC 0, 16, 17
		t.Errorf("Reset released %v", released)
	}
}

// TestDPBMissingReference checks the stand-in for a reference the stream
// never delivered and the errors for pictures that cannot start decoding.
func TestDPBMissingReference(t *testing.T) {
	sps := testSPS()
	released := 0
	d := hevc.NewDPB()
	d.Release = func(*hevc.Picture) { released++ }
	d.MissingHandle = func() any { return "dummy" }

	p := &hevc.SliceHeader{NALType: hevc.NALTrailR, SliceType: hevc.SliceP, POCLsb: 1,
		ShortTermRPS: explicitRPS([]int32{-1}, nil, nil, nil), NumRefIdxL0Active: 1}
	if _, err := d.Start(sps, p, 1); !errors.Is(err, hevc.ErrNoKeyframe) {
		t.Fatalf("P picture first: %v", err)
	}
	decode(t, d, sps, &hevc.SliceHeader{NALType: hevc.NALIDRNLP, SliceType: hevc.SliceI})
	if _, err := d.Start(sps, p, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Start(sps, p, 1); !errors.Is(err, hevc.ErrPictureInProgress) {
		t.Fatalf("second Start: %v", err)
	}
	d.Abort()
	if released != 1 {
		t.Errorf("Abort released %d pictures", released)
	}

	// POC 3 names POC 2, which was never decoded, and keeps POC 1 (also
	// gone, but only for later pictures: no stand-in needed).
	sh := &hevc.SliceHeader{NALType: hevc.NALTrailR, SliceType: hevc.SliceP, POCLsb: 3,
		ShortTermRPS: explicitRPS([]int32{-1, -2, -3}, nil, []bool{true, false, true}, nil), NumRefIdxL0Active: 2}
	if _, err := d.Start(sps, sh, 3); err != nil {
		t.Fatal(err)
	}
	refs := d.Refs()
	if !reflect.DeepEqual(pocsOf(refs), []int32{2, 0}) || !refs[0].Missing || refs[0].Handle != "dummy" || refs[1].Missing {
		t.Errorf("refs %v, missing flags %v %v", pocsOf(refs), refs[0].Missing, refs[1].Missing)
	}
	l0, _ := d.RefPicLists(sh)
	if !reflect.DeepEqual(pocsOf(l0), []int32{2, 0}) {
		t.Errorf("list %v", pocsOf(l0))
	}
	d.Finish()

	// After an end of sequence the next picture must be an IRAP picture; a
	// CRA then drops everything before it and its RASL pictures are skipped.
	d.EndOfSequence()
	if _, err := d.Start(sps, p, 9); !errors.Is(err, hevc.ErrNoKeyframe) {
		t.Fatalf("P picture after end of sequence: %v", err)
	}
	before := released
	cra := &hevc.SliceHeader{NALType: hevc.NALCRA, SliceType: hevc.SliceI, POCLsb: 8,
		ShortTermRPS: explicitRPS([]int32{-1}, nil, []bool{false}, nil)}
	cur, err := d.Start(sps, cra, 8)
	if err != nil {
		t.Fatal(err)
	}
	if cur.POC != 8 || len(d.Refs()) != 0 {
		t.Errorf("CRA: POC %d, %d references", cur.POC, len(d.Refs()))
	}
	if released-before != 2 { // POC 0 and POC 3; the stand-in is not released
		t.Errorf("CRA released %d pictures, want 2", released-before)
	}
	d.Finish()
	rasl := &hevc.SliceHeader{NALType: hevc.NALRASLR, SliceType: hevc.SliceP, POCLsb: 7,
		ShortTermRPS: explicitRPS([]int32{-1}, []int32{1}, nil, nil), NumRefIdxL0Active: 1}
	if _, err := d.Start(sps, rasl, 7); !errors.Is(err, hevc.ErrSkipped) {
		t.Fatalf("RASL after a CRA that starts decoding: %v", err)
	}
	// A trailing picture ends the leading pictures; a later CRA continues
	// the sequence and its RASL pictures are decodable.
	decode(t, d, sps, &hevc.SliceHeader{NALType: hevc.NALTrailR, SliceType: hevc.SliceP, POCLsb: 9,
		ShortTermRPS: explicitRPS([]int32{-1}, nil, nil, nil), NumRefIdxL0Active: 1})
	decode(t, d, sps, &hevc.SliceHeader{NALType: hevc.NALCRA, SliceType: hevc.SliceI, POCLsb: 12,
		ShortTermRPS: explicitRPS([]int32{-3}, nil, nil, nil)})
	rasl2 := &hevc.SliceHeader{NALType: hevc.NALRASLN, SliceType: hevc.SliceB, POCLsb: 11,
		ShortTermRPS: explicitRPS([]int32{-2}, []int32{1}, nil, nil), NumRefIdxL0Active: 1, NumRefIdxL1Active: 1}
	if _, err := d.Start(sps, rasl2, 11); err != nil {
		t.Fatalf("RASL of a CRA inside the sequence: %v", err)
	}
	l0, l1 := d.RefPicLists(rasl2)
	if !reflect.DeepEqual(pocsOf(l0), []int32{9}) || !reflect.DeepEqual(pocsOf(l1), []int32{12}) {
		t.Errorf("RASL lists %v %v", pocsOf(l0), pocsOf(l1))
	}
}
