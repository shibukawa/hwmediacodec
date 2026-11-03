# iOS report

iOS 15 and later (iPhone, iPad) runs the [macOS](REPORT_MACOS.md) backends,
VideoToolbox and AudioToolbox: the `darwin` build constraint covers `ios`,
the frameworks are the same four and are opened from the same paths, and
the public API behaves as on macOS. Back to the [README](README.md).

## Status

| Codec | Decode | Encode |
| --- | --- | --- |
| H.264 | ✅ | ✅ |
| HEVC | ✅ | ✅ |
| AV1 | ⚠️ A17 Pro and M-series iPads | (no AV1 encoder) |
| AAC | ✅ | ✅ |
| Opus | ✅ | ✅ |

Verified on an iPhone SE (3rd generation, iPhone14,6, A15, iOS 26.6.2) with
the check mode of `examples/mobile`
([#31](https://github.com/shibukawa/hwmediacodec/issues/31), 2026-10-16):
H.264 and HEVC decode in hardware (525 and 669 fps for the 720x1280 clip),
the encode round trips at 35.5 dB (H.264) and 36.6 dB (HEVC), AAC and Opus
round trips, and playback with sound confirmed by eye and ear. AV1 is
unsupported there (no AV1 decoder before the A17 Pro). Also verified in the
iOS Simulator with the `hwmediacodec` CLI built for `ios/arm64` (probe,
decoding to NV12 and RGBA in display order, H.264 and HEVC encoding,
transcoding).

## Notes

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
- **Sample app.** [`examples/mobile`](examples/mobile) is the player as an
  Ebitengine mobile game with an Xcode project, and runs
  `hwmediacodec check` on the device.
