package webrtc

import (
	"errors"
	"math"
	"sync"
	"testing"

	pion "github.com/pion/webrtc/v4"
	"github.com/shibukawa/hwmediacodec"
)

func TestBroadcasterConcurrentWrites(t *testing.T) {
	b := NewBroadcaster(math.NaN(), nil, nil)
	track, err := pion.NewTrackLocalStaticSample(h264Codec, "video", "test")
	if err != nil {
		t.Fatal(err)
	}
	b.peers[&peer{track: track}] = struct{}{}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for i := range 100 {
				if err := b.WritePacket(hwmediacodec.Packet{PTS: int64(i), Keyframe: true}); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	b.mu.Lock()
	b.peers = map[*peer]struct{}{} // this peer did not need a connection
	b.mu.Unlock()
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if err := b.WritePacket(hwmediacodec.Packet{}); !errors.Is(err, hwmediacodec.ErrClosed) {
		t.Fatalf("write after close = %v", err)
	}
	if _, err := b.Accept(pion.SessionDescription{}); err == nil {
		t.Fatal("closed broadcaster accepted a peer")
	}
}

func TestBroadcasterDefaultsUnrepresentableFramePeriod(t *testing.T) {
	for _, fps := range []float64{0, -1, math.NaN(), math.Inf(1), math.SmallestNonzeroFloat64, math.MaxFloat64} {
		b := NewBroadcaster(fps, nil, nil)
		if b.fps != 60 {
			t.Errorf("invalid frame rate %g became %g", fps, b.fps)
		}
	}
}
