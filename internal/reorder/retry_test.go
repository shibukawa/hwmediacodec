package reorder

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/shibukawa/hwmediacodec/encoding/annexb"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/h264"
)

type retryDecoder struct {
	fakeDecoder
	sendErr  error
	flushErr error
	orders   []codec.Order
}

func (d *retryDecoder) Send(ctx context.Context, p codec.Packet) error {
	d.orders = append(d.orders, codec.PacketOrder(p))
	if d.sendErr != nil {
		return d.sendErr
	}
	return d.fakeDecoder.Send(ctx, p)
}

func (d *retryDecoder) Flush(ctx context.Context) error {
	if d.flushErr != nil {
		return d.flushErr
	}
	return d.fakeDecoder.Flush(ctx)
}

func retryH264Packet() codec.Packet {
	sps := &h264.SPS{
		ProfileIDC: 66, LevelIDC: 31, ChromaFormatIDC: 1, BitDepthLuma: 8, BitDepthChroma: 8,
		Log2MaxFrameNum: 4, PicOrderCntType: 0, Log2MaxPicOrderCntLsb: 4, MaxNumRefFrames: 1,
		PicWidthInMbs: 20, PicHeightInMapUnits: 15, FrameMbsOnly: true, Direct8x8Inference: true,
	}
	pps := &h264.PPS{NumSliceGroups: 1, NumRefIdxL0DefaultActive: 1, NumRefIdxL1DefaultActive: 1}
	idr, _ := h264.WriteSliceHeader(&h264.SliceHeader{NALType: h264.NALSliceIDR, NALRefIdc: 3, IDR: true, SliceTypeRaw: 2, SliceType: h264.SliceI}, sps, pps)
	var data []byte
	for _, nal := range [][]byte{h264.WriteSPS(sps), h264.WritePPS(pps), idr} {
		data = annexb.AppendUnit(data, nal)
	}
	return codec.Packet{Data: data}
}

func retryHEVCPacket(t *testing.T) codec.Packet {
	t.Helper()
	var data []byte
	for _, text := range []string{
		"42010101600000030090000003000003003ca00a080f16595964932bc05a020000030002000003003c10",
		"4401c172b46240", "2801af08482f002d07c163fc",
	} {
		nal, err := hex.DecodeString(text)
		if err != nil {
			t.Fatal(err)
		}
		data = annexb.AppendUnit(data, nal)
	}
	return codec.Packet{Data: data}
}

func TestSendFailureDoesNotAdvanceTracker(t *testing.T) {
	for _, c := range []codec.Codec{codec.H264, codec.HEVC} {
		for _, sendErr := range []error{codec.ErrAgain, context.Canceled} {
			t.Run(c.String()+"/"+sendErr.Error(), func(t *testing.T) {
				p := retryH264Packet()
				if c == codec.HEVC {
					p = retryHEVCPacket(t)
				}
				inner := &retryDecoder{fakeDecoder: fakeDecoder{codec: c}, sendErr: sendErr}
				dec := Wrap(inner, c)
				dec.flushing, dec.drained = true, true
				if err := dec.Send(context.Background(), p); !errors.Is(err, sendErr) {
					t.Fatal(err)
				}
				if dec.tracker.seq != 0 || !dec.tracker.afterReset || !dec.flushing || !dec.drained {
					t.Fatal("failed send changed picture-order or draining state")
				}
				if dec.tracker.h264PS.SPS(0) != nil || len(dec.tracker.hevcSPS) != 0 || len(dec.tracker.hevcPPS) != 0 {
					t.Fatal("failed send retained new parameter sets")
				}
				inner.sendErr = nil
				if err := dec.Send(context.Background(), p); err != nil {
					t.Fatal(err)
				}
				if len(inner.orders) != 2 || inner.orders[0] != inner.orders[1] || inner.orders[1].Seq != 1 {
					t.Fatalf("retry received inconsistent order keys: %+v", inner.orders)
				}
				if dec.tracker.seq != 1 || dec.tracker.afterReset || dec.flushing || dec.drained {
					t.Fatal("successful retry did not commit picture-order state")
				}
			})
		}
	}
}

func TestFlushFailureDoesNotResetTracker(t *testing.T) {
	inner := &retryDecoder{fakeDecoder: fakeDecoder{codec: codec.H264}, flushErr: codec.ErrAgain}
	dec := Wrap(inner, codec.H264)
	if err := dec.Send(context.Background(), retryH264Packet()); err != nil {
		t.Fatal(err)
	}
	before := dec.tracker.h264POC
	if err := dec.Flush(context.Background()); !errors.Is(err, codec.ErrAgain) {
		t.Fatal(err)
	}
	if dec.flushing || dec.tracker.afterReset || dec.tracker.h264POC != before {
		t.Fatal("failed flush reset picture-order state")
	}
	inner.flushErr = nil
	if err := dec.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !dec.flushing || !dec.tracker.afterReset {
		t.Fatal("successful flush did not reset picture-order state")
	}
}

func TestPictureRetryPreservesPOC(t *testing.T) {
	inner := &retryDecoder{fakeDecoder: fakeDecoder{codec: codec.H264}}
	dec := Wrap(inner, codec.H264)
	ctx := context.Background()
	if err := dec.Send(ctx, retryH264Packet()); err != nil {
		t.Fatal(err)
	}
	sps, pps, err := dec.tracker.h264PS.Lookup(0)
	if err != nil {
		t.Fatal(err)
	}
	nal, _ := h264.WriteSliceHeader(&h264.SliceHeader{
		NALType: h264.NALSlice, NALRefIdc: 3, SliceTypeRaw: 2, SliceType: h264.SliceI,
		FrameNum: 1, PicOrderCntLsb: 2,
	}, sps, pps)
	p := codec.Packet{Data: annexb.AppendUnit(nil, nal)}
	beforePOC, beforePS := dec.tracker.h264POC, dec.tracker.h264PS
	inner.sendErr = codec.ErrAgain
	if err := dec.Send(ctx, p); !errors.Is(err, codec.ErrAgain) {
		t.Fatal(err)
	}
	if dec.tracker.h264POC != beforePOC {
		t.Fatal("rejected reference picture advanced POC state")
	}
	inner.sendErr = nil
	if err := dec.Send(ctx, p); err != nil {
		t.Fatal(err)
	}
	if dec.tracker.h264POC == beforePOC || dec.tracker.h264PS != beforePS {
		t.Fatal("picture retry did not advance POC without copying parameter maps")
	}
	if inner.orders[1] != inner.orders[2] || inner.orders[2].POC != 2 {
		t.Fatalf("picture retry changed display order: %+v", inner.orders)
	}
}
