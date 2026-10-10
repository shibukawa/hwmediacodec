# hwmediacodec

Hardware video decoding and encoding from Go, without cgo.

The library loads the operating system's codec engines at run time
(VideoToolbox on macOS, NVDEC/NVENC, Intel VPL and VA-API on Linux, and
NVDEC/NVENC and Media Foundation on Windows)
through [purego](https://github.com/ebitengine/purego) and, for Media
Foundation, `golang.org/x/sys/windows` plus raw COM vtable calls, so
`CGO_ENABLED=0 go build` works and the module cross-compiles from one machine.

## Packages

| Import path | What it is |
| --- | --- |
| `github.com/shibukawa/hwmediacodec` | The codec API: `Probe`, `NewDecoder`, `NewEncoder`, packets and frames |
| `.../encoding/annexb` | H.264/HEVC Annex-B byte streams: split into NAL units and access units, classify NAL units |
| `.../mediacontainer/mp4` | MP4/MOV demuxer and muxer around the codec's packets, fMP4 segmenter |
| `.../mediacontainer/ivf` | IVF reader and writer (AV1) |
| `.../image/heif` | HEIC still images (HEVC), registered with the standard `image` package |
| `.../image/avif` | AVIF still images (AV1), registered with the standard `image` package |
| `.../net/hls` | Live HLS playlist and HTTP handler over the fMP4 segments |
| `.../capture` | Records what a renderer draws (an Ebitengine screen, for example) through the encoder into a sink |
| `.../net/webrtc` | Separate module: one H.264 stream broadcast to browsers over WebRTC (pion) |
| `.../ebitenvideo` | Separate module: video playback as an `*ebiten.Image` |
| `.../examples` | Separate module: complete programs (converter, thumbnails, recorder, HLS and WebRTC servers, player) |

The core module depends on purego, `golang.org/x/sys` and, for the MP4 and
image packages, [mp4ff](https://github.com/Eyevinn/mp4ff); all are pure Go.
Ebitengine and pion are only pulled in by the separate modules
(`ebitenvideo`, `net/webrtc`, `examples`).

## Status

| Platform | Backend | Decode | Encode |
| --- | --- | --- | --- |
| macOS, Apple Silicon | VideoToolbox | H.264, HEVC; display order; NV12, RGBA or BGRA in CPU memory. AV1 on M3 and newer: Main profile 8-bit and 10-bit 4:2:0 in (IVF / ISOBMFF temporal units), 8-bit out | H.264, HEVC; NV12, RGBA or BGRA in, Annex-B out (no AV1 encoder exists on Apple Silicon) |
| Linux, AMD (Mesa) and Intel (iHD / i965) | VA-API | H.264, HEVC; display order; NV12 in CPU memory; **not yet verified on hardware** | H.264, HEVC; NV12 in, Annex-B out; **not yet verified on hardware** |
| Linux, NVIDIA (proprietary driver 470+) | NVDEC / NVENC | H.264, HEVC; display order; NV12 in CPU memory; **not yet verified on hardware** | H.264, HEVC; NV12, RGBA or BGRA in, Annex-B out; **not yet verified on hardware** |
| Linux, Intel (Tiger Lake and newer with `libmfx-gen`; older GPUs with the Media SDK runtime) | Intel VPL (Quick Sync Video) | H.264, HEVC; display order; NV12 in CPU memory; **not yet verified on hardware** | H.264, HEVC; NV12 in, Annex-B out; **not yet verified on hardware** |
| Windows x64, NVIDIA (driver 471.41+) | NVDEC / NVENC | H.264, HEVC; display order; NV12 in CPU memory; **not yet verified on hardware** | H.264, HEVC; NV12, RGBA or BGRA in, Annex-B out; **not yet verified on hardware** |
| Windows x64 / ARM64, Intel, AMD (and NVIDIA as the fallback) | Media Foundation (decode: Microsoft MFTs + Direct3D 11 DXVA; encode: vendor hardware MFTs) | H.264, HEVC; display order; NV12 in CPU memory; **not yet verified on hardware** | H.264, HEVC; NV12 in, Annex-B out; **not yet verified on hardware** |

Decoded frames come back in display order: the slice headers are parsed in
Go to derive picture order counts, and frames are held back no longer than
the stream's reorder bound. `WithDecodeOrder` switches back to the order the
hardware produces. (AV1 temporal units already arrive in presentation order
and yield one frame each, so neither applies to them.)
`WithOutputFormat(hwmediacodec.RGBA)` (or `BGRA`) returns
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
- H.264 and HEVC input and output are Annex-B, one access unit per `Packet`;
  use `annexb.Reader` to split a raw elementary stream. AV1 input is one
  temporal unit per `Packet` in the low-overhead OBU format (what an IVF
  frame or an ISOBMFF `av01` sample holds, with or without temporal
  delimiters); use `ivf.Reader` to split an IVF file. The codec API does
  not parse containers; the `mediacontainer/mp4` package reads and writes
  MP4 files around it (see [MP4 files](#mp4-files)). MKV and TS are not
  covered, and AVCC/HVCC output is not offered by the encoders themselves.
- On an M3, 1080p H.264 with B-frames decodes at roughly 780 frames per
  second to NV12 in display order, 350 to BGRA and 250 to RGBA, including
  the copy; 1080p H.264 encodes at roughly 200 frames per second from NV12
  and 180 from RGBA or BGRA.
- The VideoToolbox hardware decoder rejects very small pictures (64x48
  fails with "decoder malfunction"; 96x64 works).
- The VideoToolbox encoder rounds odd picture sizes down to even: a
  321x203 request yields a 320x202 stream. Pad to even and crop at the
  consumer (as `image/heif` does with a `clap` property).
- `Encoder.Flush` blocks until VideoToolbox has emitted every pending frame;
  it does not observe context cancellation once the call has started.
- Intel Macs are out of scope; the VideoToolbox backend requires hardware
  engines unless `WithSoftwareFallback` is given.
- VA-API decoding: H.264 and HEVC, 8-bit 4:2:0 only (HEVC Main; Main 10,
  the range extensions and screen content coding are rejected with
  `ErrUnsupported`). H.264 is progressive frames only (interlaced field
  pictures are rejected with `ErrUnsupported`); HEVC field sequences decode
  as one frame per field. VA-API is a slice-level API, so the bitstream
  parsing, picture order count, reference picture marking (H.264) or
  reference picture sets (HEVC) and reference list construction run in Go
  (`internal/h264`, `internal/hevc`), and the driver only accelerates the
  slice data. An HEVC decoder that starts at a CRA picture, or is flushed
  and resumes at one, drops the RASL pictures that follow it, as the
  specification requires. `Send` returns `ErrAgain` when more than a few
  decoded frames are waiting for `Receive`; drain and resend.
- VA-API encoding: H.264 and HEVC (Main profile), I and P frames with one
  reference; `WithBFrames` is accepted but no B-frames are produced yet.
  Pictures must have even width and height. `WithQuality` maps to a constant
  quantiser (CQP), `WithBitrate` to the driver's VBR or CBR rate control (the
  HRD buffer is one second of the peak rate), and without either a constant
  quantiser of 26 is used. The parameter sets and slice headers are written
  in Go and handed to the driver as packed headers when it accepts them
  (Intel requires this; Mesa generates its own otherwise), so every keyframe
  carries in-band SPS/PPS (and VPS for HEVC). Each picture is encoded
  synchronously inside `Send`. For HEVC the coding tree block size and the
  coding tools (AMP, SAO) follow what the driver reports through
  `VAConfigAttribEncHEVCBlockSizes` and `VAConfigAttribEncHEVCFeatures`.
  When the driver writes its own SPS, the picture size it declares is checked
  against the requested one on the first keyframe and a mismatch is an
  error, not a wrongly sized stream. Intel encoders that have no P-frames
  (they need "generalised B" frames) report `ErrUnsupported`; the Intel VPL
  backend serves those GPUs.
- The VA-API backend was written on a Mac. Everything up to the driver is
  verified without hardware (see Testing), including complete decode and
  encode runs through libva against a checking test driver, but no run on
  AMD or Intel hardware has happened yet: what the GPU makes of the
  buffers, and the pixels, are still to be confirmed.

### macOS notes

AV1 decoding uses the hardware decoder Apple added with the M3
(`VTIsHardwareDecodeSupported('av01')`). `Probe` lists `av1 decode` only on
such machines, and `NewDecoder` reports `ErrUnsupported` on M1 and M2 Macs
even with `WithSoftwareFallback`: macOS 26 lists a "SW AV1 Decoder"
(`com.apple.videotoolbox.videodecoder.av1.sw`) in `VTCopyVideoDecoderList`,
but VideoToolbox refuses to create a session with it
(`kVTCouldNotFindVideoDecoderErr` with hardware decoding disabled, with or
without `VTRegisterSupplementalVideoDecoderIfAvailable`, which changes
nothing for AV1; measured on an M3). No Apple Silicon chip has an AV1
encoder, so `NewEncoder` with `AV1` reports `ErrUnsupported`.

- A `Packet` is one temporal unit in the low-overhead OBU format, which is
  what an IVF frame or an ISOBMFF `av01` sample holds; `ivf.Reader` splits
  an IVF file and `ivf.Writer` writes one. Temporal delimiter OBUs may be
  present or absent: VideoToolbox produced identical frames either way, so
  the unit is handed over as it is. The sequence header is parsed in Go
  (`internal/av1`) to build the `av1C` record that goes into the format
  description (`CMVideoFormatDescriptionCreate` with the
  `SampleDescriptionExtensionAtoms` extension); a new sequence header (for
  example a size change) starts a new decompression session, and after
  `Flush` decoding resumes at the next unit with a shown key frame.
- Every temporal unit yields exactly one frame, in presentation order and
  with the packet's PTS; hidden alternate reference frames come out when
  the stream shows them through `show_existing_frame`. The Go reorder layer
  is therefore not used for AV1 and `WithDecodeOrder` has no effect.
- Main profile streams decode, 8-bit and 10-bit 4:2:0; the output is 8-bit
  (NV12, RGBA or BGRA). 8-bit decoding is bit-exact with libdav1d. 10-bit
  pictures are converted by VideoToolbox itself, within 53.5 dB PSNR of
  ffmpeg's conversion (the two round differently). High and Professional
  profile (4:4:4, 4:2:2, 12-bit) and monochrome streams report
  `ErrUnsupported`. Untagged streams are converted to RGB with BT.601, as
  for the other codecs.
- On an M3, 1080p AV1 (SVT-AV1 preset 10, 300 frames) decodes at roughly
  820 frames per second to NV12, 310 to BGRA and 220 to RGBA, including the
  copy.

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

## MP4 files

The codec API stops at the elementary stream: packets in, frames out,
Annex-B on both ends. Files need a container, and
`github.com/shibukawa/hwmediacodec/mediacontainer/mp4` is the bridge for
MP4 and MOV, built on the box parser of
[mp4ff](https://github.com/Eyevinn/mp4ff) (the one dependency this package
adds; programs that do not import it do not link it):

```go
d, _ := mp4.Open("movie.mp4")
v := d.Video()                      // codec, size, time scale, sample table
pkt, _ := v.Packet(i)               // Annex-B access unit with SPS/PPS in front of keyframes
dec, _ := hwmediacodec.NewDecoder(ctx, v.Codec, hwmediacodec.WithTimeScale(int32(v.TimeScale)))
dec.Send(ctx, pkt)                  // pkt.PTS is the MP4 sample time

m, _ := mp4.Create("out.mp4")
vw, _ := m.AddVideoTrack(hwmediacodec.HEVC, v.TimeScale)
vw.WritePacket(p)                   // p from Encoder.Receive: Annex-B, PTS, DTS, Keyframe
aw := m.AddPassthroughTrack(d.Others()[0])
aw.WriteSample(s)                   // audio copied as is
m.Close()                           // writes moov
```

What the package takes care of:

- `Track.MediaTimeOffset` carries the edit list. A B-frame stream's first
  picture has a PTS above its DTS and the muxer hides that delay with an
  `elst`; sample PTS minus the offset is the time a player shows the frame.
- The muxer learns SPS/PPS/VPS from the packets (hwmediacodec encoders put
  them in front of every keyframe) and strips them from the samples, so the
  output is `avc1`/`hvc1` as QuickTime and browsers expect.
- VideoToolbox numbers decode times from the first PTS when B-frames are on,
  so a reordered picture can carry DTS > PTS. MP4 needs DTS ≤ PTS, so the
  muxer shifts every DTS back by the largest lag; durations are unchanged and
  the edit list absorbs the start delay. The tests check that ffprobe sees
  the same presentation times as in the source.
- Progressive output only (`ftyp`, `mdat`, `moov` at the end), `stco` or
  `co64` as the size requires, one chunk per run of samples of the same
  track. Fragmented input is rejected.
- `VideoTrack.PacketSource()` hands out access units with presentation
  times and seeks by the sync-sample table, which is what
  `ebitenvideo.NewPlayerFromSource` takes; `VideoTrack.ElementaryStream()`
  is the same track as a raw Annex-B stream.
- `CreateVideoFile` is the one-track file for encoder output (a
  `hwmediacodec.PacketWriteCloser`), and `Segmenter` cuts encoder packets into CMAF/fMP4
  segments for live HLS: an init segment, then one `moof`+`mdat` per
  segment, each starting at a keyframe. It needs PTS == DTS, so encode
  without B-frames.

## Live HLS

`github.com/shibukawa/hwmediacodec/net/hls` serves those
segments as a live HLS stream. A `Playlist` keeps a sliding window of them
in memory (nothing is written to disk) and is an `http.Handler` for the
media playlist, the init segment and the media segments:

```go
playlist := hls.NewPlaylist(6, 2*time.Second)             // six segments of two seconds
seg, _ := mp4.NewSegmenter(hwmediacodec.H264, timeScale, 2*time.Second, playlist.SetInit, playlist.Add)
// seg.WritePacket(p) for every encoder packet (a hwmediacodec.PacketWriteCloser)
http.Handle("/live/", playlist)                            // players open /live/index.m3u8
// at the end
seg.Close()
playlist.End()                                             // EXT-X-ENDLIST
```

- Set the encoder's keyframe interval to the segment length (segments are
  cut at keyframes) and encode without B-frames (`WithLowLatency`).
- The handler answers by the last path element (`index.m3u8`, `init.mp4`,
  `seg_N.m4s`), so it can be mounted under any prefix; it answers 503 until
  the first segment exists and sends `Access-Control-Allow-Origin: *`.
- Safari plays the stream natively; other browsers need hls.js or another
  MSE player (`examples/hls` has such a page). HEVC plays in Safari only.
  Latency is a few seconds, which is what plain HLS gives;
  `net/webrtc` is the low-latency path.

## WebRTC

`github.com/shibukawa/hwmediacodec/net/webrtc` is the low-latency
counterpart of `net/hls`: a `Broadcaster` hands every access unit of an
H.264 stream to a [pion](https://github.com/pion/webrtc) track per viewer.
It is a module of its own, so that the WebRTC stack stays out of the core
module's dependencies (`go get github.com/shibukawa/hwmediacodec/net/webrtc`).

```go
var rec *capture.Recorder
bc := webrtc.NewBroadcaster(60, nil, func() { rec.RequestKeyframe() }) // nil: no STUN/TURN, LAN only
rec, _ = capture.New(w, h, bc, capture.Options{
	FPS: 60, LowLatency: true, Profile: hwmediacodec.ProfileBaseline})
http.Handle("/offer", bc)   // the page POSTs its SDP offer and gets the answer
```

- `Broadcaster` is a `hwmediacodec.PacketWriteCloser`; packet times
  are in 90 kHz units. pion's H.264 payloader splits the Annex-B NAL units
  into RTP (STAP-A for the parameter sets, FU-A for large slices).
- Signalling is a single HTTP POST of the browser's SDP offer as JSON; the
  answer is returned once ICE gathering is done, so no trickle ICE and no
  WebSocket. `Accept` does the same for programs with their own signalling.
- A viewer joining or reporting a picture loss calls the keyframe callback,
  and a viewer only starts receiving at a keyframe. Encode with
  `WithLowLatency`, Baseline profile and no B-frames.
- `examples/webrtc` has a player page; measured there in a Chromium browser
  on the same machine at 1280x720, the jitter buffer delay was about 8 ms.

## Screen recording

`github.com/shibukawa/hwmediacodec/capture` is a package of the core
module that records what a game or any other renderer draws. A `Recorder`
reads the pixels of a `Source` on every frame, encodes them on a
background goroutine and pushes the packets into a
`hwmediacodec.PacketWriteCloser`:

```go
rec, err := capture.New(1280, 720, sink, capture.Options{FPS: 60, Bitrate: 8_000_000})

func (g *game) Draw(screen *ebiten.Image) {
	g.scene.Draw(screen)
	rec.Capture(screen)      // ReadPixels, then the encoder runs on its own goroutine
}
// on exit
rec.Close()                  // flushes the encoder, closes the sink
```

- A `Source` is `Bounds()` plus `ReadPixels([]byte)` (tightly packed
  RGBA). `*ebiten.Image` satisfies it as it is, but the package does not
  import Ebitengine: anything that can hand out RGBA pixels can be
  recorded.
- `Capture` never blocks on the encoder. `ReadPixels` runs on the calling
  goroutine (about 2 ms at 720p for an Ebitengine screen on an M3; the GPU
  has to finish the frame first, so a heavy scene shows up in this
  number); the buffer then goes through a bounded queue to the encoder,
  and when the queue is full the frame is dropped and counted (`Dropped`).
- PTS come from the wall clock quantised to the frame rate, in units of
  `capture.TimeScale` (90 kHz), so a dropped frame leaves a gap instead
  of speeding the recording up, and a 120 Hz display showing a 60 fps game
  does not record every frame twice. `CaptureAt` takes the time stamp
  explicitly.
- The sink is any `hwmediacodec.PacketWriteCloser`
  (`WritePacket(hwmediacodec.Packet)` plus `Close`);
  `hwmediacodec.PacketWriterFunc` adapts a closure. The packets are Annex-B access
  units with in-band parameter sets, so writing `Packet.Data` to a file
  gives a playable elementary stream. `mediacontainer/mp4` has an MP4
  file sink (`CreateVideoFile`) and an fMP4 segmenter for HLS, and
  `net/webrtc` a sink that feeds a pion track.
- `Options` selects the codec, bitrate or quality, keyframe interval, low
  latency and profile; `Extra` appends any other `EncoderOption`.
  `RequestKeyframe` forces a keyframe on the next captured frame (a new
  viewer joining a live stream).

## HEIC and AVIF images

`github.com/shibukawa/hwmediacodec/image/heif` (HEIC: HEVC pictures) and
`github.com/shibukawa/hwmediacodec/image/avif` (AVIF: AV1 pictures) read
and write HEIF still images with the hardware codecs. They follow the
conventions of `image/jpeg` and `image/png`, and importing one registers
its format with the standard `image` package:

```go
import (
	_ "github.com/shibukawa/hwmediacodec/image/avif"
	_ "github.com/shibukawa/hwmediacodec/image/heif"
)

img, format, err := image.Decode(file)         // format: "heic", "avif" or "heif"
cfg, _, err := image.DecodeConfig(file)        // size after rotation; nothing is decoded
```

Both packages have the same functions:

```go
img, err := heif.Decode(r)                     // image.Image (an *image.RGBA)
err = heif.Encode(w, img, nil)                 // HEIC with the encoder's default quality
err = heif.Encode(w, img, &heif.Options{Quality: 0.8, TileSize: 512, Rotation: 90})

rgba, info, err := heif.DecodeBytes(data, hwmediacodec.WithSoftwareFallback())
info, err = heif.DecodeInfo(data)              // size, tiles, rotation; no decoder needed
```

- Each package reads only its own codec and says so when handed the
  other's file. A file whose major brand is the generic `mif1` does not
  name its codec: `image.Decode` reports it as `"heif"` and decodes it when
  the package for its pictures is imported.
- Decoding needs a hardware HEVC or AV1 decoder and encoding a hardware
  encoder; the error wraps `hwmediacodec.ErrUnsupported` on a machine
  without one (no Apple Silicon chip encodes AV1, so `avif.Encode` fails
  there). The packages have no software codec of their own;
  `examples/imgconv` shows how to fall back to cgo-free ones.
  `DecodeConfig` and `DecodeInfo` read only the file structure and work
  everywhere.
- The result is opaque RGBA: alpha planes are neither read nor written,
  and only 8-bit 4:2:0 pictures are handled (iPhone HDR photos are 10-bit
  and out of scope).
- Colours: the decoder converts to RGB with what the bitstream declares.
  An HEVC stream that declares nothing (some software encoders leave the
  VUI out and describe the colours only in the item's `colr` property) is
  converted from NV12 in Go with the `colr` matrix and range; otherwise a
  full-range picture would come out with stretched contrast.

A HEIF file is an ISOBMFF `meta` box of items (coded pictures, a `grid`
that tiles them) with properties (`hvcC`/`av1C` decoder configuration,
`ispe` size, `irot`, `imir`, `clap`, `colr`, `pixi`) and an `mdat` with the
coded data. The two packages share the code that parses and writes it
(mp4ff only supplies the `hvcC`/`av1C` record parsers) and leave the
pictures to the codecs:

- **Decode**: for each coded item, `hvcC`'s parameter sets plus the
  length-prefixed NAL units become one Annex-B packet (AV1: the temporal
  unit, with the sequence header from `av1C` if the item lacks one); all
  items go through one decoder opened with `WithOutputFormat(RGBA)` and
  `WithDecodeOrder()`, grids are stitched tile by tile and cropped to the
  grid size, then `clap`, `irot` and `imir` are applied in their stored
  order.
- **Encode**: one keyframe per picture or tile (`WithKeyframeInterval(1)`,
  `ForceKeyframe`, `WithQuality`), VPS/SPS/PPS from the packet into `hvcC`,
  the slices as the item data. `TileSize` writes a grid like phone cameras
  do; `Rotation` stores an `irot`. Odd sizes are padded by a replicated
  row or column and declared through `clap`, because the hardware encoders
  work on even 4:2:0 pictures (VideoToolbox rounds an odd request down to
  320x202 for 321x203).
- **AVIF**: decoding works wherever `Probe` lists an AV1 decoder (M3 and
  newer Macs); encoding needs an AV1 encoder, which no Apple Silicon chip
  has, so `avif.Encode` returns `ErrUnsupported` there and
  works unchanged on a platform that gains one.

Tests use macOS ImageIO (`sips`) as the reference for the files this
package writes (single, grid, rotated, odd sizes) and for HEIC input, and
ffmpeg for AVIF input and for the rotation direction (ffmpeg maps `irot` to
a display matrix and autorotates; ImageIO keeps it as orientation
metadata). ImageIO resamples `clap`-cropped pictures instead of cropping,
and ffmpeg rounds odd `clap` sizes to even, so odd pictures are compared
with the source instead. 8-bit 4:2:0 only; iPhone HDR photos (10-bit) are
out of scope.

## Examples

The [`examples/`](examples/) directory is a third Go module with complete
programs built on the library: a video file converter that keeps
timestamps and copies audio (`examples/convert`), a keyframe thumbnail
extractor (`examples/thumbnails`), an Ebitengine screen recorder on top
of the `capture` package (`examples/record`), live HLS and WebRTC
servers for a fireworks show (`examples/hls`, `examples/webrtc`), a video
player
(`examples/player`), video as a texture on a box and in a Kage shader
(`examples/texture`) and a HEIC/AVIF converter (`examples/imgconv`). The players default to a bundled clip
(`examples/assets`). See [examples/README.md](examples/README.md).

```sh
cd examples
go run ./convert -codec hevc -bitrate 6M input.mp4 output.mp4
go run ./thumbnails -every 10s -width 320 -o thumbs input.mp4
go run ./record -o capture.mp4 -seconds 10
go run ./hls -addr :8080      # then open http://localhost:8080/
go run ./webrtc -addr :8080   # same, about 100 ms of latency
go run ./player               # the bundled clip; space pause, arrows seek
go run ./texture              # 1 flat, 2 box, 3 shader
go run ./imgconv photo.heic photo.png
go run ./imgconv -quality 0.8 picture.png picture.heic
```

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
(`Player.Skipped` counts them). `NewPlayerFromSource` takes a
`hwmediacodec.PacketReader`
instead of a reader: anything that hands out access units with
presentation times, such as the MP4 demuxer in `mediacontainer/mp4`
(`VideoTrack.PacketSource()`). A reader that is a `hwmediacodec.PacketSeeker`
(a keyframe index) gives the player `Seek`, `Length` and looping; an
`io.ReadSeeker` passed to `NewPlayer` gets the same by scanning the stream
once for keyframes on the first seek. `Seek(t)` restarts decoding at the
keyframe before `t` and drops the frames up to it, so the next picture
shown is the one at `t`; `Position` reports `t` meanwhile. A minimal
example plays a raw stream in a window (space pauses, the arrow keys
seek); the MP4-capable player with a bundled clip is `examples/player`:

```sh
cd ebitenvideo && go run ./example -codec h264 -fps 30 ../video.h264
cd examples && go run ./player
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
go run ./cmd/hwmediacodec decode -codec av1 -hash input.ivf                  # AV1: IVF input (macOS, M3+)
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
The HEVC side is checked the same way: every parsed syntax element and the
position where slice data starts against `trace_headers`, picture order
counts and both reference lists of every picture against the lists the
`x265` command line encoder reports it used (these tests skip when `x265` is
not installed), and inter-predicted reference picture sets against the
reference encoder's random-access configuration. The VA-API parameter
buffers built from HEVC streams (reference frames and their set flags,
reference list indices, `slice_data_byte_offset`, `st_rps_bits`, prediction
weights) are compared with the same two oracles on any platform, and the
parameter sets and slice headers the HEVC encoder writes are read back by
ffmpeg and ffprobe. The AV1 OBU splitter, sequence header and frame header
parsers are checked against `trace_headers` on SVT-AV1 streams (8-bit and
10-bit), and the `av1C` record against the one ffmpeg writes into an MP4 of
the same stream; the `mediacontainer/ivf` package is checked against ffmpeg's own IVF
files. These tests skip when ffmpeg lacks libsvtav1. The AV1 decode
conformance tests need libdav1d as well: every NV12 frame of an 8-bit
stream with hidden frames and `show_existing_frame` must equal dav1d's
output exactly, and a 10-bit stream is compared by PSNR.

The VA-API backend itself runs without a GPU against a fake VA driver
(`internal/vaapi/testdata/fakedriver`, about 1,400 lines of C).
`scripts/vaapi_fake_driver_test.sh` builds it in a container and runs the
backend through the real libva, for H.264 and HEVC, decoding and encoding.
The driver decodes nothing; it checks what it is handed through the libva C
headers (buffer sizes, that every reference picture is in the surface the
backend names, reference list indices, slice data offsets, the encoder's
sequence, picture, slice, rate control and packed header buffers) and paints
each decoded surface with a pattern that names its picture order count, so
the tests can tell that the right surface is copied out for every frame,
cropped and in display order. It runs as several driver personalities
(packed headers as Mesa or as Intel, with and without HEVC capability
attributes, with and without `vaDeriveImage`), and `PLATFORM=linux/amd64`
runs it on that architecture through emulation.
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

The `mediacontainer/mp4` tests need only ffmpeg and ffprobe: they compare
sample tables, presentation times and decoded frame checksums with
ffprobe's view of the same files and feed ffmpeg-made streams through the
segmenter, and the `net/hls` test lets ffprobe play the served
playlist over HTTP. The `capture` tests and the recorder-to-MP4 test need a
hardware encoder and skip otherwise. The `image/heif` and `image/avif` tests use macOS
ImageIO (`sips`) and ffmpeg as references and need the hardware codecs.

```sh
go test ./...
CGO_ENABLED=0 go test ./...   # exercises the cgo-free callback path
./scripts/crossbuild.sh       # CGO_ENABLED=0 builds for every target
HWMEDIACODEC_BACKENDS=vaapi go test -count=1 .   # Linux: one backend at a time
./scripts/vaapi_fake_driver_test.sh   # VA-API backend against the fake driver (needs docker)
(cd ebitenvideo && go test ./...)   # separate module: timeline logic plus a hardware playback test
(cd net/webrtc && go test ./...)    # separate module: a pion viewer receives an ffmpeg-made stream (ffmpeg only)
(cd examples && go test ./...)      # separate module: sample end-to-end tests (ffmpeg, most also hardware)
```

Project knowledge (requirements, decisions, backend notes) lives in
`.knowledge/` and is maintained with the knowledge-memory skill. After
re-running the tests a verification entry names, refresh its file hashes with
`python3 scripts/knowledge_refresh_basis.py . --apply`.
