# hwmediacodec

Hardware video decoding and encoding from Go, without cgo.

The library loads the operating system's codec engines at run time
(VideoToolbox on macOS and VA-API on Linux today; Media Foundation, Intel
VPL and NVENC/NVDEC are planned) through
[purego](https://github.com/ebitengine/purego), so `CGO_ENABLED=0 go build`
works and the module cross-compiles from one machine.

## Status

| Platform | Backend | Decode | Encode |
| --- | --- | --- | --- |
| macOS, Apple Silicon | VideoToolbox | H.264, HEVC; display order; NV12, RGBA or BGRA in CPU memory | H.264, HEVC; NV12, RGBA or BGRA in, Annex-B out |
| Linux, AMD (Mesa) and Intel (iHD / i965) | VA-API | H.264; display order; NV12 in CPU memory | H.264; NV12 in, Annex-B out |
| Linux, NVIDIA (proprietary driver 470+) | NVDEC / NVENC | H.264, HEVC; display order; NV12 in CPU memory | H.264, HEVC; NV12, RGBA or BGRA in, Annex-B out |
| Linux | Intel VPL | planned | planned |
| Windows | Media Foundation / NVENC | planned | planned |

Decoded frames come back in display order: the slice headers are parsed in
Go to derive picture order counts, and frames are held back no longer than
the stream's reorder bound. `WithDecodeOrder` switches back to the order the
hardware produces. `WithOutputFormat(hwmediacodec.RGBA)` (or `BGRA`) returns
packed 8-bit pixels with rows of exactly `4*Width` bytes, ready for
`ebiten.Image.WritePixels` or an `image.RGBA`; `WithInputFormat` accepts the
same formats for encoding, which is what `ebiten.Image.ReadPixels` produces.
The `ebitenvideo` module (see below) wraps this in a player for Ebitengine.

Encoder controls: target bitrate with VBR or CBR, constant quality, keyframe
interval, forced keyframes, B-frames on/off, low-latency mode, profile, and
in-band VPS/SPS/PPS in front of every keyframe.

Known limitations:

- The reorder bound comes from the stream. H.264 streams whose SPS has no
  VUI `bitstream_restriction` (VideoToolbox's own encoder writes none) are
  held back by the DPB size the level allows (4 frames at 1080p level 4.0,
  7 at 320x240), as the standard requires, unless they signal that they
  cannot reorder (`pic_order_cnt_type` 2, intra-only, constrained
  profiles). Latency-sensitive callers decoding such streams should use
  `WithDecodeOrder`. HEVC `pic_output_flag` 0 is not interpreted.
- RGBA and BGRA conversion is done by VideoToolbox: RGBA output is BGRA
  swapped in Go (about 1 ms per 1080p frame), since VideoToolbox rejects
  RGBA as a destination. Untagged streams are converted with the matrix
  VideoToolbox assumes (BT.601 for standard definition in our tests).
  RGB input is converted with BT.709 and the stream is tagged accordingly.
  Alpha is 255 on output and ignored on input. The VA-API backend offers
  NV12 only for now; requesting RGBA or BGRA there reports `ErrUnsupported`.
- Input and output are Annex-B, one access unit per `Packet`; use
  `annexb.Reader` to split a raw elementary stream. Containers (MP4, MKV, TS)
  are not parsed. AVCC/HVCC output is not offered yet.
- On an M3, 1080p H.264 with B-frames decodes at roughly 780 frames per
  second to NV12 in display order, 350 to BGRA and 250 to RGBA, including
  the copy; 1080p H.264 encodes at roughly 200 frames per second from NV12
  and 180 from RGBA or BGRA.
- The VideoToolbox hardware decoder rejects very small pictures (64x48
  fails with "decoder malfunction"; 96x64 works).
- `Encoder.Flush` blocks until VideoToolbox has emitted every pending frame;
  it does not observe context cancellation once the call has started.
- Intel Macs are out of scope; the VideoToolbox backend requires hardware
  engines unless `WithSoftwareFallback` is given.
- VA-API decoding: H.264 only for now (HEVC is next); progressive frames
  only (interlaced field pictures are rejected with `ErrUnsupported`); 8-bit
  4:2:0 only. VA-API is a slice-level API, so the bitstream parsing,
  picture order count, reference marking and reference list construction
  run in Go (`internal/h264`), and the driver only accelerates the slice
  data. `Send` returns `ErrAgain` when more than a few decoded frames are
  waiting for `Receive`; drain and resend.
- VA-API encoding: H.264 only, I and P frames with one reference;
  `WithBFrames` is accepted but no B-frames are produced yet. Pictures must
  have even width and height. `WithQuality` maps to a constant quantiser
  (CQP), `WithBitrate` to the driver's VBR or CBR rate control (the HRD
  buffer is one second of the peak rate), and without either a constant
  quantiser of 26 is used. The SPS, PPS and slice headers are written in Go
  and handed to the driver as packed headers when it accepts them (Intel
  requires this; Mesa generates its own otherwise), so every keyframe
  carries in-band SPS/PPS. Each picture is encoded synchronously inside
  `Send`.

## Usage

Decoding:

```go
dec, err := hwmediacodec.NewDecoder(ctx, hwmediacodec.H264)
if err != nil { /* errors.Is(err, hwmediacodec.ErrUnsupported) on machines without hardware */ }
defer dec.Close()

r := annexb.NewReader(file, hwmediacodec.H264)
for {
	au, err := r.Next()
	if err == io.EOF {
		break
	}
	if err := dec.Send(ctx, hwmediacodec.Packet{Data: au, PTS: pts}); err != nil {
		return err // errors.Is(err, hwmediacodec.ErrAgain): Receive first, then resend
	}
	for {
		f, err := dec.Receive(ctx)
		if errors.Is(err, hwmediacodec.ErrAgain) {
			break
		}
		// f.Planes[0] is Y, f.Planes[1] is interleaved CbCr (NV12)
		f.Release()
	}
}
dec.Flush(ctx) // then Receive until io.EOF
```

Encoding:

```go
enc, err := hwmediacodec.NewEncoder(ctx, hwmediacodec.H264, 1920, 1080,
	hwmediacodec.WithFrameRate(30),
	hwmediacodec.WithBitrate(4_000_000),      // VBR; add WithRateControl(hwmediacodec.CBR) for CBR
	hwmediacodec.WithKeyframeInterval(60))    // keyframe every two seconds
if err != nil { /* errors.Is(err, hwmediacodec.ErrUnsupported) */ }
defer enc.Close()

for i, f := range frames { // *hwmediacodec.Frame, NV12 planes owned by the caller
	f.PTS = int64(i) * int64(hwmediacodec.DefaultTimeScale) / 30
	if err := enc.Send(ctx, f); err != nil { // the frame is copied before Send returns
		return err
	}
	for {
		p, err := enc.Receive(ctx)
		if errors.Is(err, hwmediacodec.ErrAgain) {
			break
		}
		out.Write(p.Data) // one Annex-B access unit; p.Keyframe, p.PTS, p.DTS
	}
}
enc.Flush(ctx) // then Receive until io.EOF
```

Set `Frame.ForceKeyframe` to start a segment on a given frame, `WithBFrames`
to allow reordering (packets then come in decode order with `DTS` lagging
`PTS`), `WithQuality(0.7)` for constant quality instead of a bitrate, and
`WithLowLatency` for live streaming.

Pixel formats and frame order:

```go
dec, err := hwmediacodec.NewDecoder(ctx, hwmediacodec.H264,
	hwmediacodec.WithOutputFormat(hwmediacodec.RGBA)) // display order by default
...
f, err := dec.Receive(ctx)
img.WritePixels(f.Planes[0]) // *ebiten.Image of f.Width x f.Height; rows are 4*Width bytes
f.Release()

enc, err := hwmediacodec.NewEncoder(ctx, hwmediacodec.H264, w, h,
	hwmediacodec.WithInputFormat(hwmediacodec.RGBA), hwmediacodec.WithFrameRate(60))
pix := make([]byte, 4*w*h)
screen.ReadPixels(pix)
enc.Send(ctx, &hwmediacodec.Frame{Width: w, Height: h, Format: hwmediacodec.RGBA,
	Planes: [][]byte{pix}, Strides: []int{4 * w}, PTS: pts})
```

`PixelFormat.PlaneCount`, `PlaneLayout` and `FrameSize` describe the plane
layout of each format. `WithDecodeOrder()` returns frames as the hardware
produces them, which is what a transcoder that keeps the original
timestamps wants.

## Ebitengine

`github.com/shibukawa/hwmediacodec/ebitenvideo` is a separate Go module in
this repository (its own `go.mod`) that depends on both `hwmediacodec` and
Ebitengine; the core module never imports Ebitengine, so users without a
game engine do not pull it in. The module decodes an elementary stream on a
background goroutine, in display order and as RGBA, and paces it against
the game loop:

```go
player, err := ebitenvideo.NewPlayer(file, hwmediacodec.H264, 30, ebitenvideo.WithLoop())
player.Play()

func (g *game) Update() error { return player.Update() }   // one tick per call
func (g *game) Draw(screen *ebiten.Image) {
	if img := player.Image(); img != nil {                   // updated in place
		screen.DrawImage(img, nil)
	}
}
```

Elementary streams carry no timestamps, so the frame rate is a parameter.
Frames are skipped when decoding or the game loop falls behind
(`Player.Skipped` counts them). A runnable example plays a file in a
window:

```sh
cd ebitenvideo && go run ./example -codec h264 -fps 30 ../video.h264
```

Inside the repository `ebitenvideo/go.mod` points at the core module with a
`replace ../` directive, so both modules always build against the working
tree. Consumers get the version named in its `require` line; tag the core
module first (for example `v0.3.0`), update that line, then tag
`ebitenvideo/v0.3.0`.

A command-line tool exercises the same API:

```sh
go run ./cmd/hwmediacodec probe
go run ./cmd/hwmediacodec decode -hash input.h264                       # display order, NV12
go run ./cmd/hwmediacodec decode -format rgba -o out.rgba input.hevc -codec hevc
go run ./cmd/hwmediacodec decode -decode-order -hash input.h264
go run ./cmd/hwmediacodec encode -size 1920x1080 -bitrate 8M -gop 60 -o out.h264 input.nv12
go run ./cmd/hwmediacodec encode -size 1920x1080 -format rgba -o out.h264 input.rgba
go run ./cmd/hwmediacodec transcode -in h264 -codec hevc -bitrate 6M -o out.hevc input.h264
```

### Linux notes

The NVIDIA backend (`internal/nvidia`) loads `libcuda.so.1`, `libnvcuvid.so.1`
and `libnvidia-encode.so.1` from the proprietary driver (470.57 or newer for
NVENC API 11.1). Decoding uses the driver's own parser, so H.264 and HEVC both
work; frames are 8-bit 4:2:0 NV12 (10-bit and 4:4:4 streams report
`ErrUnsupported`), progressive only, copied to CPU memory per picture.
Encoding accepts NV12, RGBA and BGRA (RGB is converted by NVENC with BT.601
and the stream is tagged so), supports B-frames (two consecutive, when the GPU
has them), VBR/CBR, constant QP for `WithQuality`, low-latency tuning and
profiles. Set `HWMEDIACODEC_NVIDIA_DEVICE` to a CUDA device ordinal to pick a
GPU. The backend is registered before VA-API. It was written on a Mac: struct
layouts are verified against the SDK headers with clang and every symbol binds
against driver 535 in a container, but no NVIDIA hardware conformance run has
happened yet.

The VA-API backend opens the first DRM render node (`/dev/dri/renderD128`
and up) that libva can initialise. Set `HWMEDIACODEC_VAAPI_DEVICE` to a
render node path to pick a GPU on multi-GPU machines. The user needs read
and write access to the node (usually the `render` or `video` group), and
`libva2`, `libva-drm2` and the GPU's VA driver (`mesa-va-drivers` for AMD,
`intel-media-va-driver` for Intel) must be installed. `Probe` reports no
VA-API capability, rather than an error, when any of these is missing.

