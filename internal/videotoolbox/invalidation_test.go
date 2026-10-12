//go:build darwin

package videotoolbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/shibukawa/hwmediacodec/encoding/annexb"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
	"github.com/shibukawa/hwmediacodec/internal/videotoolbox/sys"
)

// requireHardware skips the test unless this backend reports a hardware
// engine for c in the given direction.
func requireHardware(t *testing.T, c codec.Codec, dir codec.Direction) {
	t.Helper()
	if runtime.GOARCH != "arm64" {
		t.Skip("hardware tests target Apple Silicon")
	}
	caps, err := Backend{}.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	for _, cap := range caps {
		if cap.Codec == c && cap.Direction == dir && cap.Hardware {
			return
		}
	}
	t.Skipf("no hardware %s %s on this machine", c, dir)
}

// invalidate tears the live session down behind the codec's back, which is
// what iOS does to an application that leaves the foreground.
func invalidateDecoder(d *decoder) {
	sessionMu.Lock()
	sys.VTDecompressionSessionInvalidate(d.session)
	sessionMu.Unlock()
}

func invalidateEncoder(e *encoder) {
	sessionMu.Lock()
	sys.VTCompressionSessionInvalidate(e.session)
	sessionMu.Unlock()
}

// TestDecoderRecoversFromInvalidatedSession decodes a stream whose session
// is invalidated part way: Send must not fail, the frames up to the
// invalidation and from the next keyframe on must match the reference, and
// the pictures in between (undecodable without their reference) are
// skipped.
func TestDecoderRecoversFromInvalidatedSession(t *testing.T) {
	requireHardware(t, codec.H264, codec.Decode)
	const w, h, n = 320, 240, 60 // keyframes at 0, 25 and 50
	const invalidateAt, resumeAt = 30, 50
	stream := testutil.GenerateStream(t, codec.H264, w, h, n)
	ref := testutil.ReferenceNV12(t, stream.Path, codec.H264, w, h)
	data, err := os.ReadFile(stream.Path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	dec, err := Backend{}.NewDecoder(ctx, codec.DecoderConfig{Codec: codec.H264, OutputFormat: codec.NV12, TimeScale: 90000})
	if err != nil {
		t.Fatalf("NewDecoder: %v", err)
	}
	defer dec.Close()
	d := dec.(*decoder)

	got := map[int64][32]byte{}
	receive := func() {
		for {
			f, err := dec.Receive(ctx)
			if errors.Is(err, codec.ErrAgain) || errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				t.Fatalf("Receive: %v", err)
			}
			got[f.PTS] = sha256.Sum256(testutil.FrameBytes(f))
			f.Release()
		}
	}
	r := annexb.NewReader(bytes.NewReader(data), codec.H264)
	for i := 0; ; i++ {
		au, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if i == invalidateAt {
			if d.session == 0 {
				t.Fatal("no session before the invalidation")
			}
			invalidateDecoder(d)
		}
		if err := dec.Send(ctx, codec.Packet{Data: au, PTS: int64(i) * 3000}); err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
		receive()
	}
	if err := dec.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	receive()

	for i := 0; i < n; i++ {
		sum, ok := got[int64(i)*3000]
		switch {
		case i >= invalidateAt && i < resumeAt:
			if ok {
				t.Errorf("frame %d came out although it followed the invalidation without a keyframe", i)
			}
		case !ok:
			t.Errorf("frame %d missing", i)
		case sum != ref[i]:
			t.Errorf("frame %d differs from the reference", i)
		}
	}
	t.Logf("%d of %d frames decoded across the invalidation", len(got), n)
}

