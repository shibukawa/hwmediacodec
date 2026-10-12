# mobile: the player on an iPhone, iPad or Android device

This directory is the video player as an Ebitengine *mobile* game: a Go
package without a `main`, which `ebitenmobile bind` compiles into a native
library that a normal Xcode or Android Studio project embeds. The game
plays the bundled waterfall clip ([`../assets`](../assets)) in a loop
through the device's hardware decoder (VideoToolbox on iOS, MediaCodec on
Android), shows what `hwmediacodec.Probe` found at the top of the screen,
and pauses and resumes on a tap. `ios/` is the Xcode project, `android/`
the Android Studio project; both embed the same Go code.

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

## Build and run on an Android device

Requirements: the Android SDK with platform 35, build-tools and an NDK,
a JDK (17 or 21), Go 1.27 or later, and a device running Android 14 or
later with USB debugging on. On a Mac without Android Studio, Homebrew's
command-line tools are enough; the `android` CLI in them replaces the
deprecated `sdkmanager`:

```sh
brew install --cask android-commandlinetools temurin@21
export ANDROID_HOME=/opt/homebrew/share/android-commandlinetools
export JAVA_HOME=$(/usr/libexec/java_home -v 21)
export PATH="$ANDROID_HOME/platform-tools:$PATH"
android sdk install platform-tools "platforms;android-35" "build-tools;35.0.0" "ndk;27.3.13750724"
android sdk list 'ndk*'     # the versions on offer
```

`ebitenmobile bind` finds the newest NDK under `$ANDROID_HOME/ndk` by
itself; `ANDROID_NDK_HOME` is only needed by `scripts/mobilebuild.sh`.
From the `examples` module:

```sh
cd examples
go run github.com/hajimehoshi/ebiten/v2/cmd/ebitenmobile bind -target android/arm64,android/amd64 \
    -androidapi 34 -javapkg jp.shibu.hwmediacodec \
    -o mobile/android/app/libs/mobile.aar github.com/shibukawa/hwmediacodec/examples/mobile
```

Then open `mobile/android` in Android Studio (it adds the Gradle wrapper on
first open) and run the `app` configuration on the device, or from the
command line:

```sh
cd mobile/android
gradle wrapper && ./gradlew installDebug
adb shell am start -n jp.shibu.hwmediacodec.player/.MainActivity
```

`ebitenmobile bind` compiles the Go code with the NDK's clang (purego needs
the cgo runtime on Android; the library itself has no cgo code) into
`mobile.aar`, which the app module references but which is not checked in.
The activity is one `EbitenView` from the aar
([`android/app/src/main/java/.../MainActivity.kt`](android/app/src/main/java/jp/shibu/hwmediacodec/player/MainActivity.kt)).

What to expect on a device:

- The top line reads `android/arm64  mediacodec: h264 decode, h264 encode,
  hevc decode, hevc encode` (plus `av1 decode` on SoCs with an AV1
  decoder) and the clip decodes through the vendor's codec. On an emulator
  only software codecs exist, so the line says `(software decoder)`.
- `adb logcat -s hwmediacodec Go` shows what the Go side logs. A codec whose
  output layout the backend does not understand ends with an error on screen
  that quotes the MediaCodec output format; please report it.

`mobile.aar` builds on the Mac with the command above (NDK r27, API 34,
about a minute); the Gradle app around it has not been built or run here
(no Gradle or Android Studio on the development machine), and the Go
backend has not run on a device. See the Android notes in the repository
README.
