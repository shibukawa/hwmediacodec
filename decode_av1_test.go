package hwmediacodec_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"math"
	"testing"

	"github.com/shibukawa/hwmediacodec"
	"github.com/shibukawa/hwmediacodec/internal/av1"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
)

// av1Structure counts the hidden frames (show_frame=0) and the
// show_existing_frame units of a list of temporal units.
func av1Structure(t *testing.T, units [][]byte) (hidden, existing int) {
	t.Helper()
	var seq *av1.SequenceHeader
	for i, data := range units {
		tu, err := av1.ParseTemporalUnit(data, seq)
		if err != nil {
			t.Fatalf("temporal unit %d: %v", i, err)
		}
		if tu.Sequence != nil {
			seq = tu.Sequence
		}
		if tu.ShowExistingFrame {
			existing++
		}
		for _, o := range tu.OBUs {
			if o.Type != av1.OBUFrame && o.Type != av1.OBUFrameHeader {
				continue
			}
			if fh, err := av1.ParseFrameHeader(o.Payload, seq.ReducedStillPictureHeader); err == nil && !fh.ShowExistingFrame && !fh.ShowFrame {
				hidden++
			}
		}
	}
	return hidden, existing
}

func checksums(frames []decodedFrame) [][32]byte {
	out := make([][32]byte, len(frames))
	for i, f := range frames {
		out[i] = sha256.Sum256(f.pix)
	}
	return out
}

// TestDecodeAV1MatchesReference decodes an SVT-AV1 stream whose GOPs hold
// hidden alternate reference frames shown later through
// show_existing_frame and compares every NV12 frame with libdav1d's output
// bit for bit: AV1 decoding is bit-exact across conformant decoders when
// film grain is off. Each temporal unit yields exactly one frame, in
// order, carrying the packet's PTS.
func TestDecodeAV1MatchesReference(t *testing.T) {
	requireHardwareDecode(t, hwmediacodec.AV1)
	s := testutil.GenerateStream(t, hwmediacodec.AV1, 320, 240, 60)
	stream := testutil.ReadFile(t, s.Path)
	units := splitStream(t, hwmediacodec.AV1, stream)
	hidden, existing := av1Structure(t, units)
	if hidden == 0 || existing == 0 {
		t.Fatalf("the stream has %d hidden frames and %d show_existing_frame units; the test needs both", hidden, existing)
	}
	want := testutil.ReferenceNV12(t, s.Path, s.Codec, s.Width, s.Height)
	if len(want) != len(units) {
		t.Fatalf("ffmpeg decoded %d frames from %d temporal units", len(want), len(units))
	}

	dec, err := hwmediacodec.NewDecoder(context.Background(), hwmediacodec.AV1)
	if err != nil {
		t.Fatalf("NewDecoder: %v", err)
	}
	defer dec.Close()
	got := decodePackets(t, dec, units, hwmediacodec.NV12)
	if len(got) != len(units) {
		t.Fatalf("decoded %d frames from %d temporal units", len(got), len(units))
	}
	for i, f := range got {
		if f.width != 320 || f.height != 240 {
			t.Fatalf("frame %d is %dx%d", i, f.width, f.height)
		}
		if f.pts != int64(i)*3000 {
			t.Fatalf("frame %d has PTS %d, want %d (the PTS of temporal unit %d)", i, f.pts, int64(i)*3000, i)
		}
	}
	compareChecksums(t, checksums(got), want)
	t.Logf("%d frames bit-exact with libdav1d (%d hidden frames, %d show_existing_frame units)", len(got), hidden, existing)
}

// TestDecodeAV1WithoutTemporalDelimiters feeds the same stream with the
// temporal delimiter OBUs removed from every unit, as ISOBMFF samples have
// them, and expects identical frames.
func TestDecodeAV1WithoutTemporalDelimiters(t *testing.T) {
	requireHardwareDecode(t, hwmediacodec.AV1)
	s := testutil.GenerateStream(t, hwmediacodec.AV1, 160, 120, 30)
	stream := testutil.ReadFile(t, s.Path)
	want := testutil.ReferenceNV12(t, s.Path, s.Codec, s.Width, s.Height)
	var stripped [][]byte
	for _, tu := range splitStream(t, hwmediacodec.AV1, stream) {
		obus, err := av1.Split(tu)
		if err != nil {
			t.Fatal(err)
		}
		out := av1.StripTemporalDelimiters(tu, obus)
		if len(out) == len(tu) {
			t.Fatal("the generated stream has no temporal delimiters to strip")
		}
		stripped = append(stripped, out)
	}
	dec := newDecoderOrSkip(t, hwmediacodec.AV1)
	defer dec.Close()
	compareChecksums(t, checksums(decodePackets(t, dec, stripped, hwmediacodec.NV12)), want)
}

