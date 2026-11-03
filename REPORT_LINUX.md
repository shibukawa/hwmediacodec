# Linux report

Video backends, in the order they are tried: **NVDEC / NVENC** (NVIDIA
proprietary driver), **Intel VPL** (Quick Sync Video) and **VA-API** (AMD
with Mesa, Intel with iHD / i965). Audio backends: the distribution's
`libopus`, `libavcodec` and `libfdk-aac`, loaded when present. Everything is
loaded through purego; `CGO_ENABLED=0 go build` works. Back to the
[README](README.md).

## Status

| GPU / backend | H.264 dec | H.264 enc | HEVC dec | HEVC enc | AV1 dec | AV1 enc |
| --- | --- | --- | --- | --- | --- | --- |
| AMD / VA-API | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| Intel / VA-API | ⚠️ | ⚠️ | ⚠️ | ⚠️ | ⚠️ | ⚠️ |
| Intel / VPL | ⚠️ | ⚠️ | ⚠️ | ⚠️ | ⚠️ | |
| NVIDIA / NVDEC, NVENC | ⚠️ | ⚠️ | ⚠️ | ⚠️ | ⚠️ | |

| Audio | Decode | Encode |
| --- | --- | --- |
| AAC (`libavcodec`, `libfdk-aac`; on Ubuntu `ubuntu-restricted-extras`) | ✅ | ✅ (`libfdk-aac`) |
| Opus (`libopus`) | ✅ | ✅ |

