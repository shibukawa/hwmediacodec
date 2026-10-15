# hwmediacodec

Hardware video decoding and encoding from Go, without cgo, and the
operating system's audio codecs (AAC, Opus) through the same API.

The library loads the operating system's codec engines at run time
(VideoToolbox on macOS, NVDEC/NVENC, Intel VPL and VA-API on Linux, and
NVDEC/NVENC and Media Foundation on Windows)
through [purego](https://github.com/ebitengine/purego) and, for Media
Foundation, `golang.org/x/sys/windows` plus raw COM vtable calls, so
`CGO_ENABLED=0 go build` works and the module cross-compiles from one machine.
The same VideoToolbox backend serves iOS, and Android has a backend on
the NDK MediaCodec API; on both purego needs the cgo runtime, so mobile
builds set `CGO_ENABLED=1` with the platform's clang (which
`ebitenmobile bind` does by itself) while the library still contains no cgo
code. See [iOS notes](#ios-notes) and [Android notes](#android-notes).
In a browser (`GOOS=js GOARCH=wasm`, the target Ebitengine builds for the
web) the engine is the WebCodecs API, reached through `syscall/js`; see
[Browser notes](#browser-notes).

## Packages

| Import path | What it is |
| --- | --- |
| `github.com/shibukawa/hwmediacodec` | The codec API: `Probe`, `NewDecoder`, `NewEncoder`, `NewAudioDecoder`, `NewAudioEncoder`, packets and frames |
| `.../encoding/annexb` | H.264/HEVC Annex-B byte streams: split into NAL units and access units, classify NAL units |
| `.../encoding/adts` | AAC in ADTS framing (`.aac` files): reader and writer around the codec's packets |
| `.../mediacontainer/mp4` | MP4/MOV demuxer and muxer around the codec's packets (video, AAC and Opus audio), fMP4 segmenter |
| `.../mediacontainer/ivf` | IVF reader and writer (AV1) |
| `.../image/heif` | HEIC still images (HEVC), registered with the standard `image` package |
| `.../image/avif` | AVIF still images (AV1), registered with the standard `image` package |
| `.../net/hls` | Live HLS playlist and HTTP handler over the fMP4 segments |
| `.../playback` | Plays a stream against a clock: background decoding, pacing, seeking, looping; frames to show |
| `.../capture` | Records what a renderer draws (an Ebitengine screen, for example) through the encoder into a sink |
| `.../net/webrtc` | Separate module: one H.264 stream broadcast to browsers over WebRTC (pion) |
| `.../ebitenvideo` | Separate module: `playback` with the frames in an `*ebiten.Image` |
| `.../examples` | Separate module: complete programs (converter, thumbnails, recorder, HLS and WebRTC servers, player) |

The core module depends on purego, `golang.org/x/sys` and, for the MP4 and
image packages, [mp4ff](https://github.com/Eyevinn/mp4ff); all are pure Go.
Ebitengine and pion are only pulled in by the separate modules
(`ebitenvideo`, `net/webrtc`, `examples`).

## Status

| Platform | Backend | Decode | Encode |
| --- | --- | --- | --- |
| macOS, Apple Silicon | VideoToolbox | H.264, HEVC; display order; NV12, RGBA or BGRA in CPU memory. AV1 on M3 and newer: Main profile 8-bit and 10-bit 4:2:0 in (IVF / ISOBMFF temporal units), 8-bit out | H.264, HEVC; NV12, RGBA or BGRA in, Annex-B out (no AV1 encoder exists on Apple Silicon) |
| Android 14 and later (arm64, x86_64) | MediaCodec (NDK `libmediandk.so`; cgo-enabled build) | H.264, HEVC, AV1 through the vendor's hardware codec (the framework's software codecs only with `WithSoftwareFallback`); display order; NV12 in CPU memory, RGBA or BGRA converted in Go; **verified on the Android 14 emulator (its decoders), a device run is pending** | H.264, HEVC; NV12 in, RGBA or BGRA converted in Go, Annex-B out; **verified on the Android 14 emulator (software encoders), a device run is pending** |
| Browsers (`GOOS=js GOARCH=wasm`; Chrome, Edge and other browsers with WebCodecs) | WebCodecs (`syscall/js`) | H.264, HEVC, AV1 through the codec the browser reports as a hardware one (its software decoders only with `WithSoftwareFallback`); display order; NV12 in CPU memory, RGBA or BGRA converted by the browser; **verified in Chrome on an Apple M3; Firefox on macOS decodes to RGBA / BGRA only and Safari's NV12 is not bit-exact (see the notes); other operating systems not run yet** | H.264, HEVC; NV12 in, RGBA or BGRA converted in Go, Annex-B out; **verified in Chrome on an Apple M3; H.264 in Firefox and H.264 / HEVC in Safari on macOS with the limits in the notes; other operating systems not run yet** |
| iOS 15 and later (iPhone, iPad) | VideoToolbox (the macOS backend; cgo-enabled build) | H.264, HEVC as on macOS; AV1 from the A17 Pro and the M-series iPads on; sessions lost in the background are reopened at the next keyframe; **verified in the iOS Simulator (software decoder), the iPhone run is pending** | H.264, HEVC as on macOS; **verified in the iOS Simulator, the iPhone run is pending** |
| Linux, AMD (Mesa) and Intel (iHD / i965) | VA-API | H.264, HEVC, AV1 (8-bit Main profile, on GPUs with an AV1 decoder); display order; NV12 in CPU memory, RGBA or BGRA converted in Go; **not yet verified on hardware** | H.264, HEVC; NV12 in, RGBA or BGRA converted in Go, Annex-B out; **not yet verified on hardware** |
| Linux, NVIDIA (proprietary driver 470+) | NVDEC / NVENC | H.264, HEVC, AV1 (8-bit Main profile, RTX 30 series and newer); display order; NV12 in CPU memory, RGBA or BGRA converted in Go; **not yet verified on hardware** | H.264, HEVC; NV12, RGBA or BGRA in, Annex-B out; **not yet verified on hardware** |
| Linux, Intel (Tiger Lake and newer with `libmfx-gen`; older GPUs with the Media SDK runtime) | Intel VPL (Quick Sync Video) | H.264, HEVC, AV1 (8-bit Main profile, Tiger Lake and newer); display order; NV12 in CPU memory, RGBA or BGRA converted in Go; **not yet verified on hardware** | H.264, HEVC; NV12 in, RGBA or BGRA converted in Go, Annex-B out; **not yet verified on hardware** |
| Windows x64, NVIDIA (driver 471.41+) | NVDEC / NVENC | H.264, HEVC, AV1 (8-bit Main profile, RTX 30 series and newer); display order; NV12 in CPU memory, RGBA or BGRA converted in Go; **not yet verified on hardware** | H.264, HEVC; NV12, RGBA or BGRA in, Annex-B out; **not yet verified on hardware** |
| Windows x64 / ARM64, Intel, AMD (and NVIDIA as the fallback) | Media Foundation (decode: Microsoft MFTs + Direct3D 11 DXVA; encode: vendor hardware MFTs) | H.264, HEVC, AV1 (8-bit Main profile, with the "AV1 Video Extension" on GPUs with an AV1 decoder); display order; NV12 in CPU memory, RGBA or BGRA converted in Go; **not yet verified on hardware** | H.264, HEVC; NV12 in, RGBA or BGRA converted in Go, Annex-B out; **not yet verified on hardware** |

AV1 is decoded wherever the GPU has an AV1 decoder; `Probe` lists `av1
decode` on those machines only. No backend encodes AV1 yet (Apple Silicon
has no AV1 encoder; on the other platforms the encoders exist in newer GPUs
and are not wired up).

Audio goes through the codecs the operating system or the browser ships,
which run in software everywhere (`Probe` lists them with `Hardware`
false):

| Platform | Backend | Decode | Encode |
| --- | --- | --- | --- |
| macOS, iOS | AudioToolbox (`AudioConverter`) | AAC-LC from mono to 5.1, HE-AAC and HE-AAC v2 (with the band replication and parametric stereo, at the full sample rate); Opus mono and stereo, any packet length; 16-bit or float samples | AAC-LC and Opus (48 kHz), mono and stereo; bitrate with VBR or CBR; **compared with ffmpeg on macOS, round trips verified in the iOS Simulator** |
| Browsers (`GOOS=js GOARCH=wasm`) | WebCodecs (`AudioDecoder`, `AudioEncoder`) | AAC and Opus as the browser has them: AAC-LC to 5.1 and Opus in Chrome, Firefox and Safari on macOS, HE-AAC in Chrome and Safari; **the same tests as on macOS pass in the three browsers on macOS, with the limits in the [browser notes](#browser-notes)** | Opus (48 kHz) in every browser; AAC-LC where the browser has an encoder (Chrome and Safari on macOS, not Firefox); the encoder's delay is measured in the page |
| Android, Windows, Linux | none yet | `ErrUnsupported` | `ErrUnsupported` |

See [Audio](#audio) for the API and for what using the system's codecs
means for AAC licensing.

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
  VideoToolbox assumes (BT.601 up to 704x576, BT.709 for larger pictures,
  measured on an M3).
  RGB input is converted with BT.709 and the stream is tagged accordingly.
  Alpha is 255 on output and ignored on input.
- The VA-API, Intel VPL and Media Foundation encoders take NV12 only, so
  for RGBA or BGRA input the library converts each frame to NV12 in Go
  before the backend sees it (`internal/pixconv`: BT.709 in video range,
  chroma averaged over 2x2 pixels, rows split across cores; about 0.8 ms per
  1080p frame on an Apple M3) and asks the backend to tag the stream as
  BT.709. The VA-API backend writes that into its own SPS, Intel VPL gets
  an `mfxExtVideoSignalInfo`, and Media Foundation gets the colour attributes
  on its media types; an encoder MFT that ignores them leaves the stream
  untagged, and players then guess the matrix from the picture size.
- The VA-API, NVDEC, Intel VPL and Media Foundation decoders return NV12
  only, so for RGBA or BGRA output the library converts each frame in Go
  after the backend (and after the reordering) has delivered it
  (`internal/pixconv`, about 0.8 ms per 1080p frame on an Apple M3). The
  matrix and the range follow the stream's VUI, or for AV1 the sequence
  header's colour configuration (BT.601, BT.709 or BT.2020, video or full
  range); a stream that names no matrix gets BT.709 when the
  picture is wider than 704 or taller than 576 pixels and BT.601 otherwise,
  which is where VideoToolbox draws the line, so the colours are the same
  on every backend. Every chroma sample colours its 2x2 block of pixels
  (no chroma interpolation), and the frames live in Go memory.
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

### iOS notes

iOS runs the macOS backend: the `darwin` build constraint covers `ios`, the
frameworks are the same four and are opened from the same paths, and the
public API behaves as on macOS. What differs:

- **Build.** purego needs the cgo runtime on iOS (its `CGO_ENABLED=0` mode
  is not available there), so build with `CGO_ENABLED=1`, `GOOS=ios` and
  Xcode's clang as `CC` with the SDK in `CGO_CFLAGS`/`CGO_LDFLAGS`;
  `scripts/mobilebuild.sh` does this for the device and the Simulator, and
  `ebitenmobile bind` sets it all up for an Ebitengine app. The library has
  no cgo code of its own. Desktop builds stay `CGO_ENABLED=0`.
- **Availability.** The decoder and encoder specification keys that require
  hardware (`kVTVideoDecoderSpecification_RequireHardwareAcceleratedVideoDecoder`
  and the encoder counterpart) exist on iOS 17 and 17.4 respectively; on
  older iOS they are absent and the backend leaves the specification empty,
  which is right because every codec VideoToolbox offers on an iOS device
  is a hardware one. Constant bitrate needs iOS 16, low-latency rate
  control iOS 14.5; `NewEncoder` reports `ErrUnsupported` below that.
  Nothing else the backend binds is newer than iOS 11.
- **Background.** iOS takes the hardware codecs away from an application
  that leaves the foreground: its VideoToolbox sessions are invalidated and
  every call returns `kVTInvalidSessionErr`. The backend then opens a new
  session on the same format description. Frames the old session still
  held are lost; a decoder drops pictures until the next keyframe (the
  packet that hit the error is decoded right away when it is one) and an
  encoder makes its next frame a keyframe, so the stream stays decodable.
  Neither `Send` nor `Flush` reports the event as an error. This path is
  exercised on macOS by invalidating the live session by hand
  (`internal/videotoolbox/invalidation_test.go`).
- **Simulator.** The Simulator has no hardware decoder, so `Probe` lists
  only the encoders and `NewDecoder` needs `WithSoftwareFallback`; the
  Simulator's VideoToolbox accepts the hardware-encoder requirement and
  lists `h264 encode` and `hevc encode` as hardware.
- **Status.** Verified in the iOS 26 Simulator on an M3 with the
  `hwmediacodec` CLI built for `ios/arm64`: `probe`; decoding a 30-frame
  H.264 stream with B-frames to NV12 and RGBA gives the same frames in the
  same display order as the Mac's hardware decoder; H.264 and HEVC encoding
  produces streams ffmpeg decodes (about 48 dB luma PSNR against the input);
  H.264 to HEVC transcoding works. **No run on an iPhone yet**; the
  `examples/mobile` app is the way to do it.
### Android notes

The Android backend (`internal/mediacodec`) drives the NDK MediaCodec C API
in `libmediandk.so` through purego: `AMediaCodec_createDecoderByType` and
`createEncoderByType` for `video/avc`, `video/hevc` and `video/av01`,
synchronous `dequeueInputBuffer` / `queueInputBuffer` /
`dequeueOutputBuffer` with short timeouts and no callbacks, and frames in
CPU memory through MediaCodec's byte buffers. It is the one codec API an
Android application has; the vendor's hardware codecs (Qualcomm, Exynos,
MediaTek, Tensor) sit behind it.

- **Build.** `GOOS=android` needs `CGO_ENABLED=1` and the NDK's clang
  (purego's Android `Dlopen` goes through the cgo runtime), which
  `ebitenmobile bind -target android` sets up; `scripts/mobilebuild.sh`
  builds with `ANDROID_NDK_HOME`. On a host without the NDK
  `go vet -tags mediacodec_typecheck ./internal/mediacodec/...` type-checks
  the backend. The Linux backends are excluded from Android builds
  (`android` satisfies the `linux` constraint). The SDK and NDK come from
  Homebrew's `android-commandlinetools` cask, whose `android sdk install`
  (the `android` CLI replaces the deprecated `sdkmanager`) fetches
  `platforms;android-35`, `build-tools;35.0.0` and `ndk;<version>` under
  `ANDROID_HOME`; `ebitenmobile` finds the NDK there by itself. See
  `examples/mobile/README.md`.
- **Minimum version.** Android 14 (API 34) is the documented minimum; the
  NDK symbols used all exist from API 28, and `Probe` lists nothing on
  older systems. The sample app sets `minSdk 34`.
- **Hardware or software.** The NDK cannot enumerate codecs, so the backend
  creates the default codec for the MIME type and reads its name
  (`AMediaCodec_getName`): `c2.android.*`, `OMX.google.*` and names
  containing `.sw.` are the framework's software codecs, which `Probe` leaves
  out and `NewDecoder`/`NewEncoder` refuse unless `WithSoftwareFallback` is
  given (on an emulator everything is software). AV1 in particular falls
  back to the software `c2.android.av1.decoder` on SoCs without an AV1
  engine.
- **Decoding.** MediaCodec needs the picture size before it is configured,
  so the SPS (H.264, HEVC) or sequence header (AV1) is parsed in Go and the
  decoder is configured when it arrives, with the parameter sets as `csd-0`
  and `csd-1` as well as in-band; a size change runs the codec to its end of
  stream and reconfigures it. Output is requested as
  `COLOR_FormatYUV420Flexible` into byte buffers; the plane layout comes from
  the output format's `image-data` (a `MediaImage2`, what `getOutputImage`
  uses in Java) or, without it, from `color-format` 19 or 21 with `stride`
  and `slice-height`, and the crop rectangle from `crop`. Every layout is
  packed into NV12 in Go (interleaved CbCr rows are copied, planar or
  swapped ones interleaved); a layout the backend does not understand is
  reported as `ErrUnsupported` quoting the format. MediaCodec orders its
  output by the timestamps it is given (the emulator's decoders returned a
  B-frame stream in decode order when the packets carried decode-order
  timestamps, as raw Annex-B streams do), so H.264 and HEVC frames go
  through the Go reorder layer as on macOS: display order by picture order
  count whatever the timestamps, and `WithDecodeOrder` returns MediaCodec's
  own order. AV1 temporal units come in presentation order and are passed
  through.
- **Encoding.** The encoder is configured with `bitrate` and
  `bitrate-mode` (VBR, CBR, or CQ with `quality` for `WithQuality`),
  `frame-rate`, `i-frame-interval` (a hint; keyframes are forced from Go at
  the exact interval with `request-sync`), `profile` and `level` for
  `WithProfile`, `latency`/`low-latency` for low-latency mode, `max-bframes`
  1 or 0, and the BT.709 colour keys for RGB input (not
  `prepend-sps-pps-to-idr-frames`: an encoder without that control, such
  as the framework's software H.264 encoder, fails `configure` with
  `BAD_VALUE`). Input is asked for as
  NV12 (`COLOR_FormatYUV420SemiPlanar`) and, when the encoder refuses that,
  I420 (planar), with the layout from the input format; the caller's NV12
  frame is copied (and de-interleaved for I420) into the input buffer. The
  parameter sets arrive in a codec-config buffer and are prepended to every
  keyframe that lacks them. With B-frames the DTS of a packet is the PTS of
  the frame given to the encoder at the same position minus one frame.
- **Status.** Verified on the Android 14 (API 34, arm64) emulator with
  `scripts/android_device_check.sh`, which pushes the `hwmediacodec` CLI
  over adb: `Probe` lists the emulator's `c2.goldfish` H.264 and HEVC
  decoders (they decode on the host and go by a hardware name) and refuses
  the software AV1 decoder and encoders without `-software`; decoding a
  30-frame B-frame H.264 stream, an HEVC stream and an SVT-AV1 IVF stream
  gives frames identical to ffmpeg's, in display order; H.264 and HEVC
  encoding with the framework's software encoders produce streams ffmpeg
  decodes (about 47 and 56 dB luma PSNR); H.264 to HEVC transcoding works.
  The emulator run found the MediaImage2 type value, the
  `prepend-sps-pps-to-idr-frames` rejection and the timestamp ordering,
  all fixed. The same script runs against a device
  (`ARCH=arm64`, without `SOFTWARE=1`), which is the step still open; the
  module, its test binaries and the CLI compile for `android/arm64` and
  `android/amd64` with NDK r27. `examples/mobile/android` is the app to
  run on a device; the author's Android tablet is the first target.

### Browser notes

The browser backend (`internal/webcodecs`) drives the
[WebCodecs](https://www.w3.org/TR/webcodecs/) API through `syscall/js` in a
`GOOS=js GOARCH=wasm` build, the one Ebitengine makes for the web:
`VideoDecoder` fed with Annex-B access units (or AV1 temporal units) as
`EncodedVideoChunk`s without a description, `VideoEncoder` configured for
Annex-B output, and frames in CPU memory through `VideoFrame.copyTo`. It is
the one codec API a page has; the browser puts VideoToolbox, Media
Foundation, VA-API or MediaCodec behind it.

- **Build.** `GOOS=js GOARCH=wasm go build` with the standard Go
  toolchain; nothing else is needed, and purego is not part of the build.
  TinyGo and GopherJS are untested, and the WASI targets have no way to
  the browser's API.
- **Where it runs.** WebCodecs needs a secure context (https, or
  `localhost` during development) and exists in windows and dedicated
  workers. Elsewhere (Node.js, an `http://` page) `Probe` returns an
  empty list and `NewDecoder` / `NewEncoder` report `ErrUnsupported`.
- **Hardware is the browser's word.** Configurations ask for
  `hardwareAcceleration: "prefer-hardware"`. Chrome treats that as a
  requirement: with accelerated video decoding switched off it reports
  nothing as supported, `Probe` is empty and the hardware tests skip
  (measured). The specification calls it a hint, and Firefox and Safari
  take it as one: they answer the same for every preference (Firefox says
  yes to "hardware" VP8, Safari to "software" HEVC), so
  `Capability.Hardware` means something in Chromium browsers only. `WithSoftwareFallback` asks for
  `"no-preference"` and lets the browser's software codecs in (Chrome has
  them for H.264 and AV1 decoding and H.264 encoding, not for HEVC).
- **One thread.** WebCodecs answers in callbacks on the page's event loop,
  and a WebAssembly program shares that thread: a callback runs only while
  every goroutine is blocked. `Send` blocks while the browser's codec is
  saturated and `Flush` until everything is delivered; `Receive` never
  waits and reports `ErrAgain` when nothing has arrived. The documented
  Send/Receive loop, `DecodeReader`, `EncodeWriter`, `playback` and
  `ebitenvideo` all block often enough. A loop that only polls `Receive`
  until a frame shows up never lets the browser deliver it.
- **Order.** WebCodecs decoders emit presentation order (Chrome does, from
  the picture order counts). The library still sorts H.264 and HEVC frames
  by picture order count itself, so the result does not depend on the
  engine or on the timestamps of the packets; `WithDecodeOrder` returns the
  browser's order.
- **RGBA and BGRA output** is converted by the browser
  (`copyTo({format})`), which is far cheaper than converting in
  WebAssembly. For a stream that names no colour matrix the library puts
  its own rule (BT.709 above 704x576, BT.601 otherwise) into the decoder
  configuration, because browsers guess differently per codec (Chrome
  showed an untagged 320x240 AV1 stream as BT.709). Chrome follows that
  configuration for AV1 and ignores it for H.264, where its own guess
  (BT.601 below 720 lines) agrees with the rule at the usual sizes. A
  browser whose `copyTo` does not convert gets the Go conversion instead.
- **10-bit and other pictures without an NV12 form** (4:2:2, 4:4:4) decode
  to RGBA or BGRA only; with NV12 output the stream is refused with
  `ErrUnsupported`. Frames the browser exposes without a pixel format
  (`VideoFrame.format` is `null`: 10-bit hardware frames in Chrome, and
  what some Android hardware decoders return) are read back through an
  `OffscreenCanvas` for RGB output.
- **Encoder.** NV12 frames are wrapped in `VideoFrame`s; bitrate with
  `"variable"` or `"constant"` mode, `WithQuality` through the
  `"quantizer"` mode with one quantiser per frame, `WithLowLatency` as
  `latencyMode: "realtime"`, keyframes forced from Go at the exact frame
  count. The codec string carries the profile and a level that fits the
  picture size and frame rate; without `WithProfile`, H.264 tries High,
  Main and Constrained Baseline in that order. WebCodecs encoders do not
  reorder frames, so `WithBFrames` changes nothing. A browser that
  ignores the request for Annex-B output and returns length-prefixed NAL
  units with an `avcC` / `hvcC` record gets them converted (that path has
  unit tests but no browser that takes it was run).
- **Firefox** (146 and 157 on macOS, measured 2026-10-13) differs from
  Chrome in what it gives the page:
  - Its decoders hand over `BGRX` frames, not YUV. RGBA and BGRA output
    work; NV12 output is `ErrUnsupported` at `Receive`, because turning RGB
    back into YUV would need the matrix Firefox converted with. A
    transcoder there decodes to RGBA and encodes from RGBA.
  - It ignores the colour description in the decoder configuration for
    H.264: an untagged 320x240 stream comes out as BT.709 where every
    other backend shows BT.601. Tagged streams and HD are not affected by
    the rule.
  - Its encoder does not write the colour description into the stream, so
    RGB input (converted with BT.709) is untagged; decoders then assume
    BT.601 for pictures up to 704x576, and those come out in slightly wrong
    colours. HD is assumed BT.709 and is fine.
  - `WithQuality` is accepted and has no effect (the stream has the same
    size for every value), and `latencyMode: "realtime"` is refused at
    configure although `isConfigSupported` accepts it; the backend
    notices (it waits for the configuration to take) and configures
    without it.
  - No HEVC through WebCodecs by default, although its `<video>` element
    plays HEVC: `VideoDecoder` takes it only with the preference
    `dom.media.webcodecs.h265.enabled` switched on in `about:config`
    (measured in 157: with it, HEVC decodes, again to `BGRX`). There is no
    HEVC encoder either way. The samples therefore bundle their clip in
    H.264 as well and fall back to it.
- **Safari** (macOS, measured 2026-10-13) works for playback and differs
  in these ways:
  - Decoded H.264 and HEVC frames are NV12 in a form of WebKit's own,
    whatever the stream says: widened to full range, and the chroma plane
    moved up a quarter line (every row is three quarters of itself and a
    quarter of the row below; measured against ffmpeg sample by sample).
    The backend undoes both, so a frame has the stream's range and chroma
    position again: the luma exactly, the chroma to within a step or two
    (54 dB against ffmpeg on the test pattern, 76 dB and more for luma,
    where the only differences are samples outside the video range that
    the widening clipped). That is not bit-exact, so in Safari the
    checksum tests give way to a test that compares the frames as Safari
    hands them over with a fixture in that form, made from ffmpeg's decode
    of the same B-frame streams: every sample of every frame is within one
    step, in display order. Its `copyTo` does not convert YUV frames to
    RGB (the option is ignored, which the backend detects), so RGBA and
    BGRA output are converted in Go from the restored frames (41 dB
    against ffmpeg's).
  - Its H.264 encoder stops returning chunks from the fifth frame on in
    `latencyMode: "quality"`, flush included. The first encoder a page
    opens therefore proves its latency mode with eight frames, and the
    backend uses `"realtime"` where `"quality"` does not work (and the
    other way round, which covers Firefox); the result is remembered for
    the page. In a WebKit browser, recognised by its user agent string,
    H.264 starts with `"realtime"` so that the proof does not have to wait
    three seconds for the mode that stalls; the proof still decides. In that mode constant bitrate undershoots (592 kbit/s for a
    1 Mbit/s target at 320x240) and `WithQuality` is `ErrUnsupported`.
  - NV12 input without a colour space is taken for full-range samples, so
    every input frame now says video range. Its HEVC encoder still writes
    a full-range stream (it converts the samples itself); the picture is
    right, the range is not the one asked for.
  - Like Firefox it writes no colour description, with the same effect on
    RGB input at SD sizes.
  - AV1 decodes there, bit-exact with ffmpeg, when Safari offers it, but
    Safari does not offer it to every page: whether `isConfigSupported`
    says yes for AV1 is settled per page process and came out as no in
    about half of the page loads on the same machine, before the page had
    used any codec. The library reports what the browser says at that
    moment, so `Probe` may or may not list `av1 decode` in Safari. 10-bit
    AV1 has no output there (no NV12 form, and no RGB conversion in
    `copyTo`).
  - It suspends the timers of a tab that is not visible, and with them
    every timeout of the Go program: run the tests with the Safari window
    in view (`HWMEDIACODEC_BROWSER=open HWMEDIACODEC_BROWSER_ARGS="-a
    Safari"`).
- **Frames that stay on the GPU.** `WithOutputFormat(hwmediacodec.Native)`
  returns the decoder's own pictures instead of pixels: the frame has no
  `Planes`, and `Frame.Native()` is the WebCodecs `VideoFrame` (a
  `syscall/js` value) that WebGL (`texImage2D`), WebGPU and canvases take
  as an image source without the pixels leaving the GPU. `Release` closes
  it, and since a hardware decoder has few pictures, a frame should not
  be held for long (the decoder keeps four at most for `Receive`). The
  order is the display order as for every other format. How such a frame
  turns into colours is then the browser's business: for a stream that
  names no matrix Chrome takes BT.601 at SD sizes while Firefox and Safari
  take BT.709 (measured on 320x240 streams), where the pixel formats
  follow the library's one rule. The Ebitengine player does not use
  these frames; see [Ebitengine](#ebitengine) for why.
- **Audio** goes through `AudioDecoder` and `AudioEncoder`, with the
  same API and the same timestamps as on the other platforms. WebCodecs
  leaves a few things unsaid that the backend settles itself:
  - A decoder stamps its output on its own (the first chunk's timestamp
    plus the samples so far), so frames are matched to packets by order.
    Every packet still yields one frame with its PTS and all its samples.
    An Opus decoder that is given the identification header drops the
    pre-skip itself, so the header is kept from it.
  - An encoder's chunks are stamped from 0, with no word about the
    samples the encoder starts early by. `NewAudioEncoder` therefore runs
    a trial the first time a configuration is asked for: a short signal is
    encoded, an Opus encoder's delay is read from the identification
    header it reports, and an AAC encoder's is measured by decoding the
    trial in the same page and looking for the signal (2112 samples in
    Chrome and Safari on macOS, the figure of AudioToolbox). The trial
    takes some tens of milliseconds and its result is kept for the page.
  - The trial also shows whether the encoder works: Chrome's AAC encoder
    on macOS says yes to 64 kbit/s stereo at 48 kHz in `isConfigSupported`
    and then fails on the first packets, which the backend reports as
    `ErrUnsupported` from `NewAudioEncoder` (80 kbit/s and above work).
  - A bitrate the browser's encoder does not offer is not refused: Chrome
    encodes at its default instead (32 kbit/s asked, 138 kbit/s made), and
    nothing in the API tells. Chrome's AAC encoder gives the same packets
    for VBR and CBR.
  - **Safari** (27 on macOS): its AAC encoder ignores the bitrate in its
    variable mode, so a bitrate is passed with the constant mode there,
    which holds it on average. It replaces the first 1024 samples of
    every signal with silence; the trial notices, and that much silence
    is sent ahead of the signal (the delay it reports is 3136, not 2112).
    It hands out the stream description as a whole ES_Descriptor, from
    which the AudioSpecificConfig is taken. Its decoder returns AAC with
    more than two channels in the order of the bitstream (centre first),
    which is put into the order of a WAV file. Its Opus encoder runs in
    the mode for telephony, as AudioToolbox's does by default, and a page
    cannot change that: Opus encoded in Safari is good for speech and
    loses low frequencies of music (17 dB against the input where the
    other browsers reach 38 dB on the test signal).
  - **Firefox** (157 on macOS) has no AAC encoder and refuses HE-AAC and
    4.0 AAC streams after saying yes to their configuration (an
    `ErrInvalidData` from a later call). Opus in both directions and
    AAC-LC decoding in mono, stereo, 3.0, 5.0 and 5.1 pass.
  - HE-AAC in Chrome comes out 962 samples (20 ms) later than on macOS
    and in Safari: the decoder Chrome uses leaves the delay of the band
    replication in its output and AudioToolbox takes it out.
  - Sample rates and channel counts the browser accepts for encoding
    beyond the library's own rules (Opus at 48 kHz, mono or stereo) are
    the browser's business; an odd rate may be resampled without notice.
- **Not done.** No AV1 encoding, as on every backend.

**Verification (2026-10-12).** `scripts/browser_test.sh` builds a `go
test -exec` wrapper (`internal/browserexec`) that serves the js/wasm test
binary to a headless browser and lends it the host's ffmpeg and temporary
files over a loopback port, so the same conformance tests as on the other
platforms run against WebCodecs. In Chrome on an Apple M3 (hardware H.264,
HEVC and AV1 decoders, H.264 and HEVC encoders behind it) the whole root
suite passes: H.264, HEVC and AV1 frames bit-exact with ffmpeg, B-frame
streams in display order, RGBA and BGRA output at 86 dB or more against
ffmpeg's conversion (8x8-block PSNR), a 10-bit AV1 stream to BGRA, H.264
and HEVC encoding
with the bitrate modes, constant quality, keyframe interval and forced
keyframes, RGB input, and the packet and frame pipelines. The browser's
software decoders (planar frames packed to NV12 in Go) are bit-exact with
ffmpeg for H.264 and AV1, the software H.264 encoder's output decodes
with ffmpeg, and a decoder that the browser closed after a broken keyframe
decodes the next stream. `examples/player` plays the bundled 720x1280 HEVC
clip in the browser at 60 frames per second without a skipped frame, with
seeking. In Firefox 146 on the same Mac (headless and with a window, the
same results) the suite runs with the limits listed above: H.264 and AV1
decode to RGBA and BGRA (AV1 at 41 dB against ffmpeg, a 10-bit AV1 stream
included), H.264 encoding with the bitrate modes, keyframe controls,
profiles and low-latency fallback (45 dB), and the software H.264 encoder
pass; every NV12 decode test skips, and four tests fail for the colour
and quality reasons above. In Safari the same script decodes H.264 and
HEVC to within one step of the fixture in WebKit's frame form (see
above; the checksum tests skip there), passes the RGBA / BGRA, flush,
keyframe, profile, bitrate and low-latency tests and encodes H.264 at
48.7 dB; the AV1
decode tests pass bit-exact in a page where Safari offers AV1 and skip
where it does not. Chrome on Windows,
Linux and Android has not been run.

**Audio (2026-10-13).** The audio tests of the root package run through
the same script: Chrome 153, Firefox 157 and Safari 27 on an Apple M3.
In Chrome all of them pass: AAC (ADTS and MP4, mono to 5.1) and Opus
(10, 20 and 60 ms packets) decode to ffmpeg's samples in place (75 dB),
HE-AAC and HE-AAC v2 decode at the full rate, AAC and Opus encoded in the
page decode with ffmpeg to the input in place (47 dB and 39 dB), from an
ADTS file and from an MP4 file with the delay in its edit list, and the
bitrate, flush, error and garbage tests pass; the 64 kbit/s stereo AAC
cases skip as unsupported. In Safari the same tests pass with the workarounds
above (AAC round trips at 51 to 56 dB once the lost first samples are
compensated, Opus at 17 dB in its telephony mode). In Firefox the Opus
tests and the AAC decoding tests pass and the tests that need an AAC
encoder, HE-AAC or 4.0 skip.

### AV1 on Linux and Windows

A `Packet` is one temporal unit in the low-overhead OBU format, with or
without temporal delimiter OBUs, as on macOS. Every unit yields exactly one
frame, in presentation order and with the packet's PTS, so the reorder layer
is not involved and `WithDecodeOrder` has no effect. Decoding starts, and
resumes after `Flush`, at a unit with a shown key frame; the sequence header
is kept in Go and handed to the decoder again when such a unit does not
carry one. These backends decode the Main profile at 8 bits to NV12: 10-bit
streams, the High and Professional profiles and monochrome streams report
`ErrUnsupported` (10-bit AV1 decodes on macOS only for now).

- **VA-API** (AMD from the RX 6000 series, Intel from Tiger Lake): VA-API
  takes one frame at a time, described by a picture parameter buffer and one
  entry per tile, so `internal/av1` parses the whole frame header and
  maintains what later headers depend on: the eight reference slots with
  their frame sizes, order hints, loop filter deltas, segmentation data,
  global motion parameters and film grain parameters, including the derived
  values (the references of `frame_refs_short_signaling`, skip mode, warp
  validity, lossless mode). Tiles may be spread over several tile groups.
  With film grain the driver is given two surfaces, the reference picture
  and the picture to show; `show_existing_frame` shows the latter again.
  Super-resolution and reference scaling pass through, and frames smaller
  than the sequence maximum come back at their own size. Streams with
  several spatial layers and large-scale-tile streams report
  `ErrUnsupported`.
- **NVDEC** (Linux and Windows): the driver's parser reads the stream.
  Frames are picked up in its display callback instead of the decode
  callback the other codecs use, because AV1 decodes frames that are shown
  later or never; film grain is applied by the driver.
- **Intel VPL**: the runtime reads the stream and applies film grain. A
  temporal unit that holds a hidden frame in front of the shown one is fed
  until the runtime has taken all of it.
- **Media Foundation**: the Microsoft AV1 decoder from the free "AV1 Video
  Extension" Store package, on a GPU whose driver exposes the AV1 DXVA
  profile. Its input type needs the frame size, so the transform is
  configured when the first sequence header arrives; each temporal unit is
  one sample, without its temporal delimiters.

None of this has run on a GPU yet. The VA-API path is verified up to the
driver (see Testing): for 24 stream configurations every buffer it submits
equals, byte for byte, the one ffmpeg's VA-API decoder submits for the same
frame. The NVDEC, VPL and Media Foundation paths leave the stream to the
driver's own parser and add little to their H.264/HEVC code, but what they
add (the display callback, feeding a unit in several calls, the deferred
input type) is untested until `go test ./...` runs on such a machine.

### Windows (Media Foundation)

Decoding drives the synchronous decoder MFTs that ship with Windows
(`Msmpeg2vdec.dll` for H.264, the "HEVC Video Extensions" package for HEVC,
the "AV1 Video Extension" package for AV1)
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

- Requires Windows 10 or later, a GPU driver with DXVA support, for
  HEVC the free "HEVC Video Extensions from Device Manufacturer" (or the
  paid "HEVC Video Extensions") Store package, and for AV1 the free "AV1
  Video Extension" package.
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

## Readers, writers and pipelines

The Send/Receive loops above are written once in the root package, around
four small interfaces that the other packages implement:

| Interface | Role | Implemented by |
| --- | --- | --- |
| `PacketReader`, `PacketSeeker` | coded pictures in decode order, with a time scale; seeking by keyframe | `mp4.PacketSource`, your own reader over `annexb.Reader` or `ivf.Reader`; consumed by `DecodeReader` and `playback` |
| `PacketWriter`, `PacketWriteCloser` | where encoded packets go | `mp4.VideoWriter`, `mp4.VideoFile`, `mp4.Segmenter`, `webrtc.Broadcaster`, `PacketWriterFunc` |
| `FrameReader` | raw frames out | `DecodeReader` (a `Decoder` fed from a `PacketReader`) |
| `FrameWriter` | raw frames in | `EncodeWriter` (an `Encoder` drained into a `PacketWriter`) |

A transcoder is the four of them in a row:

```go
src := video.PacketSource()                                   // *mp4.VideoTrack
dec, _ := hwmediacodec.NewDecoder(ctx, src.Codec(), hwmediacodec.WithTimeScale(src.TimeScale()))
enc, _ := hwmediacodec.NewEncoder(ctx, hwmediacodec.HEVC, w, h,
	hwmediacodec.WithTimeScale(src.TimeScale()), hwmediacodec.WithBitrate(6_000_000))
out := hwmediacodec.NewEncodeWriter(enc, track)               // track: *mp4.VideoWriter

n, err := hwmediacodec.CopyFrames(ctx, out, hwmediacodec.NewDecodeReader(dec, src))
err = out.Flush(ctx)                                          // the frames the encoder still holds
```

- `DecodeReader.ReadFrame` returns one frame at a time (release it when
  done) and `io.EOF` at the end; after `PacketSeeker.SeekKeyframe`, call
  `Reset` to drop what the decoder still holds.
- `EncodeWriter.WriteFrame` encodes a frame and writes the packets that
  became ready; `Flush` ends the stream.
- `CopyFrames` moves frames between any `FrameReader` and `FrameWriter`,
  so a filter (a scaler, an overlay) is a type that implements one of them
  around the other; `examples/convert` opens its encoder that way, for the
  size of the first decoded picture.
- `HasHardware(ctx, codec, direction)` tells in advance whether `NewDecoder`
  or `NewEncoder` can succeed without the software fallback.
- `Frame.RGBAImage()` copies an RGBA or BGRA frame into an `*image.RGBA`,
  and `RGBAFrame(img, pts)` wraps an image as encoder input without
  copying.
- `annexb.FromLengthPrefixed`, `AppendLengthPrefixed` and `AppendUnit`
  convert between Annex-B and the length-prefixed samples of MP4 and HEIF
  (AVCC, HVCC).

## Audio

`NewAudioDecoder` and `NewAudioEncoder` are the audio counterparts of the
video constructors, with the same Send/Receive/Flush loop. Packets are the
same `Packet` type; samples travel in an `AudioFrame`, which is plain Go
memory (interleaved 16-bit or float samples in `Data`) and needs no
`Release`.

```go
// Decode the audio track of a file.
d, _ := mp4.Open("movie.mp4")
a := d.Audio()                                  // AAC or Opus; nil when there is none
dec, _ := hwmediacodec.NewAudioDecoder(ctx, a.Format())
src := a.PacketSource()
for {
	p, err := src.ReadPacket()
	if err == io.EOF { break }
	dec.Send(ctx, p)
	f, _ := dec.Receive(ctx)                    // one frame per packet
	play(f.Int16s(), f.SampleRate, f.Channels)  // interleaved samples
}

// Encode PCM and write it next to a video track.
enc, _ := hwmediacodec.NewAudioEncoder(ctx, hwmediacodec.AAC, 48000, 2,
	hwmediacodec.WithBitrate(128_000))
m, _ := mp4.Create("out.mp4")
aw, _ := m.AddAudioTrack(enc.Format(), 48000)   // time scale: the sample rate
enc.Send(ctx, hwmediacodec.S16Frame(samples, 48000, 2, 0))
for {
	p, err := enc.Receive(ctx)
	if errors.Is(err, hwmediacodec.ErrAgain) { break }
	aw.WritePacket(p)
}
enc.Flush(ctx)                                  // then Receive until io.EOF
m.Close()
```

- **`AudioFormat`** describes a compressed stream: codec, sample rate,
  channels, the codec configuration (`Config`: the AudioSpecificConfig of
  AAC, the `OpusHead` of Opus) and the encoder's delay. Containers report
  it (`mp4.AudioTrack.Format`, `adts.Reader.Format`), an encoder reports
  the one of its output (`AudioEncoder.Format`), and decoders and muxers
  take it. For a stream from elsewhere it is enough to fill in the codec,
  the sample rate and the channel count; an AAC decoder then assumes
  AAC-LC.
- **Packets** are raw: one AAC access unit without ADTS header, one Opus
  packet. `encoding/adts` adds and removes the ADTS header for `.aac`
  files.
- **Time.** An audio decoder hands each packet's PTS on to its frame.
  An encoder counts PTS and `Packet.Duration` in samples (the time scale
  defaults to the sample rate; `WithTimeScale` changes it), takes the PTS
  of the first frame as the start and treats everything sent after it as
  one continuous signal, in frames of any length.
- **Delay.** Encoders start early: the first packets of a stream code
  samples that are not part of the recording (2112 for AudioToolbox's AAC,
  312 for its Opus; `AudioFormat.Delay`), and the last packet is padded to
  full length. Timestamps account for this: a stream whose first sample
  is at 0 starts with a packet at PTS `-Delay`, and the last packet's
  `Duration` ends at the last real sample. The decoder returns every
  sample a packet codes, so a player drops what lies before the start of
  the presentation and after its end. The MP4 muxer records both in the
  track's edit list and sample table; ffmpeg then plays the samples that
  went in from the first one on (for AAC to the last one exactly; for Opus
  it also plays the padding of the last packet, as it does with files of
  its own).
- **Flush** resets a decoder (after a seek) and ends a signal on an
  encoder; both can be used again afterwards. After a jump a decoder
  needs a moment to run in: `mp4.AudioPacketSource.SeekKeyframe`
  positions itself one packet (AAC) or 80 ms (Opus) before the target for
  that reason, and the samples up to the target are to be dropped.
- **Channels** come out in the order of a WAV file (left, right, centre,
  LFE, surrounds), up to 5.1. Encoding takes mono and stereo.
- **Sample rate.** The decoder returns the stream's rate (48 kHz for Opus,
  whatever the recording had) and the encoder takes what it is given
  (Opus: 48 kHz only). The library does not resample.
- **No device.** The library neither plays nor records sound: it stops at
  PCM, like the video side stops at pixels.

What the backend does on macOS and iOS:

- AudioToolbox's Opus encoder starts in its telephony mode (the Opus
  "VoIP" application), which filters the low end. The backend switches it
  to the mode for general audio: the test signal (two tones and a sweep
  from 200 Hz up) comes back with a signal-to-noise ratio of 17 dB in the
  default mode and of 36 dB in this one.
- HE-AAC is decoded with its extensions when the AudioSpecificConfig
  signals them (as the files of `afconvert` and of most encoders do). A
  stream that leaves them out (implicit signalling, as ADTS files do) is
  decoded as the AAC-LC core at half the sample rate.
- Only an edit list gives `AudioFormat.Delay` for a track. Files that
  record the delay in iTunes metadata alone (`iTunSMPB`, as `afconvert`
  writes) report 0.

In a browser the same API sits on `AudioDecoder` and `AudioEncoder`; what
differs there (the delay of an encoder has to be measured, each browser
has codecs of its own) is in the [browser notes](#browser-notes).

### AAC and licences

AAC is covered by patents that are licensed per unit to whoever ships an
encoder or decoder. This library ships neither: it contains no AAC code
(or Opus code) and bundles no codec library. It calls the codec that is
part of the operating system, whose vendor is the licensee, in the same
way it calls the system's H.264 and HEVC engines; that the codec runs on
the CPU rather than in hardware makes no difference to that. For the same
reason there is no pure Go fallback for AAC and there will not be one.
Opus is royalty-free and the safe choice where a codec has to be picked.
On Linux, where no vendor ships a codec with the system, an AAC backend
will only ever use a library the user has installed. This is how the
situation is commonly understood, not legal advice; a product that ships
commercially should have it checked.

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

`d.Audio()` is the audio track for decoding (AAC in `mp4a`, Opus) and
`m.AddAudioTrack(enc.Format(), timeScale)` the track for an audio
encoder's packets; see [Audio](#audio).

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
- An audio track's edit list starts at the encoder's delay, so players
  begin at the first real sample, and the last sample is as short as the
  signal's end makes it. The tracks written from packets share a clock:
  when one starts later than the other (sound that begins half a second
  into the picture), it gets an empty edit of that length in front.
  Tracks copied with `AddPassthroughTrack` keep the edit list of their
  source, rescaled to the output's movie time scale (QuickTime files
  count theirs in 600ths of a second, ffmpeg's in the audio sample rate;
  `Demuxer.MovieTimeScale` reports the source's).
- Progressive output only (`ftyp`, `mdat`, `moov` at the end), `stco` or
  `co64` as the size requires, one chunk per run of samples of the same
  track. Fragmented input is rejected.
- `VideoTrack.PacketSource()` hands out access units with presentation
  times and seeks by the sync-sample table, which is what
  `playback.New` and `ebitenvideo.NewPlayerFromSource` take; `VideoTrack.ElementaryStream()`
  is the same track as a raw Annex-B stream.
- `CreateVideoFile` is the one-track file for encoder output (a
  `hwmediacodec.PacketWriteCloser`), and `Segmenter` cuts encoder packets into CMAF/fMP4
  segments for live HLS: an init segment, then one `moof`+`mdat` per
  segment, each starting at a keyframe. It needs PTS == DTS, so encode
  without B-frames. A stream whose keyframes stop coming is an error
  (after four segment lengths, or `MaxSegmentBytes` of held samples)
  rather than unbounded memory.
- Files are untrusted input. The sample tables are checked against the
  file and a track with more than `MaxSamples` (8 million, about 37 hours
  at 60 fps) is refused, because the index costs 24 bytes per sample
  whatever the file's size; lower it for a program that opens files from
  anywhere. The reader may start at any offset (an MP4 embedded in another
  file); offsets are taken from there.

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
- The handler answers GET and HEAD by the last path element (`index.m3u8`,
  `init.mp4`, `seg_N.m4s`), so it can be mounted under any prefix; it
  answers 503 until the first segment exists and sends
  `Access-Control-Allow-Origin: *`. Segments are cacheable only for as
  long as the window lists them, so a restarted stream (whose sequence
  numbers start over) is not served stale segments from a browser cache.
- The stream is unauthenticated: anyone who can reach the handler can
  watch. Serve it on a loopback or trusted interface (the samples listen on
  `127.0.0.1` unless told otherwise) or behind the application's own
  authentication.
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
  and a viewer only starts receiving at a keyframe. Requests closer
  together than half a second are merged, so a viewer flooding the stream
  with picture loss indications cannot turn it into keyframes only. Encode
  with `WithLowLatency`, Baseline profile and no B-frames.
- Each viewer is served by a goroutine of its own from a bounded queue, so
  a slow network stalls neither the encoder nor the other viewers; a viewer
  that falls about a second behind is dropped. `MaxViewers` (32 by
  default) caps the connections, counting those still being set up;
  `ServeHTTP` answers 503 beyond it or after `Close`, 400 for an offer it
  cannot answer, and drops a connection whose request is cancelled during
  ICE gathering (`AcceptContext`). The endpoint itself is unauthenticated,
  like `net/hls`: serve it on a trusted interface or behind the
  application's authentication.
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

heif.MaxPixels = 4096 * 4096                   // bounds for every decode, as http.DefaultMaxHeaderBytes
rgba, info, err = heif.DecodeBytesWithLimits(data, heif.Limits{MaxPixels: 8192 * 8192})
rgba, info, err = heif.DecodeWithLimits(file, heif.Limits{MaxTiles: 64}, hwmediacodec.WithSoftwareFallback())
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
  everywhere. `Decode` and `DecodeConfig` given a reader that can seek
  (an `*os.File`) read the headers and then only the items they need; any
  other reader is read into memory up to `MaxInputBytes`.
- Limits: a HEIF file declares its picture and grid sizes itself, and its
  item table may point every grid cell at one coded tile, so a few hundred
  bytes can describe a 16 GiB canvas stitched from 65,000 decodes.
  Decoding refuses a file beyond `MaxPixels` (16384 x 16384), `MaxTiles`
  (4096) or `MaxInputBytes` (256 MiB) with an error wrapping `ErrTooLarge`
  before allocating the canvas or opening a decoder; the defaults are
  libavif's and libheif's. They are package variables because the decoders
  `image.Decode` calls take no options; `DecodeWithLimits` and
  `DecodeBytesWithLimits` take a `Limits` for one call. `DecodeConfig` and
  `DecodeInfo` still report the declared size, as `image/png` does.
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
(`examples/texture`), a HEIC/AVIF converter (`examples/imgconv`) and the
player as an Ebitengine mobile game with an Xcode project for the iPhone
(`examples/mobile`, built with `ebitenmobile bind`). The players default to a bundled clip
(`examples/assets`). See [examples/README.md](examples/README.md).

```sh
cd examples
go run ./convert -codec hevc -bitrate 6M input.mp4 output.mp4
go run ./thumbnails -every 10s -width 320 -o thumbs input.mp4
go run ./record -o capture.mp4 -seconds 10
go run ./hls                  # then open http://127.0.0.1:8080/ (-addr :8080 for the whole network)
go run ./webrtc               # same, about 100 ms of latency
go run ./player               # the bundled clip; space pause, arrows seek
go run ./texture              # 1 flat, 2 box, 3 shader
go run ./imgconv photo.heic photo.png
go run ./imgconv -quality 0.8 picture.png picture.heic
```

## Playback

`github.com/shibukawa/hwmediacodec/playback` plays a stream against a
clock the caller drives. It decodes on a background goroutine, a few frames
ahead, in display order and as RGBA, and returns the frame that is due;
where the picture goes is up to the caller (a texture, a window, an image):

```go
p, err := playback.New(track.PacketSource(), playback.WithLoop()) // *mp4.VideoTrack
p.Play()

// once per tick of the display or game loop:
f, err := p.Advance(dt)          // dt: the time since the previous call
if f != nil {
	show(f.Planes[0], f.Width, f.Height) // RGBA, f.Strides[0] bytes per row
	f.Release()
}
```

- `New` takes any `hwmediacodec.PacketReader`, which carries the
  presentation times; a `PacketSeeker` (a keyframe index, as the MP4
  demuxer's `VideoTrack.PacketSource()`) gives the player `Seek`, `Length`
  and looping.
- `NewStream(r, codec, fps)` plays a raw Annex-B elementary stream.
  Elementary streams carry no timestamps, so the frame rate is a
  parameter; an `io.ReadSeeker` gets seeking too, by scanning the stream
  once for keyframes on the first seek.
- Frames are skipped when decoding or the caller falls behind
  (`Skipped` counts them). `Seek(t)` restarts decoding at the keyframe
  before `t` and drops the frames up to it, so the next picture is the one
  at `t`; `Position` reports `t` meanwhile.
- `WithOutputFormat(NV12)` hands out the decoder's native format instead,
  for renderers that convert in a shader.

## Ebitengine

`github.com/shibukawa/hwmediacodec/ebitenvideo` is a separate Go module in
this repository (its own `go.mod`) that depends on both `hwmediacodec` and
Ebitengine; the core module never imports Ebitengine, so users without a
game engine do not pull it in. It is `playback` driven from the game loop,
with the current frame in an `*ebiten.Image`:

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

`NewPlayer` is `playback.NewStream` and `NewPlayerFromSource` is
`playback.New`; the options and the methods (`Seek`, `Position`, `Length`,
`Skipped`, ...) are those of the playback package. One `Update` stands for
1/TPS seconds, or for the wall-clock time since the previous one when the
TPS is `SyncWithFPS`. A minimal example plays a raw stream in a window
(space pauses, the arrow keys seek); the MP4-capable player with a bundled
clip is `examples/player`:

```sh
cd ebitenvideo && go run ./example -codec h264 -fps 30 ../video.h264
cd examples && go run ./player
```

The same player runs in a browser: Ebitengine builds for `GOOS=js
GOARCH=wasm` and the frames come from WebCodecs (see [Browser
notes](#browser-notes)). [wasmserve](https://github.com/hajimehoshi/wasmserve)
builds and serves it on `http://localhost:8080/`:

```sh
cd examples && go run github.com/hajimehoshi/wasmserve@latest ./player
```

The player takes RGBA frames from the decoder there as everywhere else.
Uploading the browser's `VideoFrame` straight into the texture behind the
player's image was built and measured on an Apple M3: per frame on the
page's one thread it cost about 1.3 ms in Chrome whatever the picture
size, against 0.9 ms (720p), 1.7 ms (1080p) and 9.4 ms (4K) for RGBA
frames and `WritePixels`, and it was the slower way in Firefox. It was
taken out again, because Ebitengine does not hand out its textures and the
upload had to find them from the outside, by watching the WebGL context,
which holds only as long as Ebitengine's internals stay as they are. The
frames themselves are available (`hwmediacodec.Native`, see [Browser
notes](#browser-notes)); what is missing is a way to give Ebitengine an
image source for a texture.

Inside the repository the modules build against each other's working tree
through `go.work`. Consumers get the version named in the `require` line of
`ebitenvideo/go.mod`; tag the core module first, update that line, then tag
`ebitenvideo`.

A command-line tool exercises the same API:

```sh
go run ./cmd/hwmediacodec probe
go run ./cmd/hwmediacodec decode -hash input.h264                       # display order, NV12
go run ./cmd/hwmediacodec decode -format rgba -o out.rgba input.hevc -codec hevc
go run ./cmd/hwmediacodec decode -decode-order -hash input.h264
go run ./cmd/hwmediacodec decode -codec av1 -hash input.ivf                  # AV1: IVF input (needs a GPU with an AV1 decoder)
go run ./cmd/hwmediacodec encode -size 1920x1080 -bitrate 8M -gop 60 -o out.h264 input.nv12
go run ./cmd/hwmediacodec encode -size 1920x1080 -format rgba -o out.h264 input.rgba
go run ./cmd/hwmediacodec transcode -in h264 -codec hevc -bitrate 6M -o out.hevc input.h264
```

### Linux notes

The NVIDIA backend (`internal/nvidia`) loads `libcuda.so.1`, `libnvcuvid.so.1`
and `libnvidia-encode.so.1` from the proprietary driver (470.57 or newer for
NVENC API 11.1). Decoding uses the driver's own parser, so H.264, HEVC and AV1
all work; frames are 8-bit 4:2:0 NV12 (10-bit and 4:4:4 streams report
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
NV12 with even dimensions (RGBA and BGRA are converted in Go first) and supports B-frames (two consecutive, when the
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
ffmpeg and ffprobe. The AV1 OBU splitter and sequence header parser are
checked against `trace_headers` on SVT-AV1 streams (8-bit and 10-bit), and
the `av1C` record against the one ffmpeg writes into an MP4 of the same
stream; the `mediacontainer/ivf` package is checked against ffmpeg's own IVF
files. The AV1
frame header parser is checked the same way on 32 stream configurations
written by SVT-AV1, libaom (through ffmpeg and through `aomenc`) and rav1e,
chosen to reach the branches of the header syntax: tiles and tile groups,
film grain, super-resolution, reference scaling, S-frames and error
resilience with frame numbers, screen content tools, lossless and
segmented coding, quantiser matrices, global motion, 4:4:4, monochrome and
still pictures. Every syntax element ffmpeg prints must match, and so must
the length of every header, with the headers parsed against the reference
state the parser keeps itself. These tests skip the configurations whose
encoder is missing (a plain ffmpeg with libsvtav1 runs three of them; the
container of the fake VA driver below has all the encoders). The AV1 decode
conformance tests need libdav1d as well: every NV12 frame of an 8-bit
stream with hidden frames and `show_existing_frame` must equal dav1d's
output exactly, and a 10-bit stream is compared by PSNR.

The audio codecs are checked against ffmpeg in both directions. Decoding:
AAC streams from ffmpeg's encoder (mono, stereo, 5.1; ADTS and MP4) and
Opus streams from libopus (10, 20 and 60 ms packets) must give the samples
ffmpeg's decoders give, in the same place, within the rounding of two
implementations (the tests ask for 50 dB; AAC measures 57 to 86 dB against
ffmpeg's decoder and Opus 107 dB against libopus), and the part
the edit list presents must be the signal that was encoded. Encoding: what
ffmpeg decodes from the encoder's packets (through an ADTS file and through
an MP4 file) must be the input in place, which checks the reported delay to
the sample, and ffprobe must see the stream parameters, the packet count
and the duration; a second track that starts later must start later in the
file by the same amount. HE-AAC files come from `afconvert`, since ffmpeg
has no free HE-AAC encoder, so that test runs on macOS only
(`scripts/browser_test.sh` lends it to the browser like ffmpeg). The test
signal is a tone per channel plus a sweep that never repeats, so that a
shift by one sample or two swapped channels show up. The header code in
`internal/aac`, `internal/opus` and `encoding/adts` is plain Go and tested
on every platform.

The VA-API backend itself runs without a GPU against a fake VA driver
(`internal/vaapi/testdata/fakedriver`, about 1,600 lines of C).
`scripts/vaapi_fake_driver_test.sh` builds it in a container and runs the
backend through the real libva, for H.264 and HEVC, decoding and encoding,
and for AV1 decoding.
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

For AV1 the same driver serves as a second oracle. It logs the picture
parameters and the tile parameters of every frame, with surfaces named by
the number of the picture they hold, and each test stream is decoded twice
against it: by the backend, and by ffmpeg's own VA-API AV1 decoder (`ffmpeg
-hwaccel vaapi -c:v av1`). The two logs must be identical byte for byte, for
every frame of 22 stream configurations, which covers the values no header
trace shows: reference slot contents, global motion parameters carried from
frame to frame, loop filter and segmentation state inherited from the
primary reference frame, tile offsets, and the two surfaces of a frame with
film grain. No encoder at hand writes `frame_refs_short_signaling`, so two
further streams are rewritten in the test to signal two references and
leave five to the decoder; the references the backend derives must again be
ffmpeg's. The frames that come back are checked as for the other codecs:
each temporal unit must show the right picture (the grained one where film
grain applies) at the frame's own size.
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

The Android backend is checked against an emulator or a device over adb:
`scripts/android_device_check.sh` builds the CLI with the NDK, pushes it
with ffmpeg-made streams, and compares decoded frames with ffmpeg's and
encoded streams with ffprobe and ffmpeg (`SOFTWARE=1` for emulators, whose
codecs are the framework's software ones; `android emulator create` and
`android emulator start --headless` from the command-line tools make one).
The iOS build is checked from the Mac: `scripts/mobilebuild.sh` compiles
the module for the device and the Simulator with cgo enabled, and the
`hwmediacodec` CLI built for the Simulator runs inside it with
`xcrun simctl spawn booted`, which is how the decode, encode and transcode
results under [iOS notes](#ios-notes) were obtained (the Simulator's
decoder is software, so `decode` needs `-software` there). The
session-invalidation recovery that iOS needs is tested on macOS by
invalidating the session by hand.

The browser backend is checked in a real browser, since Node.js (which the
Go toolchain runs js/wasm tests in) has no WebCodecs.
`scripts/browser_test.sh` builds `internal/browserexec` as the
`go_js_wasm_exec` wrapper "go test" looks for: it serves the test binary
and a page that starts it on a loopback port, opens the page in headless
Chrome (Chromium or Edge; `HWMEDIACODEC_BROWSER` names another browser,
`HWMEDIACODEC_BROWSER_HEADED=1` shows the window) and relays the output
and the exit code. Firefox works the same way
(`HWMEDIACODEC_BROWSER=/Applications/Firefox.app/Contents/MacOS/firefox`;
on macOS the wrapper starts it through `open`, since a Firefox started by
the path of its binary cannot start its content processes). A browser has neither processes nor files, so
`internal/testutil` sends its ffmpeg and ffprobe calls and its temporary
files to the wrapper instead (a random path token, 127.0.0.1 only, the
tools named in the wrapper's list, files below its own temporary
directory). That way the root package's conformance tests are the same
ones as on every other platform, with ffmpeg on the host as the oracle.
`./internal/webcodecs` adds the browser-only cases: software codecs and
recovery from a decoder error. Under Node.js the same tests build and skip.

```sh
go test ./...
CGO_ENABLED=0 go test ./...   # exercises the cgo-free callback path
./scripts/crossbuild.sh       # CGO_ENABLED=0 builds for every target
./scripts/mobilebuild.sh      # cgo builds for ios/arm64 (device, Simulator), the Android backend type-check and, with the NDK, android
SOFTWARE=1 ./scripts/android_device_check.sh   # Android: the CLI on the attached emulator or device over adb, against ffmpeg
./scripts/browser_test.sh . ./internal/webcodecs   # browser: the conformance tests in headless Chrome through WebCodecs, against ffmpeg
HWMEDIACODEC_BACKENDS=vaapi go test -count=1 .   # Linux: one backend at a time
./scripts/vaapi_fake_driver_test.sh   # VA-API backend against the fake driver (needs docker)
(cd ebitenvideo && go test ./...)   # separate module: frames reach an *ebiten.Image (hardware)
(cd net/webrtc && go test ./...)    # separate module: a pion viewer receives an ffmpeg-made stream (ffmpeg only)
(cd examples && go test ./...)      # separate module: sample end-to-end tests (ffmpeg, most also hardware)
```

Project knowledge (requirements, decisions, backend notes) lives in
`.knowledge/` and is maintained with the knowledge-memory skill. After
re-running the tests a verification entry names, refresh its file hashes with
`python3 scripts/knowledge_refresh_basis.py . --apply`.