// TestDecodeAV1DecodeOrderOption checks that WithDecodeOrder changes
// nothing for AV1: temporal units are already in presentation order.
func TestDecodeAV1DecodeOrderOption(t *testing.T) {
	requireHardwareDecode(t, hwmediacodec.AV1)
	s := testutil.GenerateStream(t, hwmediacodec.AV1, 160, 120, 30)
	want := testutil.ReferenceNV12(t, s.Path, s.Codec, s.Width, s.Height)
	dec, err := hwmediacodec.NewDecoder(context.Background(), hwmediacodec.AV1, hwmediacodec.WithDecodeOrder())
	if err != nil {
		t.Fatalf("NewDecoder: %v", err)
	}
	defer dec.Close()
	got, _ := decodeAll(t, dec, hwmediacodec.AV1, testutil.ReadFile(t, s.Path))
	compareChecksums(t, got, want)
}

// TestDecodeAV1InvalidData checks the error for input that is not a
// temporal unit, and that units before the first sequence header are
// skipped without error.
func TestDecodeAV1InvalidData(t *testing.T) {
	requireHardwareDecode(t, hwmediacodec.AV1)
	ctx := context.Background()
	dec, err := hwmediacodec.NewDecoder(ctx, hwmediacodec.AV1)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	if err := dec.Send(ctx, hwmediacodec.Packet{Data: []byte("not a bitstream")}); !errors.Is(err, hwmediacodec.ErrInvalidData) {
		t.Fatalf("got %v want ErrInvalidData", err)
	}
	if err := dec.Send(ctx, hwmediacodec.Packet{}); !errors.Is(err, hwmediacodec.ErrInvalidData) {
		t.Fatalf("empty packet: got %v want ErrInvalidData", err)
	}
	s := testutil.GenerateStream(t, hwmediacodec.AV1, 160, 120, 10)
	units := splitStream(t, hwmediacodec.AV1, testutil.ReadFile(t, s.Path))
	// An inter frame before any sequence header: nothing to decode with.
	if err := dec.Send(ctx, hwmediacodec.Packet{Data: units[3]}); err != nil {
		t.Fatalf("unit before the sequence header: %v", err)
	}
	if _, err := dec.Receive(ctx); !errors.Is(err, hwmediacodec.ErrAgain) {
		t.Fatalf("Receive = %v, want ErrAgain", err)
	}
	if err := dec.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := dec.Receive(ctx); err != io.EOF {
		t.Fatalf("Receive after Flush = %v, want io.EOF", err)
	}
}

// TestDecodeAV1TenBit decodes a Main profile 10-bit stream with 8-bit
// output requested. VideoToolbox converts the 10-bit pictures itself; the
// result is compared with ffmpeg's own 10-to-8-bit conversion by PSNR,
// since the two round differently (53.5 dB measured on an M3; 45 dB is the
// bar, a wrong plane layout or an unconverted frame scores far below).
func TestDecodeAV1TenBit(t *testing.T) {
	requireHardwareDecode(t, hwmediacodec.AV1)
	const minPSNR = 45.0
	path := testutil.GenerateAV1(t, 320, 240, 30, "-pix_fmt", "yuv420p10le")
	stream := testutil.ReadFile(t, path)
	want := testutil.ReferenceNV12Frames(t, path, hwmediacodec.AV1, 320, 240)

	dec := newDecoderOrSkip(t, hwmediacodec.AV1)
	defer dec.Close()
	got := decodeFrames(t, dec, hwmediacodec.AV1, stream, hwmediacodec.NV12)
	if len(got) != len(want) {
		t.Fatalf("frame count: got %d want %d", len(got), len(want))
	}
	worst := math.Inf(1)
	for i := range got {
		if len(got[i].pix) != len(want[i]) {
			t.Fatalf("frame %d has %d bytes, want %d", i, len(got[i].pix), len(want[i]))
		}
		if psnr := testutil.PSNR(got[i].pix, want[i]); psnr < worst {
			worst = psnr
		}
	}
	t.Logf("worst PSNR of VideoToolbox's 10-to-8-bit NV12 against ffmpeg's: %.2f dB", worst)
	if worst < minPSNR {
		t.Fatalf("worst PSNR %.2f dB is below %.0f dB", worst, minPSNR)
	}

	bgra := newDecoderOrSkip(t, hwmediacodec.AV1, hwmediacodec.WithOutputFormat(hwmediacodec.BGRA))
	defer bgra.Close()
	if n := len(decodeFrames(t, bgra, hwmediacodec.AV1, stream, hwmediacodec.BGRA)); n != len(want) {
		t.Fatalf("BGRA frame count: got %d want %d", n, len(want))
	}
}
