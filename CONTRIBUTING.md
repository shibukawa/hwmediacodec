# Contributing

Back to the [README](README.md).

## Reporting how the library does on your machine

The most useful contribution is a check report from hardware that has not
been run yet (the ⚠️ cells of the status table in the README):

```sh
go run github.com/shibukawa/hwmediacodec/cmd/hwmediacodec@latest check
```

It decodes the bundled clip in H.264, HEVC and AV1, encodes and decodes
back in every codec the machine has, plays the clip with sound, streams
H.264 and HEVC to the browser over WebRTC, and prints a Markdown report
with a link that opens a
[new issue](https://github.com/shibukawa/hwmediacodec/issues/new) with it
filled in. `-auto` runs the automatic checks only, without a window
(servers, CI, and Android or iOS outside the mobile sample). See
[the command's README](cmd/hwmediacodec/README.md#check) for the details.

For a deeper run, `go test ./...` on the machine (with ffmpeg and ffprobe
installed) compares every backend with ffmpeg; see below.

## Testing

```sh
go test ./...
CGO_ENABLED=0 go test ./...   # exercises the cgo-free callback path
./scripts/crossbuild.sh       # CGO_ENABLED=0 builds for every target
./scripts/mobilebuild.sh      # cgo builds for ios/arm64 (device, Simulator), the Android backend type-check and, with the NDK, android
SOFTWARE=1 ./scripts/android_device_check.sh   # Android: the CLI on the attached emulator or device over adb, against ffmpeg
./scripts/browser_test.sh . ./internal/webcodecs   # browser: the conformance tests in headless Chrome through WebCodecs, against ffmpeg
HWMEDIACODEC_BACKENDS=vaapi go test -count=1 .   # Linux: one backend at a time
./scripts/vaapi_fake_driver_test.sh   # VA-API backend against the fake driver (needs docker)
./scripts/linux_audio_test.sh         # Linux audio backends in containers of five distributions (needs docker)
(cd ebitenvideo && go test ./...)   # separate module: frames reach an *ebiten.Image (hardware)
(cd net/webrtc && go test ./...)    # separate module: a pion viewer receives an ffmpeg-made stream (ffmpeg only)
(cd examples && go test ./...)      # separate module: sample end-to-end tests (ffmpeg, most also hardware)
```

ffmpeg is the test oracle throughout and never a run-time dependency; tests
that need it (or `ffprobe`, `x265`, `aomenc`, `rav1e`, libdav1d, `sips`,
`afconvert`) skip when it is missing. What runs where:

- **Everywhere.** The bitstream code is checked against ffmpeg's own view
  of the stream: the H.264, HEVC and AV1 header parsers element by element
  against `trace_headers` (the AV1 frame header on 32 encoder
  configurations), the decoded-picture-buffer logic against `-debug mmco`
  and `-debug pict` and against the reference lists `x265` reports, the
  reorder logic against the presentation times ffmpeg writes into an MP4,
  the header writers by reading them back with ffmpeg, the VA-API parameter
  buffers built from HEVC streams against the same traces, and the AAC,
  Opus and ADTS header code on its own. The MP4 tests compare sample
  tables, presentation times and frame checksums with ffprobe, and the
  `net/hls` test lets ffprobe play the served playlist.
- **With a hardware engine** (where `Probe` reports one): decoded frames
  must equal ffmpeg's frame for frame in display order (the AV1 tests also
  need libdav1d); RGB output and input are compared with ffmpeg's
  conversion by block PSNR and RGBA must mirror BGRA; encoded streams are
  decoded by ffmpeg and compared with the source by PSNR, and their
  keyframe and B-frame structure is checked with ffprobe; the audio codecs
  are compared with ffmpeg's in both directions (HE-AAC input comes from
  `afconvert`, so that test runs on macOS). The `capture`, `playback`,
  `ebitenvideo` and image tests need the hardware codecs too; `image/heif`
  uses macOS ImageIO (`sips`) as its reference. The conformance tests skip
  when `Probe` reports no hardware engine, and the encoder tests for
  optional controls skip when the backend rejects them (Media Foundation
  vendor MFTs, for example).
