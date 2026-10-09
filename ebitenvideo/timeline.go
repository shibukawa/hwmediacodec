package ebitenvideo

import "time"

// timeline paces frames: it keeps a playback position and decides which
// decoded frame should be on screen.
type timeline struct {
	pos time.Duration // position within the current loop iteration

	// started is set once the first frame is available; the clock does not
	// run before that, so decoder start-up latency does not skip frames.
	started    bool
	gen        int
	loop       int
	pending    item
	hasPending bool
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
		switch {
		case t.pending.gen != t.gen:
			// After a seek: reset already put the clock at the target.
			t.gen, t.loop = t.pending.gen, t.pending.loop
		case t.pending.loop != t.loop:
			// The stream restarted: continue the clock from the end of the
			// previous iteration so that its last frame keeps its duration.
			t.loop = t.pending.loop
			t.pos -= t.pending.rebase
		}
		if t.pending.pts > t.pos {
			break
		}
		if show.frame != nil {
			show.release()
			skipped++
		}
		show = t.pending
		t.hasPending = false
	}
	return show, skipped
}

// reset moves the clock to pos and stops it until the next frame arrives,
// which is what a seek needs.
func (t *timeline) reset(pos time.Duration) {
	t.release()
	t.pos = pos
	t.started = false
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
		t.pending.doRelease()
		t.hasPending = false
	}
}
