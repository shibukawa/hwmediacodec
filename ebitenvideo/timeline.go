package ebitenvideo

import (
	"math"
	"time"
)

// timeline paces frames: it keeps a playback position and decides which
// decoded frame should be on screen.
type timeline struct {
	fps float64
	pos time.Duration // position within the current loop iteration

	// started is set once the first frame is available; the clock does not
	// run before that, so decoder start-up latency does not skip frames.
	started    bool
	loop       int
	lastIndex  int
	pending    item
	hasPending bool
}

// frameTime returns the presentation time of display index i.
func (t *timeline) frameTime(i int) time.Duration {
	return time.Duration(math.Round(float64(i) * float64(time.Second) / t.fps))
}

// advance moves the position forward by dt and returns the frame that
// should be on screen now: the newest frame whose time has come. Frames
// that became due and were overtaken in the same step are released and
// counted in skipped. show.frame is nil when the picture does not change;
// the caller releases the returned item.
func (t *timeline) advance(dt time.Duration, next func() (item, bool)) (show item, skipped int) {
	if t.started {
		t.pos += dt
	}
	for {
		if !t.hasPending {
			it, ok := next()
			if !ok {
				break
			}
			t.pending, t.hasPending = it, true
			t.started = true
		}
		if t.pending.loop != t.loop {
			// The stream restarted: continue the clock from the end of the
			// previous iteration so that its last frame keeps its duration.
			t.loop = t.pending.loop
			t.pos -= t.frameTime(t.lastIndex + 1)
		}
		if t.frameTime(t.pending.index) > t.pos {
			break
		}
		if show.frame != nil {
			show.release()
			skipped++
		}
		show = t.pending
		t.lastIndex = t.pending.index
		t.hasPending = false
	}
	return show, skipped
}

// position returns the playback position within the current iteration.
func (t *timeline) position() time.Duration {
	if t.pos < 0 {
		// Between the end of one iteration and the first frame of the next.
		return 0
	}
	return t.pos
}

// release drops the frame held for the future, if any.
func (t *timeline) release() {
	if t.hasPending {
		t.pending.release()
		t.hasPending = false
	}
}
