# hwmediacodec

Hardware video decoding and encoding from Go, without cgo.

The library loads the operating system's codec engines at run time
(VideoToolbox on macOS, NVDEC/NVENC, Intel VPL and VA-API on Linux, and
NVDEC/NVENC and Media Foundation on Windows)
through [purego](https://github.com/ebitengine/purego) and, for Media
Foundation, `golang.org/x/sys/windows` plus raw COM vtable calls, so
`CGO_ENABLED=0 go build` works and the module cross-compiles from one machine.

## Status

| Platform | Backend | Decode | Encode |
| --- | --- | --- | --- |
| macOS, Apple Silicon | VideoToolbox | H.264, HEVC; display order; NV12, RGBA or BGRA in CPU memory | H.264, HEVC; NV12, RGBA or BGRA in, Annex-B out |
| Linux, AMD (Mesa) and Intel (iHD / i965) | VA-API | H.264; display order; NV12 in CPU memory | H.264; NV12 in, Annex-B out |
| Linux, NVIDIA (proprietary driver 470+) | NVDEC / NVENC | H.264, HEVC; display order; NV12 in CPU memory; **not yet verified on hardware** | H.264, HEVC; NV12, RGBA or BGRA in, Annex-B out; **not yet verified on hardware** |
| Linux, Intel (Tiger Lake and newer with `libmfx-gen`; older GPUs with the Media SDK runtime) | Intel VPL (Quick Sync Video) | H.264, HEVC; display order; NV12 in CPU memory; **not yet verified on hardware** | H.264, HEVC; NV12 in, Annex-B out; **not yet verified on hardware** |
| Windows x64, NVIDIA (driver 471.41+) | NVDEC / NVENC | H.264, HEVC; display order; NV12 in CPU memory; **not yet verified on hardware** | H.264, HEVC; NV12, RGBA or BGRA in, Annex-B out; **not yet verified on hardware** |
| Windows x64 / ARM64, Intel, AMD (and NVIDIA as the fallback) | Media Foundation (decode: Microsoft MFTs + Direct3D 11 DXVA; encode: vendor hardware MFTs) | H.264, HEVC; display order; NV12 in CPU memory; **not yet verified on hardware** | H.264, HEVC; NV12 in, Annex-B out; **not yet verified on hardware** |

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
  Alpha is 255 on output and ignored on input. The VA-API, Intel VPL and
  Media Foundation backends offer NV12 only for now; requesting RGBA or BGRA
  there reports `ErrUnsupported`.
- On Windows the display order comes from the Media Foundation decoder
  itself, and on Linux with Intel VPL from the VPL runtime, so
  `WithDecodeOrder` has no effect there.
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

### Windows (NVDEC / NVENC)

On Windows x64 the NVIDIA backend (`internal/nvidia`, the same code as on
Linux) is registered before Media Foundation, so a machine with the NVIDIA
driver decodes through NVDEC and encodes through NVENC directly and gets the
controls described under "Linux notes" below: RGBA and BGRA encoder input,
B-frames, VBR/CBR, constant QP, low-latency tuning, and frames in decode order
with `WithDecodeOrder`. Media Foundation remains the backend for Intel and
AMD GPUs, and takes over when the NVIDIA backend reports `ErrUnsupported`.

- The backend loads `nvcuda.dll`, `nvcuvid.dll` and `nvEncodeAPI64.dll` from
  the Windows system directory only (`LOAD_LIBRARY_SEARCH_SYSTEM32`); the
  display driver installs them there. Driver 471.41 or newer is needed for
  NVENC API 11.1. `Probe` reports no `nvidia` capability, rather than an
  error, when a DLL or a GPU is missing.
- On a laptop with an integrated GPU next to an NVIDIA one, the NVIDIA GPU is
  used whenever its driver is present. `HWMEDIACODEC_NVIDIA_DEVICE` picks the
  CUDA device ordinal on machines with several NVIDIA GPUs; there is no
  switch yet to prefer Media Foundation over an installed NVIDIA driver.
- Windows on ARM has no NVIDIA driver libraries, so `windows/arm64` uses
  Media Foundation only.
- Encoding uses NVENC's synchronous mode (no completion events), as on Linux.
- The two cuvid structures that contain `unsigned long` fields are 32-bit
  there on Windows; their layout is asserted at compile time against values
  measured with `x86_64-w64-mingw32-gcc` and
  `clang --target=x86_64-pc-windows-msvc`, so `GOOS=windows go build` checks
  it from any host. Like the Media Foundation backend this code has **not
  been run on Windows hardware yet**.

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

The Intel VPL backend (`internal/vpl`) loads the VPL dispatcher
`libvpl.so.2`, which finds the GPU runtime: `libmfx-gen.so.1.2` for Tiger
Lake (Gen12) and newer including Arc, or the legacy Media SDK runtime
`libmfxhw64.so.1` for older GPUs when it is installed. The runtime renders
through VA-API, so `libva2`, `libva-drm2` and the iHD driver
(`intel-media-va-driver`) are needed as well; on Debian and Ubuntu the
packages are `libvpl2` and `libmfx-gen1.2`. VPL is a full codec API: the
runtime parses the bitstream and returns frames in display order, and its
encoders write complete access units, so H.264 and HEVC both work and no
slice-level bookkeeping runs in Go. Decoding yields 8-bit 4:2:0 NV12 (10-bit
streams report `ErrUnsupported`), copied to CPU memory per picture; field
pairs of interlaced streams come back as one woven frame. Encoding takes
NV12 with even dimensions and supports B-frames (two consecutive, when the
GPU has them), VBR/CBR, constant QP for `WithQuality` (and QP 26 without a
bitrate or quality), low latency (no B-frames) and profiles; keyframes are
IDR pictures with in-band VPS/SPS/PPS, placed by `WithKeyframeInterval` and
`Frame.ForceKeyframe` only. The backend opens the first render node that
belongs to an Intel device; set `HWMEDIACODEC_VPL_DEVICE` to a render node
path to choose one. It is registered before VA-API, so on an Intel GPU with
a VPL runtime it serves the requests, and VA-API takes over when the runtime
is missing or rejects a configuration. Like the NVIDIA backend it was written
on a Mac: struct layouts are verified against the VPL headers with gcc, every
symbol binds against libvpl 2.8 and the dispatcher accepts the session filter
in a container, but no run on Intel hardware has happened yet.

`HWMEDIACODEC_BACKENDS` restricts and orders the backends, as a
comma-separated list of the names `Probe` reports: `HWMEDIACODEC_BACKENDS=vaapi`
bypasses Intel VPL to exercise the VA-API backend on the same GPU, and
`HWMEDIACODEC_BACKENDS=vpl,nvidia` prefers the integrated Intel GPU on a
machine that also has an NVIDIA card. Unknown names are ignored.

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
hardware engine (Apple Silicon, Linux with an NVIDIA, Intel VPL or VA-API
driver, or Windows with a GPU) and use `ffmpeg` and `ffprobe` as the reference. Decoded B-frame
streams must match ffmpeg's output frame for frame in display order. RGB
output and input are compared to ffmpeg's conversion by block-averaged PSNR
(the two converters interpolate chroma differently) and RGBA must be the
exact mirror of BGRA. Encoded streams are decoded by ffmpeg and compared to
the source by PSNR, and their keyframe and B-frame structure is checked
with ffprobe. All of them are skipped when ffmpeg is not installed, and the
encoder tests for optional controls skip when the hardware encoder rejects
them.

```sh
go test ./...
CGO_ENABLED=0 go test ./...   # exercises the cgo-free callback path
./scripts/crossbuild.sh       # CGO_ENABLED=0 builds for every target
HWMEDIACODEC_BACKENDS=vaapi go test -count=1 .   # Linux: one backend at a time
(cd ebitenvideo && go test ./...)   # separate module: timeline logic plus a hardware playback test
```

Project knowledge (requirements, decisions, backend notes) lives in
`.knowledge/` and is maintained with the knowledge-memory skill. After
re-running the tests a verification entry names, refresh its file hashes with
`python3 scripts/knowledge_refresh_basis.py . --apply`.
