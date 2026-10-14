package capture

import (
	"image"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/shibukawa/hwmediacodec"
)

type staticSource struct{}

func (staticSource) Bounds() image.Rectangle { return image.Rect(0, 0, 1, 1) }
func (staticSource) ReadPixels(p []byte)     { clear(p) }

// Exercises capture/close coordination without opening hardware.
func TestRecorderConcurrentCloseAndCapture(t *testing.T) {
	r := &Recorder{opts: Options{FPS: 60}, width: 1, height: 1, step: 1500,
		free: make(chan []byte, 2), frames: make(chan frame, 1), done: make(chan struct{}), lastPTS: -1}
	r.free <- make([]byte, 4)
	r.free <- make([]byte, 4)
	go func() {
		for f := range r.frames {
			r.free <- f.pix
		}
		close(r.done)
	}()
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		start := time.Now()
		for i := range 1000 {
			r.CaptureAt(staticSource{}, start.Add(time.Duration(i)*time.Second/60))
		}
	}()
	go func() {
		defer wg.Done()
		for range 1000 {
			r.Duration()
		}
	}()
	go func() {
		defer wg.Done()
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	}()
	wg.Wait()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	r.CaptureAt(nil, time.Now()) // a closed recorder must ignore the source
}

func TestRecorderRejectsInvalidAllocations(t *testing.T) {
	sink := hwmediacodec.PacketWriterFunc(func(hwmediacodec.Packet) error { return nil })
	for _, c := range []struct {
		w, h int
		o    Options
	}{
		{math.MaxInt, 2, Options{}},
		{2, 2, Options{Queue: math.MaxInt}},
		{2, 2, Options{FPS: math.NaN()}},
		{2, 2, Options{FPS: math.Inf(1)}},
	} {
		if _, err := New(c.w, c.h, sink, c.o); err == nil {
			t.Fatalf("invalid recorder options accepted: %+v", c)
		}
	}
}
