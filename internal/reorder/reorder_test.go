package reorder

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sort"
	"testing"

	"github.com/shibukawa/hwmediacodec/bitstream/annexb"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
)

func frame(pts int64, seq uint32, poc int32, reorder uint8, released *[]int64) *codec.Frame {
	f := &codec.Frame{PTS: pts, Planes: [][]byte{{1}}}
	codec.SetFrameOrder(f, codec.Order{Seq: seq, POC: poc, Reorder: reorder})
	if released != nil {
		codec.SetRelease(f, func() { *released = append(*released, pts) })
	}
	return f
}

func TestBufferBumping(t *testing.T) {
	// x264-style decode order with a reorder bound of 2: I P B b P B b b.
	pocs := []int32{0, 6, 2, 4, 14, 10, 8, 12}
	var b buffer
	var out []int32
	for i, poc := range pocs {
		b.push(frame(int64(i), 1, poc, 2, nil))
		for f := b.pop(false); f != nil; f = b.pop(false) {
			out = append(out, codec.FrameOrder(f).POC)
		}
	}
	// With bound 2 the third frame releases the first, so after 8 frames
	// 6 have left, in POC order.
	if want := []int32{0, 2, 4, 6, 8, 10}; !equal(out, want) {
		t.Fatalf("released %v, want %v", out, want)
	}
	for f := b.pop(true); f != nil; f = b.pop(true) {
		out = append(out, codec.FrameOrder(f).POC)
	}
	if want := []int32{0, 2, 4, 6, 8, 10, 12, 14}; !equal(out, want) {
		t.Fatalf("drained %v, want %v", out, want)
	}
}

func TestBufferNewSequenceFlushesOld(t *testing.T) {
	var b buffer
	b.push(frame(0, 1, 0, 4, nil))
	b.push(frame(1, 1, 4, 4, nil))
	b.push(frame(2, 1, 2, 4, nil))
	if f := b.pop(false); f != nil {
		t.Fatalf("frame %d left before the bound was reached", f.PTS)
	}
	b.push(frame(3, 2, 0, 4, nil)) // IDR starts a new sequence
	var got []int64
	for f := b.pop(false); f != nil; f = b.pop(false) {
		got = append(got, f.PTS)
	}
	if want := []int64{0, 2, 1}; !equal(got, want) {
		t.Fatalf("old sequence left as %v, want %v", got, want)
	}
	if b.len() != 1 {
		t.Fatalf("new sequence frame should wait, buffer has %d", b.len())
	}
}

func TestBufferZeroReorderIsImmediate(t *testing.T) {
	var b buffer
	for i := 0; i < 3; i++ {
		b.push(frame(int64(i), 1, int32(i), 0, nil))
		f := b.pop(false)
		if f == nil || f.PTS != int64(i) {
			t.Fatalf("frame %d was held back", i)
		}
	}
}

func TestBufferRelease(t *testing.T) {
	var released []int64
	var b buffer
	b.push(frame(7, 1, 0, 3, &released))
	b.push(frame(8, 1, 2, 3, &released))
	b.release()
	if !equal(released, []int64{7, 8}) || b.len() != 0 {
		t.Fatalf("released %v, len %d", released, b.len())
	}
}

