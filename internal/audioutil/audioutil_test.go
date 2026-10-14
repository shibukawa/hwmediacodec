package audioutil

import (
	"bytes"
	"testing"

	"github.com/shibukawa/hwmediacodec/internal/codec"
)

// TestTrialMeasurement checks the arithmetic of the encoder trial on
// made-up decoder output: where the signal starts and how much of its
// start is missing.
func TestTrialMeasurement(t *testing.T) {
	signal := TrialSignal()
	for _, v := range signal {
		if v < -0.25 || v > 0.25 {
			t.Fatalf("trial signal sample %g outside [-0.25, 0.25]", v)
		}
	}
	shifted := func(delay, lost int) []float32 {
		out := make([]float32, delay+len(signal)+TrialTail)
		copy(out[delay:], signal)
		clear(out[delay : delay+lost])
		// A codec's output is not the input: add a little of something else.
		for i := range out {
			out[i] += 0.01 * float32((i*7919)%13-6) / 6
		}
		return out
	}
	for _, c := range []struct{ delay, lost, wantLead int }{
		{2112, 0, 0},
		{1600, 0, 0},
		{312, 0, 0},
		{0, 0, 0},
		{2112, 1024, 1024},
		{1600, 1000, 1024},
	} {
		decoded := shifted(c.delay, c.lost)
		delay, ok := SignalDelay(signal, decoded, 1024)
		if !ok || delay != c.delay {
			t.Errorf("delay %d with %d lost: measured %d (found %v)", c.delay, c.lost, delay, ok)
			continue
		}
		if lead := LostLead(signal, decoded, delay); lead != c.wantLead {
			t.Errorf("delay %d with %d lost: lead %d, want %d", c.delay, c.lost, lead, c.wantLead)
		}
	}
	// Output that has nothing to do with the signal gives no delay.
	unrelated := make([]float32, len(signal)+TrialTail)
	for i := range unrelated {
		unrelated[i] = float32((i*31)%17-8) / 64
	}
	if delay, ok := SignalDelay(signal, unrelated, 1024); ok {
		t.Errorf("a delay of %d was found in unrelated samples", delay)
	}
}

func TestFrames(t *testing.T) {
	f := Frames{SampleRate: 48000, Channels: 2, Format: codec.S16}
	sample := func(n int, v byte) []byte { return bytes.Repeat([]byte{v}, n*4) }
	// A decoder that runs one and a half packets behind and cuts its
	// output where it likes.
	f.Expect(100, 4)
	f.Expect(200, 4)
	f.Expect(300, 2)
	f.Append(sample(3, 1))
	if f.Ready() != 0 || f.Waiting() != 3 {
		t.Fatalf("after 3 of 4 samples: %d frames ready, %d packets waiting", f.Ready(), f.Waiting())
	}
	f.Append(append(sample(1, 1), sample(5, 2)...))
	if f.Ready() != 2 {
		t.Fatalf("after 9 samples: %d frames ready", f.Ready())
	}
	first, second := f.Next(), f.Next()
	if first.PTS != 100 || first.Samples() != 4 || !bytes.Equal(first.Data, sample(4, 1)) || first.SampleRate != 48000 || first.Channels != 2 || first.Format != codec.S16 {
		t.Errorf("first frame: %+v", first)
	}
	if second.PTS != 200 || !bytes.Equal(second.Data, sample(4, 2)) {
		t.Errorf("second frame: PTS %d, %v", second.PTS, second.Data)
	}
	// The stream ends short of the last packet's samples.
	f.End()
	last := f.Next()
	if last == nil || last.PTS != 300 || last.Samples() != 1 || f.Next() != nil || f.Waiting() != 0 {
		t.Errorf("last frame: %+v", last)
	}

	// Silence in front for a decoder that keeps its first samples back.
	f.Expect(1, 4)
	f.Pad(1)
	f.Append(sample(3, 7))
	if g := f.Next(); g == nil || !bytes.Equal(g.Data, append(sample(1, 0), sample(3, 7)...)) {
		t.Errorf("padded frame: %+v", g)
	}

	// Twice the rate, twice the samples per packet.
	f.Expect(1, 2)
	f.Scale(2, 1)
	f.Append(sample(4, 9))
	if g := f.Next(); g == nil || g.Samples() != 4 {
		t.Errorf("scaled frame: %+v", g)
	}
	f.Expect(1, 2)
	f.Append(sample(1, 3))
	f.Reset()
	f.Append(sample(8, 3))
	if f.Ready() != 0 {
		t.Errorf("samples after Reset became a frame")
	}
}

func TestConvert(t *testing.T) {
	s16 := []byte{0x00, 0x40, 0x00, 0xC0, 0xFF, 0x7F, 0x00, 0x80} // 0.5, -0.5, max, min
	f32 := S16ToF32(s16)
	if back := F32ToS16(f32); !bytes.Equal(back, s16) {
		t.Errorf("S16 -> F32 -> S16: % x", back)
	}
	if got := Convert(s16, codec.S16, codec.S16); &got[0] != &s16[0] {
		t.Error("Convert copied samples that needed no conversion")
	}
	if !bytes.Equal(Convert(s16, codec.S16, codec.F32), f32) || !bytes.Equal(Convert(f32, codec.F32, codec.S16), s16) {
		t.Error("Convert differs from the conversions")
	}
	// Out of range floats are clipped.
	loud := S16ToF32([]byte{0xFF, 0x7F})
	loud[3] = 0x40 // 2.something
	if got := F32ToS16(loud); !bytes.Equal(got, []byte{0xFF, 0x7F}) {
		t.Errorf("a float above 1 became % x", got)
	}
}

func TestPacketTimes(t *testing.T) {
	// 48 kHz in 90 kHz units: 15 units for every 8 samples.
	pt := PacketTimes{SampleRate: 48000, TimeScale: 90000, Frame: 1024, Delay: 2112}
	pt.Start(1000)
	pt.Sent(3000)
	want := []struct {
		pts, dur int64
		keep     bool
	}{
		{1000 - 3960, 1920, true}, // samples -2112..-1088
		{1000 - 2040, 1920, true}, // -1088..-64
		{1000 - 120, 1920, true},  // -64..960
		{1000 + 1800, 1920, true}, // 960..1984
		{1000 + 3720, 1905, true}, // 1984..3000: cut at the last sample
		{0, 0, false},             // 3008..: silence only
	}
	for i, w := range want {
		p, keep := pt.Next([]byte{byte(i)})
		if keep != w.keep || (keep && (p.PTS != w.pts || p.DTS != w.pts || p.Duration != w.dur || !p.Keyframe || p.Data[0] != byte(i))) {
			t.Errorf("packet %d: PTS %d duration %d keep %v; want PTS %d duration %d keep %v", i, p.PTS, p.Duration, keep, w.pts, w.dur, w.keep)
		}
	}
	if pt.Samples() != 3000 {
		t.Errorf("samples %d", pt.Samples())
	}
	// A new signal starts the count again.
	pt.Start(0)
	pt.Sent(10)
	if p, keep := pt.Next(nil); !keep || p.PTS != -3960 {
		t.Errorf("first packet of the second signal: PTS %d keep %v", p.PTS, keep)
	}
}