- **VA-API without a GPU.** `scripts/vaapi_fake_driver_test.sh` runs the
  backend through the real libva in a container against a fake VA driver
  (`internal/vaapi/testdata/fakedriver`) that decodes nothing but checks
  every buffer it is handed and paints each surface with its picture
  number, so the tests see that the right picture comes out, cropped and in
  display order. It runs as several driver personalities (packed headers as
  Mesa or Intel, with and without HEVC attributes and `vaDeriveImage`), and
  for AV1 it logs the picture and tile parameters so that the backend's
  buffers can be compared byte for byte with the ones ffmpeg's own VA-API
  decoder submits (24 stream configurations, two of them rewritten to use
  `frame_refs_short_signaling`). `PLATFORM=linux/amd64` runs it on that
  architecture through emulation.
- **Mobile.** `scripts/android_device_check.sh` builds the CLI with the
  NDK, pushes it over adb with ffmpeg-made streams and compares the results
  with ffmpeg and ffprobe (`SOFTWARE=1` for emulators; `ARCH=arm64` without
  `SOFTWARE=1` on a device). `scripts/mobilebuild.sh` compiles for the iOS
  device and Simulator; the CLI built for the Simulator runs inside it with
  `xcrun simctl spawn booted`. The session-invalidation recovery iOS needs
  is tested on macOS by invalidating the session by hand.
- **Linux audio.** `scripts/linux_audio_test.sh` cross-compiles the test
  binaries and runs the audio tests in containers of five distributions
  with ffmpeg, libopus and libfdk-aac (every libavcodec from 58 to 62, the
  full and the "free" FDK library), once with every backend, once with
  libfdk-aac as the AAC decoder and once with libavcodec alone.
- **Browser.** Node.js has no WebCodecs, so `scripts/browser_test.sh`
  builds `internal/browserexec` as the `go_js_wasm_exec` wrapper `go test`
  looks for: it serves the test binary and a page to a headless browser
  (Chrome, Chromium or Edge; `HWMEDIACODEC_BROWSER` names another,
  `HWMEDIACODEC_BROWSER_HEADED=1` shows the window) and lends the tests
  the host's ffmpeg and temporary files over a loopback port (a random
  path token, 127.0.0.1 only, a fixed list of tools, files below its own
  directory), so the root package's conformance tests are the same ones
  as everywhere else. `./internal/webcodecs` adds the browser-only cases.
  Safari needs its window in view (`HWMEDIACODEC_BROWSER=open
  HWMEDIACODEC_BROWSER_ARGS="-a Safari"`); see the
  [browser report](REPORT_BROWSER.md#safari).

## Modules and releases

The repository holds five Go modules: the core (`.`), `ebitenvideo`,
`net/webrtc`, `cmd/hwmediacodec` and `examples`. The core module depends
only on purego, `golang.org/x/sys` and mp4ff; Ebitengine and pion are
pulled in by the other modules only.

Inside the repository the modules build against each other's working tree
through `go.work`. Consumers get the versions named in the `require` lines,
so release in dependency order: tag the core module first, update the
`require` lines of `ebitenvideo/go.mod` and `net/webrtc/go.mod` and tag
them, then `cmd/hwmediacodec` (tag `cmd/hwmediacodec/vX.Y.Z`), then
`examples`, which requires `cmd/hwmediacodec` (its sample media, the check
and the fireworks scene live there). The core module shipped the command
itself up to v0.3.2, so `cmd/hwmediacodec/go.mod` must require a later
core release; otherwise `go run .../cmd/hwmediacodec@latest` finds the
package in two modules.

## Project knowledge

Project knowledge (requirements, decisions, backend notes) lives in
`.knowledge/` and is maintained with the knowledge-memory skill. After
re-running the tests a verification entry names, refresh its file hashes with
`python3 scripts/knowledge_refresh_basis.py . --apply`.