- **AMD Radeon 890M** (Mesa 26.0.8, Ubuntu 26.04):
  - Automatic checks with v0.5.1 (2026-11-03): every check passes. The
    bundled 720x1280 clip decodes in display order at 830 fps (H.264),
    1101 fps (HEVC) and 1134 fps (AV1); the encode round trips reach
    36.0 dB (H.264), 36.7 dB (HEVC) and 36.5 dB (AV1); AAC (libavcodec,
    fdk-aac) and Opus pass. The CLI's AV1 IVF output decodes in libdav1d
    in CQP, VBR and CBR.
  - Windowed check with v0.5.0 and Chrome 156 ([#38](https://github.com/shibukawa/hwmediacodec/issues/38), [#39](https://github.com/shibukawa/hwmediacodec/issues/39),
    2026-11-02): the clip plays in H.264, HEVC and AV1 with its AAC sound,
    as confirmed by eye and ear, and the H.264 stream over WebRTC shows in
    Chrome. HEVC encoding failed in that release: radeonsi pads HEVC
    pictures to 64x16 (352x640 came out as 384x640), which v0.5.1 handles
    ([#40](https://github.com/shibukawa/hwmediacodec/pull/40)). The HEVC
    WebRTC stream failed for the same reason (640x360 came out as 640x368;
    reproduced with v0.5.0 and passing with v0.5.1 through the CLI), so
    whether Chrome on Linux shows an HEVC stream is still to be seen.
- Intel (VA-API and VPL) and NVIDIA have not been run on hardware yet;
  their code is verified up to the driver (see below).
- Audio: the tests that run on macOS pass in containers of Debian 12 and
  13, Ubuntu 22.04 and 24.04 and Fedora, on arm64 and amd64
  (`scripts/linux_audio_test.sh`). A fresh Ubuntu 26.04 desktop has
  libopus only: AAC is unsupported, and the player plays the clip without
  sound ([#38](https://github.com/shibukawa/hwmediacodec/issues/38)). `sudo apt install ubuntu-restricted-extras` brings
  libavcodec and libfdk-aac, and AAC decoding and encoding pass
  ([#39](https://github.com/shibukawa/hwmediacodec/issues/39)).

`HWMEDIACODEC_BACKENDS=vaapi` bypasses Intel VPL to exercise the VA-API
backend on the same GPU, and `HWMEDIACODEC_BACKENDS=vpl,nvidia` prefers the
integrated Intel GPU on a machine that also has an NVIDIA card (see
[Environment variables](GUIDE.md#environment-variables)).

`hwmediacodec check` opens an X11 window (XWayland on a Wayland desktop);
the command sets `EGL_PLATFORM=x11` for itself, because Mesa's EGL
otherwise follows the `EGL_PLATFORM=wayland` many Wayland sessions set,
takes the X11 display for a Wayland one and crashes as the window opens.

## VA-API

The VA-API backend opens the first DRM render node (`/dev/dri/renderD128`
and up) that libva can initialise. Set `HWMEDIACODEC_VAAPI_DEVICE` to a
render node path to pick a GPU on multi-GPU machines. The user needs read
and write access to the node (usually the `render` or `video` group), and
`libva2`, `libva-drm2` and the GPU's VA driver (`mesa-va-drivers` for AMD,
`intel-media-va-driver` for Intel) must be installed. `Probe` reports no
VA-API capability, rather than an error, when any of these is missing.

VA-API is a slice-level API, so the bitstream parsing, picture order
count, reference picture marking (H.264) or reference picture sets (HEVC)
and reference list construction run in Go (`internal/h264`,
`internal/hevc`), and the driver only accelerates the slice data. Frames
are NV12 in CPU memory; RGBA or BGRA are converted in Go, before encoding
and after decoding (see [RGBA and BGRA](GUIDE.md#rgba-and-bgra)).

- **Decoding**: H.264 and HEVC, 8-bit 4:2:0 only (HEVC Main; Main 10,
  the range extensions and screen content coding are rejected with
  `ErrUnsupported`). H.264 is progressive frames only (interlaced field
  pictures are rejected with `ErrUnsupported`); HEVC field sequences decode
  as one frame per field. An HEVC decoder that starts at a CRA picture, or
  is flushed and resumes at one, drops the RASL pictures that follow it, as
  the specification requires. `Send` returns `ErrAgain` when more than a
  few decoded frames are waiting for `Receive`; drain and resend.
- **Encoding**: H.264 and HEVC (Main profile), I and P frames with one
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
  backend serves those GPUs. The stream is tagged BT.709 for RGB input in
  the SPS the backend writes.
- **radeonsi** (AMD VCN) pads HEVC pictures to a multiple of 64 luma
  samples across and 16 down and writes the application's conformance
  window into its own SPS without adjusting it to that padding; on
  radeonsi the coded size is rounded up to 64x16 so the conformance window
  crops the padding.

### AV1 decoding (VA-API)

AMD from the RX 6000 series, Intel from Tiger Lake. VA-API takes one frame
at a time, described by a picture parameter buffer and one entry per tile,
so `internal/av1` parses the whole frame header and maintains what later
headers depend on: the eight reference slots with their frame sizes, order
hints, loop filter deltas, segmentation data, global motion parameters and
film grain parameters, including the derived values (the references of
`frame_refs_short_signaling`, skip mode, warp validity, lossless mode).
Tiles may be spread over several tile groups. With film grain the driver is
given two surfaces, the reference picture and the picture to show;
`show_existing_frame` shows the latter again. Super-resolution and
reference scaling pass through, and frames smaller than the sequence
maximum come back at their own size. The Main profile is decoded at 8 bits
to NV12: 10-bit streams, the High and Professional profiles and monochrome
streams report `ErrUnsupported`, as do streams with several spatial layers
and large-scale-tile streams.

### AV1 encoding (VA-API)

`NewEncoder` with `AV1` produces 8-bit Main profile temporal units without
temporal delimiters, each key frame preceded by the sequence header (the
form IVF, MP4 and WebM files store). Every frame is shown: key frames, and
inter frames that predict from the frame before (one reference, no
B-frames; `WithBFrames` is accepted and ignored). `WithQuality` maps to a
constant quantiser index (CQP), `WithBitrate` to the driver's VBR or CBR,
and without either a quantiser index of 100 is used. The sequence header
and the frame headers are written in Go and handed over as packed headers,
together with the bit positions of the fields the rate control rewrites,
which is the form both Mesa and Intel's driver take; the stream uses 64x64
superblocks, uniform tiles (a single one up to 4096 samples wide and
4096x2304 in area) and CDEF, and RGBA or BGRA input is tagged BT.709 in the
sequence header's colour config. AMD's VCN 4 (RX 7000 series, Radeon 700M
and 800M) codes the picture at its surface alignment, 64x16, and nothing
smaller than 128x128: other sizes report `ErrUnsupported`, naming the
sizes that work, rather than giving a stream of the wrong size. Intel GPUs
(Arc, Meteor Lake and newer) have not been run.

### Verification without a GPU

The VA-API backend was written on a Mac. Everything up to the driver is
verified without hardware: `scripts/vaapi_fake_driver_test.sh` runs
complete decode and encode runs through the real libva against a checking
test driver (see [CONTRIBUTING](CONTRIBUTING.md)), and for 24 AV1 stream
configurations every buffer the backend submits equals, byte for byte, the
one ffmpeg's VA-API decoder submits for the same frame. On AMD the pixels
are now confirmed by the Radeon 890M run above; on Intel, what the GPU
makes of the buffers is still to be confirmed.

## Intel VPL

The Intel VPL backend (`internal/vpl`) loads the VPL dispatcher
`libvpl.so.2`, which finds the GPU runtime: `libmfx-gen.so.1.2` for Tiger
Lake (Gen12) and newer including Arc, or the legacy Media SDK runtime
`libmfxhw64.so.1` for older GPUs when it is installed. The runtime renders
through VA-API, so `libva2`, `libva-drm2` and the iHD driver
(`intel-media-va-driver`) are needed as well; on Debian and Ubuntu the
packages are `libvpl2` and `libmfx-gen1.2`.

- VPL is a full codec API: the runtime parses the bitstream and returns
  frames in display order (so `WithDecodeOrder` has no effect), and its
  encoders write complete access units, so H.264 and HEVC both work and no
  slice-level bookkeeping runs in Go.
- Decoding yields 8-bit 4:2:0 NV12 (10-bit streams report
  `ErrUnsupported`), copied to CPU memory per picture; field pairs of
  interlaced streams come back as one woven frame. AV1 (8-bit Main
  profile, Tiger Lake and newer): the runtime reads the stream and applies
  film grain; a temporal unit that holds a hidden frame in front of the
  shown one is fed until the runtime has taken all of it.
- Encoding takes NV12 with even dimensions (RGBA and BGRA are converted in
  Go first; the stream is tagged BT.709 through `mfxExtVideoSignalInfo`)
  and supports B-frames (two consecutive, when the GPU has them), VBR/CBR,
  constant QP for `WithQuality` (and QP 26 without a bitrate or quality),
  low latency (no B-frames) and profiles; keyframes are IDR pictures with
  in-band VPS/SPS/PPS, placed by `WithKeyframeInterval` and
  `Frame.ForceKeyframe` only. VPL's AV1 encoder is not wired up.
- The backend opens the first render node that belongs to an Intel device;
  set `HWMEDIACODEC_VPL_DEVICE` to a render node path to choose one. It is
  registered before VA-API, so on an Intel GPU with a VPL runtime it serves
  the requests, and VA-API takes over when the runtime is missing or
  rejects a configuration.
- Like the NVIDIA backend it was written on a Mac: struct layouts are
  verified against the VPL headers with gcc, every symbol binds against
  libvpl 2.8 and the dispatcher accepts the session filter in a container,
  but no run on Intel hardware has happened yet.

## NVIDIA (NVDEC / NVENC)

The NVIDIA backend (`internal/nvidia`) loads `libcuda.so.1`,
`libnvcuvid.so.1` and `libnvidia-encode.so.1` from the proprietary driver
(470.57 or newer for NVENC API 11.1). It is registered before VA-API.

- Decoding uses the driver's own parser, so H.264, HEVC and AV1 (8-bit Main
  profile, RTX 30 series and newer) all work; frames are 8-bit 4:2:0 NV12
  (10-bit and 4:4:4 streams report `ErrUnsupported`), progressive only,
  copied to CPU memory per picture. AV1 frames are picked up in the
  driver's display callback instead of the decode callback the other
  codecs use, because AV1 decodes frames that are shown later or never;
  film grain is applied by the driver.
- Encoding accepts NV12, RGBA and BGRA (RGB is converted by NVENC with
  BT.601 and the stream is tagged so), supports B-frames (two consecutive,
  when the GPU has them), VBR/CBR, constant QP for `WithQuality`,
  low-latency tuning and profiles. Encoding uses NVENC's synchronous mode
  (no completion events). NVENC's AV1 encoder is not wired up.
- Set `HWMEDIACODEC_NVIDIA_DEVICE` to a CUDA device ordinal to pick a GPU.
- It was written on a Mac: struct layouts are verified against the SDK
  headers with clang and every symbol binds against driver 535 in a
  container, but no NVIDIA hardware run has happened on Linux yet; the
  same code passes `hwmediacodec check` on Windows (see the
  [Windows report](REPORT_WINDOWS.md#nvdec--nvenc)).

## Audio

There is no system codec on Linux, so each backend stands for a library of
the distribution, opened at run time and absent without it (`Probe` lists
what is there under the names below):

| Backend | Library (Debian/Ubuntu package, Fedora package) | Serves |
| --- | --- | --- |
| `libopus` | `libopus.so.0` (`libopus0`, `opus`) | Opus decode and encode |
| `libavcodec` | `libavcodec.so.58` to `.62`, FFmpeg 4 to 8 (`libavcodec61` and its siblings, `libavcodec-free`) | AAC decode |
| `fdk-aac` | `libfdk-aac.so.2` or `.so.1` (`libfdk-aac2`, in Debian's non-free section; `fdk-aac-free`) | AAC encode, and AAC decode on a machine without libavcodec |

- Decoding: Opus mono and stereo; AAC-LC to 5.1, HE-AAC and HE-AAC v2.
  Encoding: Opus (48 kHz), AAC-LC, mono and stereo.
- AAC decodes with libavcodec where both libraries are installed. The
  build of the FDK library that Fedora and Ubuntu 24.04 ship
  (`fdk-aac-free`) has AAC-LC only: it refuses an HE-AAC stream with
  `ErrUnsupported` instead of playing its core at half the rate.
- The libavcodec backend only decodes, and only through the part of the
  library that has kept its shape from FFmpeg 4 to 8 (the functions and the
  first fields of three structures). A libavcodec outside that range is not
  loaded.
- Both AAC decoders are set up to return a packet's samples with the
  packet: the FDK decoder without its limiter and its interpolating
  concealment (each of which delays the output), and both with the 962
  samples of the band replication's delay taken out of HE-AAC, as
  AudioToolbox does.
- libfdk-aac has one rate control worth the name here, which holds the
  bitrate on average; `VBR` and `CBR` both select it. Its delay (2048
  samples) is measured on the first encoder.
- `HWMEDIACODEC_AUDIO_BACKENDS` restricts and orders the audio backends:
  `HWMEDIACODEC_AUDIO_BACKENDS=libopus,fdk-aac` decodes AAC with the FDK
  library.
- See [AAC and licences](GUIDE.md#aac-and-licences) for why the AAC
  backends only use libraries that are already installed.
