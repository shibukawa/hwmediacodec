package webrtc

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	pion "github.com/pion/webrtc/v4"
	"github.com/shibukawa/hwmediacodec"
)

// testPeer registers a peer whose queue is drained by nobody, so a test
// can observe what WritePacket does without a connection.
func testPeer(t *testing.T, b *Broadcaster) *peer {
	t.Helper()
	track, err := pion.NewTrackLocalStaticSample(h264Codec, "video", "test")
	if err != nil {
		t.Fatal(err)
	}
	p := &peer{track: track, queue: make(chan queued, peerQueueSize), done: make(chan struct{})}
	b.mu.Lock()
	b.peers[p] = struct{}{}
	b.mu.Unlock()
	return p
}

func TestBroadcasterConcurrentWrites(t *testing.T) {
	b := NewBroadcaster(math.NaN(), nil, nil)
	p := testPeer(t, b)
	go func() {
		for {
			select {
			case <-p.done:
				return
			case <-p.queue:
			}
		}
	}()
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
	b.remove(p) // idempotent: a slow drain may already have dropped it
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if err := b.WritePacket(hwmediacodec.Packet{}); !errors.Is(err, hwmediacodec.ErrClosed) {
		t.Fatalf("write after close = %v", err)
	}
	if _, err := b.Accept(pion.SessionDescription{}); !errors.Is(err, hwmediacodec.ErrClosed) {
		t.Fatalf("closed broadcaster answered %v", err)
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

// A viewer that floods the stream with picture loss indications gets one
// keyframe request per interval, not one per indication.
func TestBroadcasterMergesKeyframeRequests(t *testing.T) {
	var requests int
	b := NewBroadcaster(30, nil, func() { requests++ })
	for range 100 {
		b.askKeyframe()
	}
	if requests != 1 {
		t.Fatalf("100 requests within the interval reached the encoder %d times, want 1", requests)
	}
	b.mu.Lock()
	b.lastKeyRequest = time.Now().Add(-2 * keyframeMinInterval)
	b.mu.Unlock()
	b.askKeyframe()
	if requests != 2 {
		t.Fatalf("a request after the interval reached the encoder %d times, want 2", requests)
	}
}

// A viewer that stops draining its queue is dropped instead of stalling
// the writer or holding packets without bound.
func TestBroadcasterDropsStalledViewer(t *testing.T) {
	b := NewBroadcaster(30, nil, nil)
	p := testPeer(t, b)
	for i := range peerQueueSize + 1 {
		if err := b.WritePacket(hwmediacodec.Packet{PTS: int64(i), Keyframe: true}); err != nil {
			t.Fatal(err)
		}
	}
	if b.Viewers() != 0 {
		t.Fatalf("stalled viewer still registered (%d viewers)", b.Viewers())
	}
	select {
	case <-p.done:
	default:
		t.Fatal("the dropped peer was not told")
	}
}

func TestBroadcasterRefusesBadOffers(t *testing.T) {
	b := NewBroadcaster(30, nil, nil)
	defer b.Close()
	srv := httptest.NewServer(b)
	defer srv.Close()
	post := func(body string) int {
		t.Helper()
		resp, err := http.Post(srv.URL, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := post("not json"); code != http.StatusBadRequest {
		t.Errorf("a body that is not JSON answered %d, want 400", code)
	}
	if code := post(`{"type":"offer","sdp":"v=0\r\n"}`); code != http.StatusBadRequest {
		t.Errorf("an unusable offer answered %d, want 400", code)
	}
	if _, err := b.Accept(pion.SessionDescription{Type: pion.SDPTypeOffer, SDP: "v=0\r\n"}); !errors.Is(err, ErrBadOffer) {
		t.Errorf("Accept of an unusable offer = %v, want ErrBadOffer", err)
	}
}

func TestBroadcasterLimitsViewers(t *testing.T) {
	b := NewBroadcaster(30, nil, nil)
	defer b.Close()
	b.MaxViewers = 1
	testPeer(t, b)
	if _, err := b.Accept(pion.SessionDescription{}); !errors.Is(err, ErrTooManyViewers) {
		t.Fatalf("Accept beyond MaxViewers = %v, want ErrTooManyViewers", err)
	}
	srv := httptest.NewServer(b)
	defer srv.Close()
	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(`{"type":"offer","sdp":""}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("a full broadcaster answered %d, want 503", resp.StatusCode)
	}
	// Connections still being set up count too.
	b.MaxViewers = 2
	b.mu.Lock()
	b.pending = 1
	b.mu.Unlock()
	if _, err := b.Accept(pion.SessionDescription{}); !errors.Is(err, ErrTooManyViewers) {
		t.Fatalf("Accept with a pending connection at the limit = %v, want ErrTooManyViewers", err)
	}
}

// A request that is cancelled while ICE candidates are gathered releases
// its connection instead of keeping it until the gathering times out.
func TestBroadcasterAcceptHonoursContext(t *testing.T) {
	b := NewBroadcaster(30, nil, nil)
	defer b.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// An empty offer fails before gathering; a cancelled context must
	// not change that outcome into a hang or a registered peer.
	if _, err := b.AcceptContext(ctx, pion.SessionDescription{}); err == nil {
		t.Fatal("an empty offer was accepted")
	}
	if b.Viewers() != 0 {
		t.Fatal("a failed Accept registered a viewer")
	}
	b.mu.Lock()
	pending := b.pending
	b.mu.Unlock()
	if pending != 0 {
		t.Fatalf("pending = %d after Accept returned, want 0", pending)
	}
}