// TestDecoderRecoversAtKeyframe invalidates the session right before a
// keyframe: that packet must decode on the new session instead of being
// dropped.
func TestDecoderRecoversAtKeyframe(t *testing.T) {
	requireHardware(t, codec.H264, codec.Decode)
	const w, h, n = 320, 240, 30 // keyframes at 0 and 25
	stream := testutil.GenerateStream(t, codec.H264, w, h, n)
	ref := testutil.ReferenceNV12(t, stream.Path, codec.H264, w, h)
	data, err := os.ReadFile(stream.Path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	dec, err := Backend{}.NewDecoder(ctx, codec.DecoderConfig{Codec: codec.H264, OutputFormat: codec.NV12, TimeScale: 90000})
	if err != nil {
		t.Fatalf("NewDecoder: %v", err)
	}
	defer dec.Close()
	d := dec.(*decoder)

	frames := 0
	r := annexb.NewReader(bytes.NewReader(data), codec.H264)
	for i := 0; ; i++ {
		au, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if i == 25 {
			invalidateDecoder(d)
		}
		if err := dec.Send(ctx, codec.Packet{Data: au, PTS: int64(i) * 3000}); err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
		for {
			f, err := dec.Receive(ctx)
			if errors.Is(err, codec.ErrAgain) {
				break
			}
			if err != nil {
				t.Fatalf("Receive: %v", err)
			}
			if idx := int(f.PTS / 3000); sha256.Sum256(testutil.FrameBytes(f)) != ref[idx] {
				t.Errorf("frame %d differs from the reference", idx)
			}
			frames++
			f.Release()
		}
	}
	if frames != n {
		t.Errorf("got %d frames, want all %d (the keyframe after the invalidation must not be dropped)", frames, n)
	}
}

// TestEncoderRecoversFromInvalidatedSession encodes frames across a session
// invalidation: Send must not fail, the frame that hit the error comes out
// as a keyframe of a fresh session, and ffmpeg decodes the whole stream.
func TestEncoderRecoversFromInvalidatedSession(t *testing.T) {
	requireHardware(t, codec.H264, codec.Encode)
	const w, h, n = 320, 240, 40
	const invalidateAt = 20
	frames := testutil.GenerateNV12Frames(t, w, h, n)
	ctx := context.Background()
	enc, err := Backend{}.NewEncoder(ctx, codec.EncoderConfig{
		Codec: codec.H264, Width: w, Height: h, InputFormat: codec.NV12,
		TimeScale: 90000, FrameRate: 30, Bitrate: 1_000_000,
	})
	if err != nil {
		t.Fatalf("NewEncoder: %v", err)
	}
	defer enc.Close()
	e := enc.(*encoder)

	var packets []codec.Packet
	receive := func() {
		for {
			p, err := enc.Receive(ctx)
			if errors.Is(err, codec.ErrAgain) || errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				t.Fatalf("Receive: %v", err)
			}
			packets = append(packets, p)
		}
	}
	for i, fr := range frames {
		if i == invalidateAt {
			invalidateEncoder(e)
		}
		if err := enc.Send(ctx, testutil.NV12Frame(fr, w, h, int64(i)*3000)); err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
		receive()
	}
	if err := enc.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	receive()

	var out bytes.Buffer
	seen := map[int64]bool{}
	resumed := false
	for _, p := range packets {
		if seen[p.PTS] {
			t.Errorf("PTS %d emitted twice", p.PTS)
		}
		seen[p.PTS] = true
		if p.PTS >= invalidateAt*3000 && !resumed {
			resumed = true
			if !p.Keyframe {
				t.Errorf("the first packet after the invalidation (PTS %d) is not a keyframe", p.PTS)
			}
		}
		out.Write(p.Data)
	}
	if !resumed {
		t.Fatal("no packet came out after the invalidation")
	}
	// The frames the old session still held are lost; the rest must be there.
	if len(packets) < n-8 {
		t.Errorf("only %d of %d frames came out", len(packets), n)
	}
	path := filepath.Join(t.TempDir(), "recovered.h264")
	if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	decoded := testutil.ReferenceNV12Frames(t, path, codec.H264, w, h)
	if len(decoded) != len(packets) {
		t.Errorf("ffmpeg decodes %d frames from the stream, %d packets were written", len(decoded), len(packets))
	}
	t.Logf("%d of %d frames encoded across the invalidation; ffmpeg decodes %d", len(packets), n, len(decoded))
}
