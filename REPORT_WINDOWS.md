# Windows report

Video backends: **NVDEC / NVENC** (NVIDIA driver, x64 only), tried first,
and **Media Foundation** (Intel, AMD, Qualcomm and NVIDIA as the fallback;
x64 and ARM64). Audio: Media Foundation's software transforms. Media
Foundation is reached through `golang.org/x/sys/windows` and raw COM vtable
calls, NVIDIA through purego; `CGO_ENABLED=0 go build` works. 32-bit
Windows is out of scope; the backends build only for `windows/amd64` and
`windows/arm64`. Back to the [README](README.md).

## Status

| GPU / backend | H.264 dec | H.264 enc | HEVC dec | HEVC enc | AV1 dec | AV1 enc |
| --- | --- | --- | --- | --- | --- | --- |
| NVIDIA / NVDEC, NVENC | ✅ | ✅ | ✅ | ✅ | ⚠️ | |
| Intel / Media Foundation | ✅ | ✅ | ✅ | ⚠️ | ✅ | |
| AMD / Media Foundation | ✅ | ✅ | ✅ | ✅ | ⚠️ | |
| ARM64 (Qualcomm) / Media Foundation | ⚠️ | ⚠️ | ⚠️ | ⚠️ | ⚠️ | |

| Audio | Decode | Encode |
| --- | --- | --- |
| AAC | ✅ | ✅ |
| Opus | ⚠️ where the system has a decoder | (no Opus encoder in Windows) |