## Testing

Unit tests run everywhere. The H.264 parser, decoded-picture-buffer logic
and header writer are checked against ffmpeg's own view of the stream
(`trace_headers`, `-debug mmco`, `-debug pict`), so they run on any machine
with ffmpeg; the reorder logic is also checked without hardware, against
the presentation timestamps ffmpeg writes into an MP4 of the same stream.
The decode and encode conformance tests run where `Probe` reports a
hardware engine (Apple Silicon, or Linux with a VA-API driver) and use
`ffmpeg` and `ffprobe` as the reference. Decoded B-frame streams must match
ffmpeg's output frame for frame in display order. RGB output and input are
compared to ffmpeg's conversion by block-averaged PSNR (the two converters
interpolate chroma differently) and RGBA must be the exact mirror of BGRA.
Encoded streams are decoded by ffmpeg and compared to the source by PSNR,
and their keyframe and B-frame structure is checked with ffprobe. All of
them are skipped when ffmpeg is not installed.

```sh
go test ./...
CGO_ENABLED=0 go test ./...   # exercises the cgo-free callback path
./scripts/crossbuild.sh       # CGO_ENABLED=0 builds for every target
(cd ebitenvideo && go test ./...)   # separate module: timeline logic plus a hardware playback test
```

Project knowledge (requirements, decisions, backend notes) lives in
`.knowledge/` and is maintained with the knowledge-memory skill.
