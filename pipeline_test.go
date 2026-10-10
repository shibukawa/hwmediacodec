package hwmediacodec_test

import (
	"context"
	"errors"
	"io"
	"slices"
	"testing"
	"time"

	"github.com/shibukawa/hwmediacodec"
)

// fakeDecoder turns every packet into a frame with the packet's PTS. It
// holds at most capacity frames (Send then answers ErrAgain), delays its
// output by lag packets like a reordering decoder, and counts releases.
type fakeDecoder struct {
	capacity, lag int
	queue         []int64
	ready         int // frames of queue that Receive may return
	flushed       bool
	sends, again  int
	flushes       int
}

func (d *fakeDecoder) Send(_ context.Context, p hwmediacodec.Packet) error {
	d.sends++
	if len(d.queue) >= d.capacity {
		d.again++
		return hwmediacodec.ErrAgain
	}
	d.flushed = false
	d.queue = append(d.queue, p.PTS)
	if len(d.queue) > d.lag {
		d.ready = len(d.queue) - d.lag
	}
	return nil
}

func (d *fakeDecoder) Receive(context.Context) (*hwmediacodec.Frame, error) {
	if d.ready == 0 {
		if d.flushed {
			return nil, io.EOF
		}
		return nil, hwmediacodec.ErrAgain
	}
	pts := d.queue[0]
	d.queue = d.queue[1:]
	d.ready--
	return &hwmediacodec.Frame{Width: 2, Height: 2, PTS: pts}, nil
}

func (d *fakeDecoder) Flush(context.Context) error {
	d.flushes++
	d.flushed = true
	d.ready = len(d.queue)
	return nil
}

func (d *fakeDecoder) Close() error { return nil }

// fakeReader hands out n packets with PTS 0, 10, 20, ...
type fakeReader struct {
	n, next int
	fail    error
}

func (r *fakeReader) Codec() hwmediacodec.Codec { return hwmediacodec.H264 }
func (r *fakeReader) TimeScale() int32          { return 1000 }
func (r *fakeReader) ReadPacket() (hwmediacodec.Packet, error) {
	if r.fail != nil && r.next == r.n/2 {
		return hwmediacodec.Packet{}, r.fail
	}
	if r.next >= r.n {
		return hwmediacodec.Packet{}, io.EOF
	}
	r.next++
	return hwmediacodec.Packet{Data: []byte{1}, PTS: int64(r.next-1) * 10}, nil
}
func (r *fakeReader) SeekKeyframe(t time.Duration) (time.Duration, error) {
	r.next = int(t / (10 * time.Millisecond))
	return t, nil
}
func (r *fakeReader) Length() time.Duration { return time.Duration(r.n) * 10 * time.Millisecond }

var _ hwmediacodec.PacketSeeker = (*fakeReader)(nil)

func readAll(t *testing.T, r hwmediacodec.FrameReader) []int64 {
	t.Helper()
	var pts []int64
	for {
		f, err := r.ReadFrame(context.Background())
		if err == io.EOF {
			return pts
		}
		if err != nil {
			t.Fatal(err)
		}
		pts = append(pts, f.PTS)
		f.Release()
	}
}

func wantPTS(from, to int) []int64 {
	var out []int64
	for i := from; i < to; i++ {
		out = append(out, int64(i)*10)
	}
	return out
}

func TestDecodeReader(t *testing.T) {
	for _, tc := range []struct {
		name          string
		capacity, lag int
	}{
		{"one in one out", 1, 0},
		{"reordering decoder", 8, 3},
		{"full output queue", 2, 1}, // Send answers ErrAgain until Receive made room
	} {
		t.Run(tc.name, func(t *testing.T) {
			dec := &fakeDecoder{capacity: tc.capacity, lag: tc.lag}
			src := &fakeReader{n: 25}
			r := hwmediacodec.NewDecodeReader(dec, src)
			if got := readAll(t, r); !slices.Equal(got, wantPTS(0, 25)) {
				t.Errorf("frames %v", got)
			}
			if dec.flushes != 1 {
				t.Errorf("the decoder was flushed %d times, want once at the end of the stream", dec.flushes)
			}
			if dec.sends-dec.again != 25 {
				t.Errorf("%d packets accepted, want 25 (none lost or repeated around ErrAgain)", dec.sends-dec.again)
			}
			// The end is sticky.
			if _, err := r.ReadFrame(context.Background()); err != io.EOF {
				t.Errorf("after the end: %v, want io.EOF", err)
			}
		})
	}
}

