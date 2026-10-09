package ebitenvideo

import (
	"testing"
	"time"

	"github.com/shibukawa/hwmediacodec"
)

const testFPS = 10

func at(index int) time.Duration { return time.Duration(index) * time.Second / testFPS }

type fakeFeed struct {
	items    []item
	released []int
}

func (f *fakeFeed) frame(index, loop int) item {
	fr := &hwmediacodec.Frame{Width: 1, Height: 1}
	return item{frame: fr, pts: at(index), loop: loop, release: func() { f.released = append(f.released, loop*1000+index) }}
}

// add appends n frames of the given loop iteration; iterations after the
// first carry the previous iteration's length, as the source does.
func (f *fakeFeed) add(n, loop int) {
	for i := 0; i < n; i++ {
		it := f.frame(i, loop)
		if loop > 0 {
			it.rebase = at(3)
		}
		f.items = append(f.items, it)
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

func index(it item) int { return int(it.pts * testFPS / time.Second) }

func TestTimelineShowsFramesAtTheirTime(t *testing.T) {
	feed := &fakeFeed{}
	feed.add(4, 0)
	var tl timeline
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
	var tl timeline
	show, skipped := tl.advance(0, feed.next)
	if show.frame == nil || index(show) != 0 || skipped != 0 {
		t.Fatalf("show=%d skipped=%d, want frame 0", index(show), skipped)
	}
	show.release()
	// A 350 ms stall: frames 1, 2 and 3 are due; the newest is shown.
	show, skipped = tl.advance(350*time.Millisecond, feed.next)
	if show.frame == nil || index(show) != 3 || skipped != 2 {
		t.Fatalf("show=%d skipped=%d, want frame 3 and 2 skipped", index(show), skipped)
	}
	show.release()
	// Frames 0 to 3 were released in order; 4 is pending.
	if len(feed.released) != 4 || feed.released[2] != 2 || feed.released[3] != 3 {
		t.Fatalf("released %v", feed.released)
	}
	if !tl.hasPending || index(tl.pending) != 4 {
		t.Fatalf("pending index %d", index(tl.pending))
	}
	tl.release()
	if len(feed.released) != 5 || feed.released[4] != 4 {
		t.Fatalf("release() did not drop the pending frame: %v", feed.released)
	}
}

func TestTimelineWaitsForDecoder(t *testing.T) {
	feed := &fakeFeed{}
	var tl timeline
	if show, _ := tl.advance(time.Second, feed.next); show.frame != nil {
		t.Fatal("a frame appeared from an empty feed")
	}
	// The clock does not run before the first frame exists: when the
	// decoder delivers, playback starts from frame 0.
	feed.add(10, 0)
	show, skipped := tl.advance(time.Second, feed.next)
	if show.frame == nil || index(show) != 0 || skipped != 0 || tl.position() != 0 {
		t.Fatalf("show=%d skipped=%d position=%v", index(show), skipped, tl.position())
	}
	show.release()
	// Once started, a stall of the game loop skips frames to catch up.
	show, skipped = tl.advance(time.Second, feed.next)
	if show.frame == nil || index(show) != 9 || skipped != 8 {
		t.Fatalf("show=%d skipped=%d", index(show), skipped)
	}
	show.release()
}

func TestTimelineLoopKeepsLastFrameDuration(t *testing.T) {
	feed := &fakeFeed{}
	feed.add(3, 0) // 300 ms of video at 10 fps
	feed.add(3, 1)
	var tl timeline
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

func TestTimelineSeekResetsClock(t *testing.T) {
	feed := &fakeFeed{}
	feed.add(5, 0)
	var tl timeline
	show, _ := tl.advance(0, feed.next)
	show.release()
	tl.advance(100*time.Millisecond, feed.next) // frame 1 shown, frame 2 pending
	if !tl.hasPending {
		t.Fatal("no pending frame before the seek")
	}
	// A seek to 2 s: the pending frame is released, the clock sits at the
	// target and does not run until a frame of the new generation comes.
	tl.reset(2 * time.Second)
	if tl.hasPending || tl.position() != 2*time.Second {
		t.Fatalf("after reset: pending %v position %v", tl.hasPending, tl.position())
	}
	// The player filters frames of the old generation out before they
	// reach the timeline; while nothing new has arrived the clock waits.
	feed.items = nil
	if show, _ := tl.advance(time.Second, feed.next); show.frame != nil {
		t.Fatal("a frame appeared from an empty feed after the seek")
	}
	if tl.position() != 2*time.Second {
		t.Fatalf("clock ran without frames: %v", tl.position())
	}
	// Frames of the new generation start at the target: the one at 2.0 s
	// shows at once, the one at 2.1 s one tick later.
	for i := 20; i < 24; i++ {
		it := feed.frame(i, 0)
		it.gen = 1
		feed.items = append(feed.items, it)
	}
	show, skipped := tl.advance(50*time.Millisecond, feed.next)
	if show.frame == nil || index(show) != 20 || skipped != 0 {
		t.Fatalf("first frame after the seek: index %d skipped %d", index(show), skipped)
	}
	show.release()
	if tl.position() != 2*time.Second {
		t.Fatalf("position %v right after the seek, want 2s", tl.position())
	}
	show, _ = tl.advance(100*time.Millisecond, feed.next)
	if show.frame == nil || index(show) != 21 {
		t.Fatalf("second frame after the seek: index %d", index(show))
	}
	show.release()
}
