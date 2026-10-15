package h264

import (
	"errors"
	"testing"

	"github.com/shibukawa/hwmediacodec/internal/bitstream"
)

// A stream that marks every picture as a new long-term reference must not
// grow the reference set past max_num_ref_frames: on a hardware backend
// every reference pins a surface of a fixed pool.
func TestDPBBoundsLongTermReferences(t *testing.T) {
	sps := testSPS()
	sps.MaxNumRefFrames = 2
	d := NewDPB(&fakeSurfaces{})
	decode(t, d, sps, pic{idr: true, ref: true, fn: 0, typ: SliceI})
	for i := 1; i < 40; i++ {
		decode(t, d, sps, pic{ref: true, fn: uint32(i % 16), lsb: uint32(2 * i % 64), typ: SliceP, nRefL0: 1,
			mmco: []MMCO{{Op: 6, LongTermFrameIdx: uint32(i % 16)}}})
		if n := len(d.Refs()); n > 2 {
			t.Fatalf("after %d long-term pictures the DPB holds %d references, want at most 2", i, n)
		}
	}
}

// Impossible memory management values are syntax errors, not state.
func TestDPBRejectsImpossibleLongTermIndices(t *testing.T) {
	for _, mmco := range [][]MMCO{
		{{Op: 6, LongTermFrameIdx: 16}},
		{{Op: 4, MaxLongTermFrameIdxPlus1: 17}},
		{{Op: 3, DifferenceOfPicNumsMinus1: 0, LongTermFrameIdx: 40}},
	} {
		sps := testSPS()
		d := NewDPB(&fakeSurfaces{})
		decode(t, d, sps, pic{idr: true, ref: true, fn: 0, typ: SliceI})
		h := pic{ref: true, fn: 1, lsb: 2, typ: SliceP, nRefL0: 1, mmco: mmco}.header()
		if _, err := d.Start(sps, h, 1); err != nil {
			t.Fatal(err)
		}
		if err := d.Finish(h); !errors.Is(err, ErrSyntax) {
			t.Errorf("MMCO %+v: Finish = %v, want a syntax error", mmco, err)
		}
	}
}

// A gap in frame_num only infers as many frames as can stay referenced,
// however large the gap: 65535 missing frames cost no 65535 surfaces.
func TestDPBFrameNumGapIsBounded(t *testing.T) {
	sps := testSPS()
	sps.Log2MaxFrameNum, sps.MaxNumRefFrames, sps.GapsInFrameNumAllowed = 16, 2, true
	fs := &fakeSurfaces{}
	d := NewDPB(fs)
	decode(t, d, sps, pic{idr: true, ref: true, fn: 0, typ: SliceI})
	decode(t, d, sps, pic{ref: true, fn: 40000, lsb: 2, typ: SliceP, nRefL0: 1})
	if fs.next > 2 {
		t.Fatalf("a gap of 39999 frames allocated %d surfaces, want at most 2", fs.next)
	}
	if n := len(d.Refs()); n > 2 {
		t.Fatalf("%d references after the gap, want at most 2", n)
	}
}

// delta_scale outside [-128, 127] is a syntax error rather than a wrapped
// scaling matrix handed to the driver.
func TestParseScalingListRejectsDeltaOutOfRange(t *testing.T) {
	for _, delta := range []int32{128, -129, 1 << 30, -(1 << 30)} {
		var w bitstream.Writer
		w.WriteSE(delta)
		w.Trailing()
		var dst [16]uint8
		if _, err := parseScalingList(bitstream.New(w.Bytes()), dst[:], zigzag4x4[:]); !errors.Is(err, ErrSyntax) {
			t.Errorf("delta_scale %d: %v, want a syntax error", delta, err)
		}
	}
	var w bitstream.Writer
	for range 16 {
		w.WriteSE(0)
	}
	w.Trailing()
	var dst [16]uint8
	if _, err := parseScalingList(bitstream.New(w.Bytes()), dst[:], zigzag4x4[:]); err != nil {
		t.Fatalf("flat list rejected: %v", err)
	}
}
