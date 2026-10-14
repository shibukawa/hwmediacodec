package codec

import (
	"errors"
	"math"
	"math/big"
	"math/rand"
	"testing"
	"time"
)

func TestRescaleOverflow(t *testing.T) {
	values := []int64{math.MinInt64, math.MinInt64 + 1, -1000000000000, -1, 0, 1, 1000000000000, math.MaxInt64 - 1, math.MaxInt64}
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 1000; i++ {
		values = append(values, rng.Int63(), -rng.Int63())
	}
	for _, v := range values {
		for _, scale := range [][2]int64{{48000, 90000}, {90000, 1000000000}, {1, math.MaxInt64}, {math.MaxInt64, math.MaxInt64 - 1}, {3, -2}, {3, math.MinInt64}} {
			from, to := scale[0], scale[1]
			n := new(big.Int).Mul(big.NewInt(v), big.NewInt(to))
			negative := n.Sign() < 0
			n.Abs(n).Add(n, big.NewInt(from/2)).Quo(n, big.NewInt(from))
			if negative {
				n.Neg(n)
			}
			want := n.Int64()
			if n.Cmp(big.NewInt(math.MaxInt64)) > 0 {
				want = math.MaxInt64
			} else if n.Cmp(big.NewInt(math.MinInt64)) < 0 {
				want = math.MinInt64
			}
			if got := Rescale(v, from, to); got != want {
				t.Fatalf("Rescale(%d, %d, %d) = %d, want %d", v, from, to, got, want)
			}
		}
	}
}

func TestAudioFrameInvalidLayout(t *testing.T) {
	for _, f := range []*AudioFrame{
		nil,
		{SampleRate: 48000, Channels: -1, Format: S16},
		{SampleRate: 48000, Channels: math.MaxInt/2 + 1, Format: S16, Data: make([]byte, 4)},
		{SampleRate: 48000, Channels: 2, Format: F32, Data: make([]byte, 7)},
		{SampleRate: 48000, Channels: 2, Format: SampleFormat(255)},
	} {
		if err := f.Validate(); !errors.Is(err, ErrInvalidData) {
			t.Errorf("Validate(%+v) = %v", f, err)
		}
		if n := f.Samples(); n < 0 {
			t.Errorf("Samples(%+v) = %d", f, n)
		}
	}
}

func TestRescale(t *testing.T) {
	cases := []struct{ v, from, to, want int64 }{
		{1024, 48000, 48000, 1024},
		{1024, 48000, 90000, 1920},
		{1024, 44100, 90000, 2090}, // 2089.8
		{-2112, 48000, 90000, -3960},
		{-1, 44100, 90000, -2}, // -2.04
		{1, 3, 1, 0},           // 0.33
		{2, 3, 1, 1},           // 0.67
		{-2, 3, 1, -1},
		{3, 2, 1, 2}, // halves away from zero
		{-3, 2, 1, -2},
		{7, 0, 5, 7}, // no source unit: left alone
	}
	for _, c := range cases {
		if got := Rescale(c.v, c.from, c.to); got != c.want {
			t.Errorf("Rescale(%d, %d, %d) = %d, want %d", c.v, c.from, c.to, got, c.want)
		}
	}
}

func TestAudioFrame(t *testing.T) {
	samples := []int16{1, -2, 3, -4, 5, -6}
	f := S16Frame(samples, 48000, 2, 99)
	if f.Samples() != 3 || f.Format != S16 || f.PTS != 99 || len(f.Data) != 12 {
		t.Fatalf("S16Frame: %+v", f)
	}
	if f.Duration() != 3*time.Second/48000 {
		t.Errorf("duration %v", f.Duration())
	}
	if got := S16Frame([]int16{1}, 44100, 1, 0).Duration(); got != time.Second/44100 {
		t.Errorf("duration must truncate fractional nanoseconds: %v", got)
	}
	// The frame shares the slice, and so does the view back.
	samples[1] = 7
	view := f.Int16s()
	if len(view) != 6 || view[1] != 7 || &view[0] != &samples[0] {
		t.Errorf("Int16s does not share the samples: %v", view)
	}
	if f.Float32s() != nil {
		t.Error("an S16 frame has Float32s")
	}
	// Bytes at an odd address are copied instead.
	odd := &AudioFrame{SampleRate: 48000, Channels: 1, Format: S16, Data: append([]byte{0}, f.Data...)[1:]}
	if got := odd.Int16s(); len(got) != 6 || got[0] != 1 || got[1] != 7 || got[5] != -6 {
		t.Errorf("Int16s of unaligned data: %v", got)
	}

	g := F32Frame([]float32{0.5, -0.25}, 44100, 1, 0)
	if g.Samples() != 2 || g.Format != F32 || g.Int16s() != nil {
		t.Fatalf("F32Frame: %+v", g)
	}
	if v := g.Float32s(); len(v) != 2 || v[0] != 0.5 || v[1] != -0.25 {
		t.Errorf("Float32s: %v", v)
	}
	if n := (&AudioFrame{}).Samples(); n != 0 {
		t.Errorf("an empty frame has %d samples", n)
	}
	if S16.BytesPerSample() != 2 || F32.BytesPerSample() != 4 || SampleFormat(0).BytesPerSample() != 0 {
		t.Error("BytesPerSample")
	}
	if !AAC.IsAudio() || !Opus.IsAudio() || H264.IsAudio() || AV1.IsAudio() {
		t.Error("IsAudio")
	}
}