func equal[T comparable](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// fakeDecoder is a decode-order backend: every forwarded packet with a
// picture becomes one frame carrying the packet's PTS and order.
type fakeDecoder struct {
	codec   codec.Codec
	out     []*codec.Frame
	flushed bool
	closed  bool
	sent    int
}

func (d *fakeDecoder) Send(ctx context.Context, p codec.Packet) error {
	if d.closed {
		return codec.ErrClosed
	}
	d.flushed = false
	vcl := false
	for _, nal := range annexb.Split(p.Data) {
		if annexb.IsVCL(d.codec, annexb.NALUnitType(d.codec, nal)) {
			vcl = true
		}
	}
	if !vcl {
		return nil
	}
	d.sent++
	f := &codec.Frame{PTS: p.PTS, Planes: [][]byte{{0}}}
	codec.SetFrameOrder(f, codec.PacketOrder(p))
	d.out = append(d.out, f)
	return nil
}

func (d *fakeDecoder) Receive(ctx context.Context) (*codec.Frame, error) {
	if d.closed {
		return nil, codec.ErrClosed
	}
	if len(d.out) == 0 {
		if d.flushed {
			return nil, io.EOF
		}
		return nil, codec.ErrAgain
	}
	f := d.out[0]
	d.out = d.out[1:]
	return f, nil
}

func (d *fakeDecoder) Flush(ctx context.Context) error { d.flushed = true; return nil }
func (d *fakeDecoder) Close() error                    { d.closed = true; return nil }

// TestDisplayOrderMatchesTimestamps runs real elementary streams through
// the tracker and buffer on top of a fake decoder and compares the output
// order with the presentation timestamps ffmpeg wrote into an MP4 of the
// same stream.
func TestDisplayOrderMatchesTimestamps(t *testing.T) {
	cases := []struct {
		codec   codec.Codec
		bframes int
	}{
		{codec.H264, 0}, {codec.H264, 3}, {codec.H264, 8},
		{codec.HEVC, 0}, {codec.HEVC, 3}, {codec.HEVC, 6},
	}
	for _, c := range cases {
		t.Run(c.codec.String()+"-bf"+string(rune('0'+c.bframes)), func(t *testing.T) {
			const frames = 60
			s, pts := testutil.GenerateStreamWithTimestamps(t, c.codec, 160, 120, frames, c.bframes)
			want := make([]int64, frames)
			for i := range want {
				want[i] = int64(i)
			}
			sort.SliceStable(want, func(i, j int) bool { return pts[want[i]] < pts[want[j]] })

			ctx := context.Background()
			dec := Wrap(&fakeDecoder{codec: c.codec}, c.codec)
			r := annexb.NewReader(bytes.NewReader(testutil.ReadFile(t, s.Path)), c.codec)
			var got []int64
			var index int64
			maxHeld := 0
			for {
				au, err := r.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := dec.Send(ctx, codec.Packet{Data: au, PTS: index}); err != nil {
					t.Fatalf("Send %d: %v", index, err)
				}
				index++
				for {
					f, err := dec.Receive(ctx)
					if errors.Is(err, codec.ErrAgain) {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					got = append(got, f.PTS)
				}
				if held := dec.buf.len(); held > maxHeld {
					maxHeld = held
				}
			}
			if err := dec.Flush(ctx); err != nil {
				t.Fatal(err)
			}
			for {
				f, err := dec.Receive(ctx)
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				got = append(got, f.PTS)
			}
			if index != frames {
				t.Fatalf("stream has %d access units, want %d", index, frames)
			}
			t.Logf("%d frames, at most %d held back", len(got), maxHeld)
			if !equal(got, want) {
				t.Fatalf("display order\n got %v\nwant %v", got, want)
			}
			if c.bframes == 0 && maxHeld > 0 {
				t.Errorf("a stream without B-frames held %d frames back", maxHeld)
			}
			if _, err := dec.Receive(ctx); err != io.EOF {
				t.Fatalf("after drain Receive = %v, want io.EOF", err)
			}
			if err := dec.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := dec.Receive(ctx); !errors.Is(err, codec.ErrClosed) {
				t.Fatalf("after Close Receive = %v", err)
			}
		})
	}
}

func TestFlushResetsSequence(t *testing.T) {
	s := testutil.GenerateStream(t, codec.H264, 160, 120, 10)
	data := testutil.ReadFile(t, s.Path)
	ctx := context.Background()
	inner := &fakeDecoder{codec: codec.H264}
	dec := Wrap(inner, codec.H264)
	feed := func() []uint32 {
		r := annexb.NewReader(bytes.NewReader(data), codec.H264)
		var seqs []uint32
		for {
			au, err := r.Next()
			if err == io.EOF {
				break
			}
			if err := dec.Send(ctx, codec.Packet{Data: au}); err != nil {
				t.Fatal(err)
			}
			for {
				f, err := dec.Receive(ctx)
				if errors.Is(err, codec.ErrAgain) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				seqs = append(seqs, codec.FrameOrder(f).Seq)
			}
		}
		if err := dec.Flush(ctx); err != nil {
			t.Fatal(err)
		}
		for {
			f, err := dec.Receive(ctx)
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			seqs = append(seqs, codec.FrameOrder(f).Seq)
		}
		return seqs
	}
	first := feed()
	second := feed()
	if len(first) != 10 || len(second) != 10 {
		t.Fatalf("got %d and %d frames", len(first), len(second))
	}
	for _, s := range first {
		if s != 1 {
			t.Fatalf("first pass sequences %v", first)
		}
	}
	for _, s := range second {
		if s != 2 {
			t.Fatalf("second pass sequences %v", second)
		}
	}
}
