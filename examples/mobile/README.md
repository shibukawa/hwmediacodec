# mobile: the player on an iPhone, iPad or Android device

This directory is the video player as an Ebitengine *mobile* game: a Go
package without a `main`, which `ebitenmobile bind` compiles into a native
library that a normal Xcode or Android Studio project embeds. The game
plays the bundled waterfall clip ([`../assets`](../assets)) in a loop
through the device's hardware decoder (VideoToolbox on iOS, MediaCodec on
Android), shows what `hwmediacodec.Probe` found at the top of the screen,
and pauses and resumes on a tap. The **Run check** button at the bottom
right runs the machine check of
[`hwmediacodec check`](../../cmd/hwmediacodec/README.md#check) on the device (see
[The check on a phone or tablet](#the-check-on-a-phone-or-tablet)).
`ios/` is the Xcode project, `android/` the Android Studio project; both
embed the same Go code.

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

## The check on a phone or tablet

**Run check** does what `hwmediacodec check` does on a desktop, minus the
WebRTC stage (that needs a desktop browser), with the same checks from
[`cmd/hwmediacodec/checker`](../../cmd/hwmediacodec/checker) and
[`checker/playback`](../../cmd/hwmediacodec/checker/playback):

1. **Automatic** (about a minute): `Probe`, then decode of the bundled clip
   in H.264, HEVC and AV1, the encode round trips with their PSNR and order
   checks, and the AAC / Opus round trips, once per backend. The list
   fills in as the checks run; **Stop** ends the run and keeps what was
   checked.
2. **Playback**: the clip plays with sound in every codec the device
   decodes, and the screen asks whether picture and sound were right:
   **Yes**, **No** or **Skip**.
3. **Report**: the results (drag to scroll), with **Share**, which hands
   the Markdown report to the share sheet (Copy, Mail, Notes, ... on iOS;
   the chooser with its copy button on Android), and **Open issue**, which
   opens a new issue on GitHub with the title and the report filled in
   (a report over about 8 KB is left out of the link: share it and paste
   it). Nothing is sent until the issue is submitted. **Back** returns to
   the player once the automatic checks are over.

The report names the device: the model identifier and OS version on iOS
(`iPhone16,1, iOS 26.0`), maker, model, SoC and Android version on
Android, passed in by the native code (`Mobile.setDeviceInfo`). The share
sheet and the browser are native too: `Host` in
[`check.go`](check.go) is implemented by `ReportHost` in
[`AppDelegate.swift`](ios/HWMediaCodecPlayer/AppDelegate.swift) and in
[`MainActivity.java`](android/app/src/main/java/jp/shibu/hwmediacodec/player/MainActivity.java).
Both apps keep the screen on, because a locked screen takes the hardware
codecs away in the middle of a check. On tablets the text is drawn two
points to a pixel so that it stays readable; the video keeps its full
resolution.

In the iOS Simulator nothing decodes in hardware: the decode checks say
`unsupported`, the encode round trips code a synthetic picture, and the
playback stage plays the clips through the OS software decoder, labelled
`(software decoder)` in the report, so that the stage can be tried there.
On the Android emulator the goldfish H.264 and HEVC decoders pass and
the encoders, software only, are `unsupported`. Both were run that way
(iPhone 17 Pro Simulator with iOS 26, Android 14 arm64 emulator as a
tablet). On an iPhone SE (3rd generation, iOS 26.6.2) every check passed
([#31](https://github.com/shibukawa/hwmediacodec/issues/31)), and on a
Redmi Pad SE (Snapdragon 680, Android 15) H.264, HEVC, AAC and Opus
passed (AV1 unsupported: that SoC has no AV1 decoder or encoder).

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

Then either open `mobile/android` in Android Studio (it adds the Gradle
wrapper on first open) and run the `app` configuration on the device, or
build the APK with the SDK's own tools, which needs neither Gradle nor
Android Studio:

```sh
cd mobile/android
./build-apk.sh        # aapt2, javac, d8, zipalign, apksigner; installs and launches when adb sees a device
```

An emulator does as a device for this (`android sdk install emulator
"system-images/android-34/default/arm64-v8a"`, then
`android emulator create` / `android emulator start`); its codecs are the
framework's software ones and the goldfish host decoders, so the app says
`(software decoder)` only where no goldfish decoder exists.

`ebitenmobile bind` compiles the Go code with the NDK's clang (purego needs
the cgo runtime on Android; the library itself has no cgo code) into
`mobile.aar`, which the app module references but which is not checked in.
The activity is one `EbitenView` from the aar in a plain `Activity`, no
AndroidX
([`android/app/src/main/java/.../MainActivity.java`](android/app/src/main/java/jp/shibu/hwmediacodec/player/MainActivity.java)).

What to expect on a device:

- The top line reads `android/arm64  mediacodec: h264 decode, h264 encode,
  hevc decode, hevc encode` (plus `av1 decode` on SoCs with an AV1
  decoder) and the clip decodes through the vendor's codec. On an emulator
  only software codecs exist, so the line says `(software decoder)`.
- `adb logcat -s hwmediacodec Go` shows what the Go side logs. A codec whose
  output layout the backend does not understand ends with an error on screen
  that quotes the MediaCodec output format; please report it.

`mobile.aar` builds on the Mac with the command above (NDK r27, API 34,
about a minute) and `build-apk.sh` turns it into an APK that runs on the
Android 14 emulator (that is how the app was verified; the Gradle project
itself was not built here, there being no Gradle or Android Studio on the
development machine). The same APK runs on a Redmi Pad SE (Android 15);
on Xiaomi devices turn on **Install via USB** in the developer options
first, or `adb install` fails with `INSTALL_FAILED_USER_RESTRICTED`. See the
Android notes in the repository README.