- **GeForce RTX 2060 Max-Q** (NVDEC / NVENC, in an ASUS ROG Zephyrus G14
  GA401IV next to AMD Radeon integrated graphics; 2026-10-30 and 31,
  [#33](https://github.com/shibukawa/hwmediacodec/issues/33), [#35](https://github.com/shibukawa/hwmediacodec/issues/35)) with `hwmediacodec check`: `Probe` lists
  hardware H.264 (up to 4096x4096) and HEVC (up to 8192x8192) decode and
  encode; the 720x1280 clip decodes in display order (216 to 428 fps for
  H.264 and 367 to 785 fps for HEVC across the runs) and plays back with
  sound as confirmed by eye and ear; the encode round trips at 352x640
  reach 36.5 dB (H.264) and 36.9 dB (HEVC); and H.264 and HEVC streams
  sent over WebRTC show in Chrome 156 ([#35](https://github.com/shibukawa/hwmediacodec/issues/35)). Edge 156 on the same
  machine shows H.264, and its offer has no H.265 at all, so the HEVC
  stream is refused ([#33](https://github.com/shibukawa/hwmediacodec/issues/33)); Edge needs the paid HEVC Video
  Extensions. AV1 was not run: the RTX 20 series has no AV1 decoder.
- **Intel integrated graphics** (Media Foundation): the player and texture
  samples decode the bundled HEVC clip and play its AAC sound, the webrtc
  sample encodes H.264 through the Quick Sync encoder MFT, and
  `hwmediacodec check` decodes H.264 and AV1 (256 and 355 fps at 720x1280)
  and round-trips H.264 (37.0 dB luma PSNR) and AAC. HEVC encode on Intel
  has not been run.
- **AMD Radeon integrated graphics** (Ryzen 4000, the same G14,
  2026-10-31): H.264 and HEVC decode (647 and 401 fps) and encode through
  `AMDh264Encoder` and `AMDh265Encoder` (round trips at 37.2 dB H.264 and
  34.9 dB HEVC), and AAC decode and encode pass, with the workarounds
  described below. The reports [#33](https://github.com/shibukawa/hwmediacodec/issues/33) and [#35](https://github.com/shibukawa/hwmediacodec/issues/35) still show the HEVC
  decode failure (`0xC00D36E6`) that the first of those workarounds fixes. That GPU has no
  AV1 decoder.
- **Audio**: AAC decode of the clip's track with the player sample and
  `hwmediacodec check`, and the AAC encode round trip (2.01 s back at the
  input level). On the G14 `Probe` lists a Media Foundation Opus decoder,
  but the check does not decode Opus, so it stays unverified.
- Not run yet: discrete AMD GPUs, Media Foundation on an NVIDIA GPU (on a
  laptop with both, it picks the AMD GPU), Windows on ARM.

## NVDEC / NVENC

On Windows x64 the NVIDIA backend (`internal/nvidia`, the same code as on
Linux; see the [Linux report](REPORT_LINUX.md#nvidia-nvdec--nvenc)) is
registered before Media Foundation, so a machine with the NVIDIA driver
decodes through NVDEC and encodes through NVENC directly and gets its
controls: RGBA and BGRA encoder input, B-frames, VBR/CBR, constant QP,
low-latency tuning, and frames in decode order with `WithDecodeOrder`.
Media Foundation remains the backend for Intel and AMD GPUs, and takes over
when the NVIDIA backend reports `ErrUnsupported`.

- The backend loads `nvcuda.dll`, `nvcuvid.dll` and `nvEncodeAPI64.dll` from
  the Windows system directory only (`LOAD_LIBRARY_SEARCH_SYSTEM32`); the
  display driver installs them there. Driver 471.41 or newer is needed for
  NVENC API 11.1. `Probe` reports no `nvidia` capability, rather than an
  error, when a DLL or a GPU is missing.
- On a laptop with an integrated GPU next to an NVIDIA one, the NVIDIA GPU is
  used whenever its driver is present. `HWMEDIACODEC_NVIDIA_DEVICE` picks the
  CUDA device ordinal on machines with several NVIDIA GPUs; there is no
  switch yet to prefer Media Foundation over an installed NVIDIA driver
  other than `HWMEDIACODEC_BACKENDS`.
- Windows on ARM has no NVIDIA driver libraries, so `windows/arm64` uses
  Media Foundation only.
- Encoding uses NVENC's synchronous mode (no completion events), as on Linux.
- The two cuvid structures that contain `unsigned long` fields are 32-bit
  there on Windows; their layout is asserted at compile time against values
  measured with `x86_64-w64-mingw32-gcc` and
  `clang --target=x86_64-pc-windows-msvc`, so `GOOS=windows go build` checks
  it from any host.

## Media Foundation

Decoding drives the synchronous decoder MFTs that ship with Windows
(`Msmpeg2vdec.dll` for H.264, the "HEVC Video Extensions" package for HEVC,
the "AV1 Video Extension" package for AV1) with a Direct3D 11 device
attached through `IMFDXGIDeviceManager`, so the GPU's DXVA engine does the
decoding whatever the vendor. `Probe` reports a decoder only when the GPU
exposes the matching DXVA profile with NV12 output *and* a usable decoder
MFT exists; `NewDecoder` fails with `ErrUnsupported` otherwise instead of
decoding in software. The display order comes from the decoder itself, so
`WithDecodeOrder` has no effect.

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
is given it declares 30 fps. RGBA and BGRA are converted to and from NV12 in
Go; for RGB input the colour attributes (BT.709) are set on the media types,
and an encoder MFT that ignores them leaves the stream untagged.

- Requires Windows 10 or later, a GPU driver with DXVA support, for
  HEVC the free "HEVC Video Extensions from Device Manufacturer" (or the
  paid "HEVC Video Extensions") Store package, and for AV1 the free "AV1
  Video Extension" package.
- With two GPUs (a laptop with integrated graphics and a discrete GPU) the
  backend decodes on the default adapter, the first one DXGI lists, which is
  usually the integrated GPU the panel is wired to, and encodes through the
  first hardware encoder MFT `MFTEnumEx` returns, whichever vendor's that is.
  So on a laptop with an NVIDIA GPU, Media Foundation may run on the other
  GPU while the NVIDIA backend runs on the NVIDIA one. `Capability.Device`
  names the adapter and the transform (for example
  `AMD Radeon(TM) Graphics (AMDh264Encoder)`); `hwmediacodec probe` and
  `hwmediacodec check` print it.
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
- **AV1 decoding**: the Microsoft AV1 decoder from the free "AV1 Video
  Extension" Store package, on a GPU whose driver exposes the AV1 DXVA
  profile (8-bit Main profile). Its input type needs the frame size, so the
  transform is configured when the first sequence header arrives; each
  temporal unit is one sample, without its temporal delimiters. The Media
  Foundation AV1 encoder MFTs of newer GPUs are not wired up.
- **AMD HEVC decoder**: on AMD Radeon integrated graphics (Ryzen 4000) the
  HEVC decoder MFT (`HEVCVideoExtension`) offers an NV12 output type and
  then refuses it with `MF_E_ATTRIBUTENOTFOUND` (`0xC00D36E6`) until the
  input type carries a frame size. The backend then configures the decoder
  again at the first keyframe with the coded size of its SPS (measured
  2026-10-31).
- **AMD HEVC encoder**: the AMD HEVC encoder MFT (`AMDh265Encoder`) on the
  same GPU encodes only the columns up to the previous multiple of 8 and
  leaves the rest black while its SPS declares the full width, and it
  ignores `MF_MT_MINIMUM_DISPLAY_APERTURE`. The backend gives it frames
  padded to a multiple of 8 (the last column repeated) and narrows the
  conformance window of the SPS it writes back to the real width. Its
  H.264 encoder has no such problem.
- The backend was written and cross-compiled on macOS against the Windows
  SDK headers (GUIDs, vtable layouts and structure sizes are checked at
  compile time) before it ran on the machines listed above.

## Audio

The audio codecs are Media Foundation transforms like the video ones, but
plain software, so no Direct3D device is involved: the AAC decoder and
encoder that come with Windows, and an Opus decoder where one is installed
(it is listed by `Probe` only then).

- AAC decoding from mono to 5.1.
- The AAC encoder takes 44.1 and 48 kHz, mono and stereo, and offers four
  bitrates (96, 128, 160 and 192 kbit/s); anything else is
  `ErrUnsupported`. Its delay is measured on the first encoder (one packet
  is assumed if the measurement fails).
- The Media Foundation AAC encoder adds no priming: it codes from the
  first sample, so every decoder rebuilt the first 512 or so samples faded
  in from silence. The backend sends one frame of silence first; it shows
  in `Format().Delay` (1024), which callers drop.
- Windows has no Opus encoder.
- The audio tests need no GPU, so they run on any Windows machine with
  ffmpeg.
