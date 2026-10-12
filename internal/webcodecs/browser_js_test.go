//go:build js && wasm

package webcodecs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"syscall/js"
	"testing"

	"github.com/shibukawa/hwmediacodec/encoding/annexb"
	"github.com/shibukawa/hwmediacodec/internal/codec"
	"github.com/shibukawa/hwmediacodec/internal/h264"
	"github.com/shibukawa/hwmediacodec/internal/reorder"
	"github.com/shibukawa/hwmediacodec/internal/testutil"
	"github.com/shibukawa/hwmediacodec/mediacontainer/ivf"
)

// These tests need a browser (scripts/browser_test.sh ./internal/webcodecs);
// under Node.js there is no WebCodecs and they skip.

// requireDiagnose skips the tests that only log how a browser behaves
// unless HWMEDIACODEC_TEST_DIAGNOSE is set: they drive the browser's
// codecs directly, where a browser that does not work can make them wait
// for as long as its timers are suspended.
func requireDiagnose(t *testing.T) {
	t.Helper()
	requireWebCodecs(t)
	if os.Getenv("HWMEDIACODEC_TEST_DIAGNOSE") == "" {
		t.Skip("set HWMEDIACODEC_TEST_DIAGNOSE=1 to log this browser's behaviour")
	}
}

func requireWebCodecs(t *testing.T) {
	t.Helper()
	if _, ok := class("VideoDecoder"); !ok {
		t.Skip(noWebCodecs)
	}
}

// software makes every configuration of the test ask for the browser's
// software codecs.
func software(t *testing.T) {
	t.Helper()
	accelerationOverride = "prefer-software"
	t.Cleanup(func() { accelerationOverride = "" })
}

func units(t *testing.T, c codec.Codec, stream []byte) [][]byte {
	t.Helper()
	var out [][]byte
	if c == codec.AV1 {
		r, err := ivf.NewReader(bytes.NewReader(stream))
		if err != nil {
			t.Fatal(err)
		}
		for {
			tu, _, err := r.Next()
			if err == io.EOF {
				return out
			}
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, tu)
		}
	}
	r := annexb.NewReader(bytes.NewReader(stream), c)
	for {
		au, err := r.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, au)
	}
}

// openDecoder opens a backend decoder the way the public API does: with
// the reorder layer where the backend asks for it.
func openDecoder(t *testing.T, c codec.Codec, format codec.PixelFormat) codec.Decoder {
	t.Helper()
	d, err := Backend{}.NewDecoder(context.Background(), codec.DecoderConfig{Codec: c, OutputFormat: format, TimeScale: 90000, DisplayOrder: true, AllowSoftware: true})
	if errors.Is(err, codec.ErrUnsupported) {
		t.Skipf("not available in this browser: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if o, ok := d.(codec.DisplayOrderer); !ok || !o.OutputsDisplayOrder() {
		d = reorder.Wrap(d, c)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// decode runs the Send/Receive loop over the packets and returns the
// checksum of every frame.
func decode(t *testing.T, d codec.Decoder, packets [][]byte) [][32]byte {
	t.Helper()
	ctx := context.Background()
	var sums [][32]byte
	drain := func(end error) {
		for {
			f, err := d.Receive(ctx)
			if err == end {
				return
			}
			if errors.Is(err, codec.ErrUnsupported) {
				// A browser whose decoders hand over RGB has no NV12.
				t.Skipf("not available in this browser: %v", err)
			}
			if err != nil {
				t.Fatalf("Receive: %v", err)
			}
			sums = append(sums, testutil.FrameChecksum(f))
			f.Release()
		}
	}
	for i, p := range packets {
		for {
			err := d.Send(ctx, codec.Packet{Data: p, PTS: int64(i)})
			if errors.Is(err, codec.ErrAgain) {
				drain(codec.ErrAgain)
				continue
			}
			if err != nil {
				t.Fatalf("Send %d: %v", i, err)
			}
			break
		}
		drain(codec.ErrAgain)
	}
	if err := d.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	drain(io.EOF)
	return sums
}

func compare(t *testing.T, got, want [][32]byte) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d frames, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("frame %d differs from ffmpeg's", i)
		}
	}
}

