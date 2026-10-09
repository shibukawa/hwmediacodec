package ebitenvideo

import (
	"testing"
	"time"

	"github.com/shibukawa/hwmediacodec"
)

type fakeFeed struct {
	items    []item
	released []int
}

func (f *fakeFeed) frame(index, loop int) item {
	fr := &hwmediacodec.Frame{Width: 1, Height: 1}
	return item{frame: fr, index: index, loop: loop, release: func() { f.released = append(f.released, loop*1000+index) }}
}

func (f *fakeFeed) add(n, loop int) {
	for i := 0; i < n; i++ {
		f.items = append(f.items, f.frame(i, loop))
	}
}

func (f *fakeFeed) next() (item, bool) {
	if len(f.items) == 0 {
		return item{}, false
	}
	it := f.items[0]
	f.items = f.items[1:]
	return it, true
}

func TestTimelineShowsFramesAtTheirTime(t *testing.T) {
	feed := &fakeFeed{}
	feed.add(4, 0)
	tl := timeline{fps: 10}
	tick := 25 * time.Millisecond // 40 ticks per second against 10 fps
	var shown []int
	for i := 0; i < 16; i++ {
		show, skipped := tl.advance(tick, feed.next)
		if skipped != 0 {
			t.Fatalf("tick %d skipped %d frames", i, skipped)
		}
		if show.frame != nil {
			shown = append(shown, i)
			show.release()
		}
	}
	// The clock starts with frame 0 at tick 0; frame k is due at k*100 ms,
	// which tick 4k reaches.
	want := []int{0, 4, 8, 12}
	if len(shown) != len(want) {
		t.Fatalf("shown at ticks %v, want %v", shown, want)
	}
	for i := range want {
		if shown[i] != want[i] {
			t.Fatalf("shown at ticks %v, want %v", shown, want)
		}
	}
}

func TestTimelineSkipsWhenBehind(t *testing.T) {
	feed := &fakeFeed{}
	feed.add(10, 0)
	tl := timeline{fps: 10}
	show, skipped := tl.advance(0, feed.next)
	if show.frame == nil || show.index != 0 || skipped != 0 {
		t.Fatalf("show=%d skipped=%d, want frame 0", show.index, skipped)
	}
	show.release()
	// A 350 ms stall: frames 1, 2 and 3 are due; the newest is shown.
	show, skipped = tl.advance(350*time.Millisecond, feed.next)
	if show.frame == nil || show.index != 3 || skipped != 2 {
		t.Fatalf("show=%d skipped=%d, want frame 3 and 2 skipped", show.index, skipped)
	}
	show.release()
	// Frames 0 to 3 were released in order; 4 is pending.
	if len(feed.released) != 4 || feed.released[2] != 2 || feed.released[3] != 3 {
		t.Fatalf("released %v", feed.released)
	}
	if !tl.hasPending || tl.pending.index != 4 {
		t.Fatalf("pending index %d", tl.pending.index)
	}
	tl.release()
	if len(feed.released) != 5 || feed.released[4] != 4 {
		t.Fatalf("release() did not drop the pending frame: %v", feed.released)
	}
}

func TestTimelineWaitsForDecoder(t *testing.T) {
	feed := &fakeFeed{}
	tl := timeline{fps: 10}
	if show, _ := tl.advance(time.Second, feed.next); show.frame != nil {
		t.Fatal("a frame appeared from an empty feed")
	}
	// The clock does not run before the first frame exists: when the
	// decoder delivers, playback starts from frame 0.
	feed.add(10, 0)
	show, skipped := tl.advance(time.Second, feed.next)
	if show.frame == nil || show.index != 0 || skipped != 0 || tl.position() != 0 {
		t.Fatalf("show=%d skipped=%d position=%v", show.index, skipped, tl.position())
	}
	show.release()
	// Once started, a stall of the game loop skips frames to catch up.
	show, skipped = tl.advance(time.Second, feed.next)
	if show.frame == nil || show.index != 9 || skipped != 8 {
		t.Fatalf("show=%d skipped=%d", show.index, skipped)
	}
	show.release()
}

func TestTimelineLoopKeepsLastFrameDuration(t *testing.T) {
	feed := &fakeFeed{}
	feed.add(3, 0) // 300 ms of video at 10 fps
	feed.add(3, 1)
	tl := timeline{fps: 10}
	var shown []int
	for i := 0; i < 12; i++ {
		show, skipped := tl.advance(50*time.Millisecond, feed.next)
		if skipped != 0 {
			t.Fatalf("tick %d skipped %d", i, skipped)
		}
		if show.frame != nil {
			shown = append(shown, i)
			show.release()
		}
	}
	// The clock starts at tick 0 with frame 0; frame 1 (100 ms) shows at
	// tick 2, frame 2 at tick 4; the second iteration starts at 300 ms, so
	// its frames show at ticks 6, 8 and 10 without the restart shortening
	// frame 2.
	want := []int{0, 2, 4, 6, 8, 10}
	if len(shown) != len(want) {
		t.Fatalf("shown at ticks %v, want %v", shown, want)
	}
	for i := range want {
		if shown[i] != want[i] {
			t.Fatalf("shown at ticks %v, want %v", shown, want)
		}
	}
	// 11 ticks advanced the clock to 550 ms; 250 ms of them belong to the
	// second iteration.
	if tl.position() != 250*time.Millisecond {
		t.Fatalf("position %v, want 250ms", tl.position())
	}
}
