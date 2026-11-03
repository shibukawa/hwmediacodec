# Android report

Backend: **MediaCodec** through the NDK (`libmediandk.so`), for video and
audio (`internal/mediacodec`). Android 14 (API 34) and later, arm64 and
x86_64. Back to the [README](README.md).

## Status

| Codec | Decode | Encode |
| --- | --- | --- |
| H.264 | ✅ | ✅ |
| HEVC | ✅ | ✅ |
| AV1 | ⚠️ emulator only | (not wired up) |
| AAC | ✅ | ✅ |
| Opus | ✅ | ✅ |

- **Redmi Pad SE** (Snapdragon 680 / SM6225, Android 15, 2026-10-16):
  `Probe` lists hardware H.264 and HEVC decode and encode, both B-frame
  streams decode bit-exact with ffmpeg in display order (also to RGBA),
  the hardware encoders give streams ffmpeg decodes at about 47 dB PSNR,
  H.264 to HEVC transcoding works, and the audio part passes as on the
  emulator. AV1 is unsupported there: the SoC has no AV1 engine and the
  only `video/av01` decoder is the software `c2.android.av1-dav1d.decoder`.
  The `examples/mobile` app plays the HEVC clip through the hardware
  decoder at 60 fps without skipped frames, and its check passed for
  H.264, HEVC, AAC and Opus. Xiaomi devices need **Install via USB**
  turned on in the developer options before `adb install` is allowed.
- **Android 14 emulator** (API 34, arm64), with
  `scripts/android_device_check.sh`, which pushes the `hwmediacodec` CLI
  over adb: `Probe` lists the emulator's `c2.goldfish` decoders (they
  decode on the host and go by a hardware name) and refuses the software
  codecs without `-software`; H.264, HEVC and AV1 streams decode to
  ffmpeg's frames in display order, and the framework's software encoders
  produce streams ffmpeg decodes.
- AV1 on a device with an AV1 engine has not been run yet.

The backend drives the NDK MediaCodec C API through purego:
`AMediaCodec_createDecoderByType` and `createEncoderByType` for
`video/avc`, `video/hevc` and `video/av01`, synchronous
`dequeueInputBuffer` / `queueInputBuffer` / `dequeueOutputBuffer` with
short timeouts and no callbacks, and frames in CPU memory through
MediaCodec's byte buffers. It is the one codec API an Android application
has; the vendor's hardware codecs (Qualcomm, Exynos, MediaTek, Tensor) sit
behind it.

## Notes

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
  [`examples/mobile/README.md`](examples/mobile/README.md).
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
  reported as `ErrUnsupported` quoting the format. RGBA and BGRA are
  converted in Go. MediaCodec orders its output by the timestamps it is
  given (the emulator's decoders returned a B-frame stream in decode order
  when the packets carried decode-order timestamps, as raw Annex-B streams
  do), so H.264 and HEVC frames go through the Go reorder layer as on
  macOS: display order by picture order count whatever the timestamps, and
  `WithDecodeOrder` returns MediaCodec's own order. AV1 temporal units come
  in presentation order and are passed through.
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
  MediaCodec has AV1 encoders on newer SoCs; they are not wired up.

## Audio

The codecs are MediaCodec's, as for video (the framework's software AAC
and Opus codecs; nothing is tied to a GPU): AAC-LC to 5.1 and HE-AAC as the
device has it, Opus mono and stereo; encoding AAC-LC (the rates of the AAC
table) and Opus (48 kHz), mono and stereo.

- MediaCodec does not report an encoder's delay, so the first encoder for
  a rate and channel count encodes a short noise signal, decodes it on the
  device and looks for it in the result (2048 samples for AAC and 312 for
  Opus on the emulator).
- The Opus decoder is given an identification header without pre-skip and
  zero codec delay, so that it returns every sample like the other
  backends.
- On the emulator and the Redmi Pad SE the script's audio part passes: AAC
  from an MP4 file and from ADTS and Opus from an MP4 file decode to
  ffmpeg's samples in place (63 and 70 dB), and what the device encodes
  ffmpeg decodes to the input in place.
