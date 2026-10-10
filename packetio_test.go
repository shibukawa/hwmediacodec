package hwmediacodec_test

import (
	"context"
	"errors"
	"testing"

	"github.com/shibukawa/hwmediacodec"
)

func TestHasHardwareAgreesWithProbe(t *testing.T) {
	ctx := context.Background()
	caps, err := hwmediacodec.Probe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []hwmediacodec.Codec{hwmediacodec.H264, hwmediacodec.HEVC, hwmediacodec.AV1} {
		for _, dir := range []hwmediacodec.Direction{hwmediacodec.Decode, hwmediacodec.Encode} {
			want := false
			for _, cap := range caps {
				if cap.Codec == c && cap.Direction == dir && cap.Hardware {
					want = true
				}
			}
			if got := hwmediacodec.HasHardware(ctx, c, dir); got != want {
				t.Errorf("HasHardware(%s, %s) = %v, Probe says %v", c, dir, got, want)
			}
		}
	}
}

func TestPacketWriterFunc(t *testing.T) {
	boom := errors.New("boom")
	var got []int64
	var w hwmediacodec.PacketWriteCloser = hwmediacodec.PacketWriterFunc(func(p hwmediacodec.Packet) error {
		if p.PTS < 0 {
			return boom
		}
		got = append(got, p.PTS)
		return nil
	})
	if err := w.WritePacket(hwmediacodec.Packet{PTS: 7}); err != nil || len(got) != 1 || got[0] != 7 {
		t.Errorf("WritePacket: %v %v", got, err)
	}
	if err := w.WritePacket(hwmediacodec.Packet{PTS: -1}); !errors.Is(err, boom) {
		t.Errorf("the function's error is not returned: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}
