package reorder

import "github.com/shibukawa/hwmediacodec/internal/codec"

// buffer holds decoded frames until they may leave in display order.
type buffer struct {
	entries []entry
}

type entry struct {
	f *codec.Frame
	o codec.Order
}

// push adds a frame that arrived in decode order.
func (b *buffer) push(f *codec.Frame) {
	b.entries = append(b.entries, entry{f: f, o: codec.FrameOrder(f)})
}

func (b *buffer) len() int { return len(b.entries) }

// pop returns the next frame in display order when the reorder rules allow
// one to leave: when the buffer holds frames of an older sequence than the
// newest frame, when it holds more frames than the reorder bound, or always
// when drain is set.
func (b *buffer) pop(drain bool) *codec.Frame {
	if len(b.entries) == 0 {
		return nil
	}
	// Frames arrive in decode order, so the oldest sequence is at the front.
	oldest := b.entries[0].o.Seq
	newest := b.entries[len(b.entries)-1].o.Seq
	if drain || oldest != newest || len(b.entries) > int(b.entries[0].o.Reorder) {
		return b.popOldest()
	}
	return nil
}

// popOldest removes and returns the frame with the lowest POC of the oldest
// sequence.
func (b *buffer) popOldest() *codec.Frame {
	oldest := b.entries[0].o.Seq
	best := 0
	for i := 1; i < len(b.entries); i++ {
		e := b.entries[i]
		if e.o.Seq != oldest {
			break
		}
		if e.o.POC < b.entries[best].o.POC {
			best = i
		}
	}
	f := b.entries[best].f
	copy(b.entries[best:], b.entries[best+1:])
	b.entries[len(b.entries)-1] = entry{}
	b.entries = b.entries[:len(b.entries)-1]
	return f
}

// release returns every buffered frame to its decoder.
func (b *buffer) release() {
	for _, e := range b.entries {
		e.f.Release()
	}
	b.entries = nil
}
