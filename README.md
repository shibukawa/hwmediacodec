# hwmediacodec

Hardware video decoding and encoding from Go, without cgo.

The library loads the operating system's codec engines at run time
(VideoToolbox on macOS and Media Foundation on Windows today; Intel VPL,
VA-API and NVENC/NVDEC are planned for Linux) through
[purego](https://github.com/ebitengine/purego) on macOS and
`golang.org/x/sys/windows` plus raw COM vtable calls on Windows, so
`CGO_ENABLED=0 go build` works and the module cross-compiles from one machine.

## Status

| Platform | Backend | Decode | Encode |
| --- | --- | --- | --- |
| macOS, Apple Silicon | VideoToolbox | H.264, HEVC; display order; NV12, RGBA or BGRA in CPU memory | H.264, HEVC; NV12, RGBA or BGRA in, Annex-B out |
| Windows x64 / ARM64, Intel, AMD, NVIDIA | Media Foundation (decode: Microsoft MFTs + Direct3D 11 DXVA; encode: vendor hardware MFTs) | H.264, HEVC; display order; NV12 in CPU memory; **not yet verified on hardware** | H.264, HEVC; NV12 in, Annex-B out; **not yet verified on hardware** |
| Linux | Intel VPL / VA-API / NVDEC | planned | planned |

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
  `WithDecodeOrder`. H.264 `memory_management_control_operation` 5 and
  HEVC `pic_output_flag` 0 are not interpreted.
- RGBA and BGRA conversion is done by VideoToolbox: RGBA output is BGRA
  swapped in Go (about 1 ms per 1080p frame), since VideoToolbox rejects
  RGBA as a destination. Untagged streams are converted with the matrix
  VideoToolbox assumes (BT.601 for standard definition in our tests).
  RGB input is converted with BT.709 and the stream is tagged accordingly.
  Alpha is 255 on output and ignored on input.
- On Windows the display order comes from the Media Foundation decoder
  itself, so `WithDecodeOrder` has no effect there, and only NV12 is offered:
  RGBA/BGRA conversion is not implemented for the Windows backend yet.
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

### Windows (Media Foundation)

Decoding drives the synchronous decoder MFTs that ship with Windows
(`Msmpeg2vdec.dll` for H.264, the "HEVC Video Extensions" package for HEVC)
with a Direct3D 11 device attached through `IMFDXGIDeviceManager`, so the
GPU's DXVA engine does the decoding whatever the vendor. `Probe` reports a
decoder only when the GPU exposes the matching DXVA profile with NV12 output
*and* a usable decoder MFT exists; `NewDecoder` fails with `ErrUnsupported`
otherwise instead of decoding in software.

Encoding drives the vendor's hardware encoder MFT (Intel Quick Sync Video,
AMD, NVIDIA), which Media Foundation exposes as an asynchronous MFT: the
backend unlocks it, feeds NV12 frames from system memory on
`METransformNeedInput` and collects Annex-B access units on
`METransformHaveOutput`. Encoder controls map to `ICodecAPI`
(`AVEncCommonRateControlMode`, `AVEncCommonMeanBitRate`, `AVEncCommonQuality`,
`AVEncMPVGOPSize`, `AVEncMPVDefaultBPictureCount`, `AVLowLatencyMode`,
`AVEncVideoForceKeyFrame`) and to the output media type (`MF_MT_AVG_BITRATE`,
`MF_MT_FRAME_RATE`, `MF_MT_MPEG2_PROFILE`). Keyframes always carry in-band
VPS/SPS/PPS: the backend stores the parameter sets it sees in the stream or in
`MF_MT_MPEG_SEQUENCE_HEADER` and prepends them when the encoder leaves them
out. `Probe` reports an encoder only when the driver registers a hardware
encoder MFT; `WithSoftwareFallback` allows the synchronous Microsoft H.264
encoder (software) instead. When no bitrate or quality is given the backend
asks for 0.1 bit per pixel per frame (at least 200 kbit/s); when no frame rate
is given it declares 30 fps.

- Requires Windows 10 or later, a GPU driver with DXVA support, and for
  HEVC the free "HEVC Video Extensions from Device Manufacturer" (or the
  paid "HEVC Video Extensions") Store package.
- Decoded textures are copied to CPU memory through a staging texture; the
  visible picture is cropped from the padded coded size using
  `MF_MT_MINIMUM_DISPLAY_APERTURE`.
- `WithSoftwareFallback` is not implemented for decoding on Windows yet.
  Vendor asynchronous hardware *decoder* MFTs (for example the Intel VP9
  decoder MFT) are not used; H.264 and HEVC go through the Microsoft decoders,
  which is the common path on Intel, AMD and NVIDIA.
- Which encoder controls work depends on the vendor MFT: a control the MFT
  rejects (constant quality, CBR, B-frames, low latency, a profile) makes
  `NewEncoder` return `ErrUnsupported` naming the control and the MFT.
  B-frames request up to two consecutive B-pictures.
- 8-bit 4:2:0 only (NV12). 10-bit HEVC (P010) is rejected. Above 1920x1088
  the Microsoft H.264 decoder may fall back to software internally without
  reporting it.
- 32-bit Windows is out of scope; the backend builds only for `windows/amd64`
  and `windows/arm64`.
- The backend was written and cross-compiled on macOS against the Windows
  SDK headers (GUIDs, vtable layouts and structure sizes are checked at
  compile time) and has **not been run on Windows hardware yet**. Run
  `go test ./...` on a Windows machine with ffmpeg and ffprobe installed to
  verify; the conformance tests skip when `Probe` reports no hardware engine,
  and the encoder tests for optional controls skip when the vendor MFT
  rejects them.

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
		return err
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

## Testing

Unit tests run everywhere. The decode and encode conformance tests run on
Apple Silicon and on Windows machines with hardware engines, and use the
`ffmpeg` and `ffprobe` commands as the reference (they are skipped when
ffmpeg is not installed or when no hardware engine is present). Decoded
B-frame streams must match ffmpeg's output frame for frame in display order;
the reorder logic is also checked without hardware, against the presentation
timestamps ffmpeg writes into an MP4 of the same stream. RGB output and
input are compared to ffmpeg's conversion by block-averaged PSNR (the two
converters interpolate chroma differently) and RGBA must be the exact
mirror of BGRA. Encoded streams are decoded by ffmpeg and compared to the
source by PSNR, and their keyframe and B-frame structure is checked with
ffprobe.

```sh
go test ./...
CGO_ENABLED=0 go test ./...   # exercises the cgo-free callback path
./scripts/crossbuild.sh       # CGO_ENABLED=0 builds for every target
(cd ebitenvideo && go test ./...)   # separate module: timeline logic plus a hardware playback test
```

Project knowledge (requirements, decisions, backend notes) lives in
`.knowledge/` and is maintained with the knowledge-memory skill.