func TestDecodeReaderReset(t *testing.T) {
	dec := &fakeDecoder{capacity: 8, lag: 3}
	src := &fakeReader{n: 30}
	r := hwmediacodec.NewDecodeReader(dec, src)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		f, err := r.ReadFrame(ctx)
		if err != nil {
			t.Fatal(err)
		}
		f.Release()
	}
	// Seek: the frames the decoder still holds belong to the old position.
	if _, err := src.SeekKeyframe(200 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := r.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, r); !slices.Equal(got, wantPTS(20, 30)) {
		t.Errorf("after the seek: %v, want frames 20..29", got)
	}
	// A reader at its end can be reset and read again.
	src.SeekKeyframe(0)
	if err := r.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, r); len(got) != 30 {
		t.Errorf("after rewinding: %d frames, want 30", len(got))
	}
}

func TestDecodeReaderErrors(t *testing.T) {
	boom := errors.New("boom")
	r := hwmediacodec.NewDecodeReader(&fakeDecoder{capacity: 4}, &fakeReader{n: 10, fail: boom})
	var n int
	for {
		f, err := r.ReadFrame(context.Background())
		if err != nil {
			if !errors.Is(err, boom) {
				t.Errorf("error %v, want the reader's", err)
			}
			break
		}
		f.Release()
		n++
	}
	if n != 5 {
		t.Errorf("%d frames before the error, want 5", n)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := hwmediacodec.NewDecodeReader(&fakeDecoder{capacity: 4}, &fakeReader{n: 10}).ReadFrame(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled context: %v", err)
	}
}

// fakeEncoder turns every frame into a packet. It holds at most capacity
// packets (Send then answers ErrAgain) and keeps lag frames back until
// Flush, like an encoder with look-ahead.
type fakeEncoder struct {
	capacity, lag int
	queue         []int64
	ready         int
	flushed       bool
	sends, again  int
}

func (e *fakeEncoder) Send(_ context.Context, f *hwmediacodec.Frame) error {
	e.sends++
	if e.ready >= e.capacity {
		e.again++
		return hwmediacodec.ErrAgain
	}
	e.flushed = false
	e.queue = append(e.queue, f.PTS)
	if len(e.queue) > e.lag {
		e.ready = len(e.queue) - e.lag
	}
	return nil
}

func (e *fakeEncoder) Receive(context.Context) (hwmediacodec.Packet, error) {
	if e.ready == 0 {
		if e.flushed {
			return hwmediacodec.Packet{}, io.EOF
		}
		return hwmediacodec.Packet{}, hwmediacodec.ErrAgain
	}
	pts := e.queue[0]
	e.queue = e.queue[1:]
	e.ready--
	return hwmediacodec.Packet{Data: []byte{2}, PTS: pts}, nil
}

func (e *fakeEncoder) Flush(context.Context) error {
	e.flushed = true
	e.ready = len(e.queue)
	return nil
}

func (e *fakeEncoder) Close() error { return nil }

func TestEncodeWriter(t *testing.T) {
	ctx := context.Background()
	for _, lag := range []int{0, 4} {
		enc := &fakeEncoder{capacity: 2, lag: lag}
		var got []int64
		w := hwmediacodec.NewEncodeWriter(enc, hwmediacodec.PacketWriterFunc(func(p hwmediacodec.Packet) error {
			got = append(got, p.PTS)
			return nil
		}))
		for i := 0; i < 20; i++ {
			if err := w.WriteFrame(ctx, &hwmediacodec.Frame{PTS: int64(i) * 10}); err != nil {
				t.Fatal(err)
			}
		}
		if lag > 0 && len(got) != 20-lag {
			t.Errorf("lag %d: %d packets before Flush, want %d", lag, len(got), 20-lag)
		}
		if err := w.Flush(ctx); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, wantPTS(0, 20)) || w.Packets() != 20 {
			t.Errorf("lag %d: packets %v, counted %d", lag, got, w.Packets())
		}
	}

	// The writer's error stops the frame that caused it.
	boom := errors.New("disk full")
	w := hwmediacodec.NewEncodeWriter(&fakeEncoder{capacity: 2}, hwmediacodec.PacketWriterFunc(func(hwmediacodec.Packet) error { return boom }))
	if err := w.WriteFrame(ctx, &hwmediacodec.Frame{}); !errors.Is(err, boom) {
		t.Errorf("WriteFrame: %v, want the writer's error", err)
	}
}

func TestCopyFrames(t *testing.T) {
	ctx := context.Background()
	r := hwmediacodec.NewDecodeReader(&fakeDecoder{capacity: 4, lag: 2}, &fakeReader{n: 40})
	enc := &fakeEncoder{capacity: 3, lag: 1}
	var got []int64
	w := hwmediacodec.NewEncodeWriter(enc, hwmediacodec.PacketWriterFunc(func(p hwmediacodec.Packet) error {
		got = append(got, p.PTS)
		return nil
	}))
	n, err := hwmediacodec.CopyFrames(ctx, w, r)
	if err != nil || n != 40 {
		t.Fatalf("CopyFrames: %d %v", n, err)
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, wantPTS(0, 40)) {
		t.Errorf("transcoded packets %v", got)
	}
}