// TestSoftwareDecodeMatchesReference decodes with the browser's software
// decoders, whose frames are planar (I420) and are packed into NV12 in Go,
// and compares every frame with ffmpeg's decode. HEVC has no software
// decoder in the browsers and skips.
func TestSoftwareDecodeMatchesReference(t *testing.T) {
	requireWebCodecs(t)
	for _, c := range []codec.Codec{codec.H264, codec.HEVC, codec.AV1} {
		t.Run(c.String(), func(t *testing.T) {
			software(t)
			d := openDecoder(t, c, codec.NV12)
			s := testutil.GenerateStreamBFrames(t, c, 320, 240, 40, 2)
			want := testutil.ReferenceNV12(t, s.Path, c, s.Width, s.Height)
			compare(t, decode(t, d, units(t, c, testutil.ReadFile(t, s.Path))), want)
		})
	}
}

// TestSoftwareEncode encodes with the browser's software H.264 encoder and
// decodes the result with ffmpeg.
func TestSoftwareEncode(t *testing.T) {
	requireWebCodecs(t)
	software(t)
	const w, h, n = 320, 240, 20
	ctx := context.Background()
	e, err := Backend{}.NewEncoder(ctx, codec.EncoderConfig{
		Codec: codec.H264, Width: w, Height: h, InputFormat: codec.NV12, TimeScale: 90000,
		FrameRate: 30, Bitrate: 800_000, RateControl: codec.VBR, KeyframeInterval: 10, AllowSoftware: true,
	})
	if errors.Is(err, codec.ErrUnsupported) {
		t.Skipf("not available in this browser: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	raw := testutil.GenerateNV12Frames(t, w, h, n)
	var stream []byte
	keys, packets := 0, 0
	drain := func(end error) {
		for {
			p, err := e.Receive(ctx)
			if err == end {
				return
			}
			if err != nil {
				t.Fatalf("Receive: %v", err)
			}
			if p.Keyframe {
				keys++
				if info := newParamSets(codec.H264).inspect(p.Data); !info.kinds[annexb.ParamSPS] || !info.kinds[annexb.ParamPPS] {
					t.Errorf("keyframe packet %d carries no parameter sets", packets)
				}
			}
			if p.PTS != int64(packets)*3000 {
				t.Errorf("packet %d has PTS %d", packets, p.PTS)
			}
			packets++
			stream = append(stream, p.Data...)
		}
	}
	for i, pix := range raw {
		if err := e.Send(ctx, testutil.NV12Frame(pix, w, h, int64(i)*3000)); err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
		drain(codec.ErrAgain)
	}
	if err := e.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	drain(io.EOF)
	if packets != n || keys != 2 {
		t.Fatalf("%d packets with %d keyframes, want %d and 2", packets, keys, n)
	}
	path := testutil.TempDir(t) + "/software.h264"
	testutil.WriteFile(t, path, stream)
	decoded := testutil.ReferenceNV12Frames(t, path, codec.H264, w, h)
	if len(decoded) != n {
		t.Fatalf("ffmpeg decodes %d frames, want %d", len(decoded), n)
	}
	worst := 100.0
	for i := range decoded {
		if psnr := testutil.PSNRY(decoded[i], raw[i], w, h); psnr < worst {
			worst = psnr
		}
	}
	t.Logf("worst luma PSNR %.2f dB", worst)
	if worst < 30 {
		t.Errorf("worst luma PSNR %.2f dB is below 30 dB", worst)
	}
}

// TestDecoderRecoversFromError breaks a stream so that the browser's
// decoder gives up, which closes a VideoDecoder for good, and checks that
// the error is reported and that the same Decoder then decodes a stream
// from its first keyframe.
func TestDecoderRecoversFromError(t *testing.T) {
	requireWebCodecs(t)
	d := openDecoder(t, codec.H264, codec.NV12)
	s := testutil.GenerateStream(t, codec.H264, 320, 240, 12)
	stream := testutil.ReadFile(t, s.Path)
	packets := units(t, codec.H264, stream)
	want := testutil.ReferenceNV12(t, s.Path, codec.H264, s.Width, s.Height)

	// The parameter sets of the stream in front of a keyframe slice that
	// is noise.
	var broken []byte
	for _, nal := range annexb.Split(packets[0]) {
		if annexb.IsParameterSet(codec.H264, annexb.NALUnitType(codec.H264, nal)) {
			broken = annexb.AppendUnit(broken, nal)
		}
	}
	noise := []byte{0x65}
	for i := 0; i < 400; i++ {
		noise = append(noise, byte(i*37+11)|1)
	}
	broken = annexb.AppendUnit(broken, noise)

	ctx := context.Background()
	err := d.Send(ctx, codec.Packet{Data: broken})
	if err == nil {
		err = d.Flush(ctx)
	}
	for err == nil {
		var f *codec.Frame
		if f, err = d.Receive(ctx); err == nil {
			f.Release()
		}
	}
	if err == io.EOF {
		t.Skip("this browser's decoder swallowed the broken keyframe")
	}
	t.Logf("broken keyframe: %v", err)
	if errors.Is(err, codec.ErrAgain) || errors.Is(err, codec.ErrClosed) {
		t.Fatalf("broken keyframe reported as %v", err)
	}
	compare(t, decode(t, d, packets), want)
}

// TestSupportMatrix logs what the browser answers for every codec and
// acceleration preference: the first thing to look at when a browser
// behaves differently. It asserts nothing.
func TestSupportMatrix(t *testing.T) {
	requireDiagnose(t)
	ctx := context.Background()
	dec, _ := class("VideoDecoder")
	enc, hasEnc := class("VideoEncoder")
	for _, mode := range []string{"prefer-hardware", "no-preference", "prefer-software"} {
		accelerationOverride = mode
		for _, c := range []codec.Codec{codec.H264, codec.HEVC, codec.AV1} {
			name, err := supportedDecodeCodec(ctx, dec, probeDecodeCodecs(c), false)
			if err != nil {
				t.Fatal(err)
			}
			line := "decode " + name
			if name == "" {
				line = "no decoder"
			}
			if hasEnc && c != codec.AV1 {
				config, err := supportedEncoderConfig(ctx, enc, probeEncoderConfig(c))
				if err != nil {
					t.Fatal(err)
				}
				if config != nil {
					line += ", encode " + config["codec"].(string)
				} else {
					line += ", no encoder"
				}
			}
			t.Logf("%-16s %-5s %s", mode, c, line)
		}
	}
	accelerationOverride = ""
}

// TestEncoderBehaviour logs how the browser's H.264 encoder answers a few
// configurations when it is driven directly: how many chunks it has
// emitted after eight frames and some turns of the event loop, how many
// more a flush brings, and what the first chunk looks like. It asserts
// nothing; it is what to read when Send stalls or packets come late in a
// browser. It waits on promises and network round trips only, since a
// browser may suspend the timers of a tab nobody looks at.
func TestEncoderBehaviour(t *testing.T) {
	requireDiagnose(t)
	enc, ok := class("VideoEncoder")
	if !ok {
		t.Skip(noWebCodecs)
	}
	const w, h, frames = 320, 240, 8
	ctx := context.Background()
	base := probeEncoderConfig(codec.H264)
	base.Width, base.Height, base.Bitrate = w, h, 800_000
	variants := []struct {
		name   string
		adjust func(map[string]any)
	}{
		{"quality, annexb", func(m map[string]any) {}},
		{"realtime, annexb", func(m map[string]any) { m["latencyMode"] = "realtime" }},
		{"quality, default format", func(m map[string]any) { delete(m, "avc") }},
		{"realtime, default format", func(m map[string]any) { m["latencyMode"] = "realtime"; delete(m, "avc") }},
	}
	for _, v := range variants {
		config, err := supportedEncoderConfig(ctx, enc, base)
		if err != nil || config == nil {
			t.Logf("%-26s no supported configuration (%v)", v.name, err)
			continue
		}
		v.adjust(config)
		chunks, failure, first := 0, "", ""
		onOutput := js.FuncOf(func(_ js.Value, args []js.Value) any {
			if chunks == 0 {
				first = args[0].Get("type").String()
				if len(args) > 1 && args[1].Type() == js.TypeObject {
					if dc := args[1].Get("decoderConfig"); dc.Type() == js.TypeObject && dc.Get("description").Type() == js.TypeObject {
						first += " with description"
					}
				}
			}
			chunks++
			return nil
		})
		onError := js.FuncOf(func(_ js.Value, args []js.Value) any {
			if len(args) > 0 {
				failure = errorText(args[0])
			}
			return nil
		})
		var e js.Value
		err = try("VideoEncoder", func() {
			e = enc.New(map[string]any{"output": onOutput, "error": onError})
			e.Call("configure", config)
			pixels := uint8Array.New(codec.NV12.FrameSize(w, h))
			for i := 0; i < frames; i++ {
				frame := global.Get("VideoFrame").New(pixels, map[string]any{
					"format": "NV12", "codedWidth": w, "codedHeight": h, "timestamp": i * 33333, "duration": 33333,
				})
				e.Call("encode", frame, map[string]any{"keyFrame": i == 0})
				frame.Call("close")
			}
		})
		if err != nil {
			t.Logf("%-26s %v", v.name, err)
			continue
		}
		// Turns of the event loop that do not depend on timers.
		for i := 0; i < 40; i++ {
			testutil.LookPath("ffmpeg")
		}
		before, queue := chunks, -1
		if failure == "" {
			queue = e.Get("encodeQueueSize").Int()
			var promise js.Value
			if err := try("flush", func() { promise = e.Call("flush") }); err == nil {
				if _, err := await(ctx, "flush", promise); err != nil && failure == "" {
					failure = err.Error()
				}
			}
		}
		t.Logf("%-26s %d of %d chunks before flush (queue %d), %d after; first chunk: %s; error: %q", v.name, before, frames, queue, chunks, first, failure)
		try("close", func() {
			if e.Get("state").String() != "closed" {
				e.Call("close")
			}
		})
		onOutput.Release()
		onError.Release()
	}
}

// TestDecoderBehaviour logs what the browser's H.264 decoder hands over
// for the first picture of an ffmpeg stream, driven directly: the frame's
// format, sizes and colour space, and how far its samples are from
// ffmpeg's, as they are and read as full-range samples. It asserts nothing.
func TestDecoderBehaviour(t *testing.T) {
	requireDiagnose(t)
	dec, _ := class("VideoDecoder")
	ctx := context.Background()
	const w, h = 320, 240
	s := testutil.GenerateStream(t, codec.H264, w, h, 3)
	want := testutil.ReferenceNV12Frames(t, s.Path, codec.H264, w, h)[0]
	key := units(t, codec.H264, testutil.ReadFile(t, s.Path))[0]
	ps := newParamSets(codec.H264)
	ps.inspect(key)
	sps, err := h264.ParseSPS(ps.lastSPS)
	if err != nil {
		t.Fatal(err)
	}

	var frame js.Value
	failure := ""
	onOutput := js.FuncOf(func(_ js.Value, args []js.Value) any {
		if frame.IsUndefined() {
			frame = args[0]
		} else {
			args[0].Call("close")
		}
		return nil
	})
	onError := js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) > 0 {
			failure = errorText(args[0])
		}
		return nil
	})
	defer onOutput.Release()
	defer onError.Release()
	var d js.Value
	err = try("VideoDecoder", func() {
		d = dec.New(map[string]any{"output": onOutput, "error": onError})
		d.Call("configure", decoderConfig(h264StreamCodecString(sps), false))
		data := uint8Array.New(len(key))
		js.CopyBytesToJS(data, key)
		d.Call("decode", global.Get("EncodedVideoChunk").New(map[string]any{"type": "key", "timestamp": 0, "data": data}))
		if _, err := await(ctx, "flush", d.Call("flush")); err != nil {
			failure = err.Error()
		}
	})
	if err != nil || failure != "" || frame.IsUndefined() {
		t.Fatalf("no frame: %v %s", err, failure)
	}
	defer frame.Call("close")
	format := "null"
	if v := frame.Get("format"); v.Type() == js.TypeString {
		format = v.String()
	}
	rect, cs := frame.Get("visibleRect"), frame.Get("colorSpace")
	t.Logf("format %s, coded %dx%d, visible %dx%d at %d,%d, display %dx%d", format,
		frame.Get("codedWidth").Int(), frame.Get("codedHeight").Int(),
		rect.Get("width").Int(), rect.Get("height").Int(), rect.Get("x").Int(), rect.Get("y").Int(),
		frame.Get("displayWidth").Int(), frame.Get("displayHeight").Int())
	t.Logf("colorSpace %s", global.Get("JSON").Call("stringify", cs).String())
	if format != "NV12" && format != "I420" {
		return
	}
	size := frame.Call("allocationSize").Int()
	buf := uint8Array.New(size)
	layoutValue, err := await(ctx, "copyTo", frame.Call("copyTo", buf))
	if err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, size)
	js.CopyBytesToGo(raw, buf)
	layout := make([]planeLayout, layoutValue.Length())
	for i := range layout {
		layout[i] = planeLayout{offset: layoutValue.Index(i).Get("offset").Int(), stride: layoutValue.Index(i).Get("stride").Int()}
	}
	t.Logf("allocation %d bytes, layout %v", size, layout)
	got := make([]byte, codec.NV12.FrameSize(w, h))
	if format == "I420" {
		if err := i420ToNV12(got, raw, layout, w, h); err != nil {
			t.Fatal(err)
		}
	} else {
		planes, strides, err := nv12Planes(raw, layout, w, h)
		if err != nil {
			t.Fatal(err)
		}
		for y := 0; y < h; y++ {
			copy(got[y*w:], planes[0][y*strides[0]:y*strides[0]+w])
		}
		for y := 0; y < h/2; y++ {
			copy(got[w*h+y*w:], planes[1][y*strides[1]:y*strides[1]+w])
		}
	}
	// The same samples read as full range and brought to video range.
	narrowed := make([]byte, len(got))
	for i, v := range got {
		if i < w*h {
			narrowed[i] = byte((int(v)*219+127)/255 + 16)
		} else {
			narrowed[i] = byte(((int(v)-128)*224)/255 + 128)
		}
	}
	mean := func(b []byte) float64 {
		sum := 0
		for _, v := range b {
			sum += int(v)
		}
		return float64(sum) / float64(len(b))
	}
	t.Logf("luma: PSNR %.1f dB as is, %.1f dB narrowed; mean %.2f, ffmpeg %.2f; first samples %v, ffmpeg %v",
		testutil.PSNR(got[:w*h], want[:w*h]), testutil.PSNR(narrowed[:w*h], want[:w*h]), mean(got[:w*h]), mean(want[:w*h]), got[:8], want[:8])
	t.Logf("chroma: PSNR %.1f dB as is, %.1f dB narrowed; first samples %v, ffmpeg %v",
		testutil.PSNR(got[w*h:], want[w*h:]), testutil.PSNR(narrowed[w*h:], want[w*h:]), got[w*h:w*h+8], want[w*h:w*h+8])
}
