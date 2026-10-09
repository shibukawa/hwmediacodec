package mp4_test

import (
	"bytes"
	"io"
	"testing"
	"time"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/internal/mp4test"
	"github.com/shibukawa/hwmediacodec/mediacontainer/mp4"
)

func TestPacketSource(t *testing.T) {
	mp4test.RequireFFmpeg(t)
	src := mp4test.GenerateMP4(t, t.TempDir(), mp4test.MP4Options{Codec: hwmediacodec.H264, Width: 160, Height: 120, Frames: 60, BFrames: 2, GOP: 10})
	d, err := mp4.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	v := d.Video()
	ps := v.PacketSource()
	if ps.Codec() != hwmediacodec.H264 {
		t.Fatalf("codec %s", ps.Codec())
	}
	if l := ps.Length(); l != 2*time.Second {
		t.Errorf("Length %v, want 2s", l)
	}

	// The whole stream in decode order, presentation times starting at 0
	// (the edit list hides the B-frame delay) and covering every frame.
	var times []time.Duration
	for {
		data, pts, err := ps.ReadPacket()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(data) == 0 {
			t.Fatal("empty packet")
		}
		times = append(times, pts)
	}
	if len(times) != 60 {
		t.Fatalf("%d packets, want 60", len(times))
	}
	seen := map[time.Duration]bool{}
	for _, pts := range times {
		seen[pts] = true
	}
	for i := 0; i < 60; i++ {
		if !seen[time.Duration(i)*time.Second/30] {
			t.Fatalf("no packet presented at frame %d", i)
		}
	}

	// Seeking lands on the keyframe at or before the target and reads it
	// next; keyframes are every 10 frames (1/3 s).
	for _, tc := range []struct {
		target, want time.Duration
		sample       int
	}{
		{1500 * time.Millisecond, 1333333333, 40},
		{time.Second, time.Second, 30},
		{0, 0, 0},
		{10 * time.Second, 1666666666, 50},
	} {
		got, err := ps.SeekKeyframe(tc.target)
		if err != nil {
			t.Fatal(err)
		}
		if got/time.Microsecond != tc.want/time.Microsecond {
			t.Errorf("SeekKeyframe(%v) = %v, want %v", tc.target, got, tc.want)
		}
		data, pts, err := ps.ReadPacket()
		if err != nil {
			t.Fatal(err)
		}
		want, _ := v.AccessUnit(tc.sample)
		if !bytes.Equal(data, want.Data) || pts != got || !want.Keyframe {
			t.Errorf("after SeekKeyframe(%v) the next packet is not keyframe sample %d (pts %v)", tc.target, tc.sample, pts)
		}
	}
}
