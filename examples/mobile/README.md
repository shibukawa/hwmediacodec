# mobile: the player on an iPhone or iPad

This directory is the video player as an Ebitengine *mobile* game: a Go
package without a `main`, which `ebitenmobile bind` compiles into a native
library that a normal Xcode project embeds. The game plays the bundled
waterfall clip ([`../assets`](../assets)) in a loop through VideoToolbox
on the device, shows what `hwmediacodec.Probe` found at the top of the
screen, and pauses and resumes on a tap. `ios/` is the Xcode project.

The same Go package will serve the Android project once the Android backend
(NDK MediaCodec) exists; until then an Android build plays nothing, since
`Probe` reports no backend there.

## Build and run on an iPhone

Requirements: Xcode with the iOS platform installed, Go 1.27 or later, and a
device (or the Simulator). Everything runs from the `examples` module:

```sh
cd examples
go run github.com/hajimehoshi/ebiten/v2/cmd/ebitenmobile bind -target ios -iosversion 15.0 \
    -o mobile/ios/Mobile.xcframework github.com/shibukawa/hwmediacodec/examples/mobile
open mobile/ios/HWMediaCodecPlayer.xcodeproj
```

`ebitenmobile bind` builds the Go code for the device and the Simulator
with cgo enabled (purego needs the cgo runtime on iOS; the library itself
has no cgo code) and writes `Mobile.xcframework`, which the project already
references but which is not checked in. In Xcode pick your team under
Signing & Capabilities, choose the iPhone, and run. The app is a single
`MobileEbitenViewController` from the framework
([`ios/HWMediaCodecPlayer/AppDelegate.swift`](ios/HWMediaCodecPlayer/AppDelegate.swift)).

From the command line, for the Simulator:

```sh
xcodebuild -project mobile/ios/HWMediaCodecPlayer.xcodeproj -scheme HWMediaCodecPlayer \
    -destination 'platform=iOS Simulator,name=iPhone 17 Pro' -derivedDataPath mobile/ios/build build
xcrun simctl install booted mobile/ios/build/Build/Products/Debug-iphonesimulator/HWMediaCodecPlayer.app
xcrun simctl launch booted jp.shibu.hwmediacodec.player
```

## What to expect

- On a device the top line reads `ios/arm64  videotoolbox: h264 decode,
  h264 encode, hevc decode, hevc encode` (plus `av1 decode` from the A17
  Pro and M-series iPads on) and the clip decodes in hardware.
- In the Simulator VideoToolbox has no hardware decoder, so the app falls
  back to the OS software decoder and says `(software decoder)`.
- Sending the app to the background and back keeps playing: iOS
  invalidates VideoToolbox sessions of a background application, and the
  library opens a new one at the next keyframe.

The Go side is tested on the Mac (`go test ./mobile` in `examples` opens the
clip the way the first frame does); the iOS build itself is covered by
`scripts/mobilebuild.sh` in the repository root.
