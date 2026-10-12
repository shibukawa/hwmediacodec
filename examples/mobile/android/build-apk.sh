#!/bin/sh
# Builds and installs the sample app with the SDK's own tools (aapt2, javac,
# d8, zipalign, apksigner), without Gradle or Android Studio. Needs
# ANDROID_HOME with platforms;android-35 and build-tools;35.0.0, a JDK on
# JAVA_HOME (17 or 21), app/libs/mobile.aar from `ebitenmobile bind` (see
# ../README.md), and adb with one device or emulator attached for the
# install. The Gradle project in this directory is the usual route; this
# script is the one used to put the app on the emulator from the command
# line.
set -eu
cd "$(dirname "$0")"
: "${ANDROID_HOME:?set ANDROID_HOME}"
: "${JAVA_HOME:?set JAVA_HOME}"
BT=$ANDROID_HOME/build-tools/35.0.0
PLATFORM=$ANDROID_HOME/platforms/android-35/android.jar
PKG=jp.shibu.hwmediacodec.player
OUT=app/build-apk
rm -rf "$OUT"
mkdir -p "$OUT/aar" "$OUT/gen" "$OUT/classes" "$OUT/dex" "$OUT/lib"

echo "== unpack mobile.aar"
unzip -q -o app/libs/mobile.aar -d "$OUT/aar"

echo "== resources"
# aapt2 wants the package in the manifest, which AGP would add from the
# Gradle namespace; a copy gets it.
sed "s|<manifest |<manifest package=\"$PKG\" |" app/src/main/AndroidManifest.xml > "$OUT/AndroidManifest.xml"
"$BT/aapt2" compile --dir app/src/main/res -o "$OUT/res.zip"
"$BT/aapt2" link -o "$OUT/base.apk" -I "$PLATFORM" --manifest "$OUT/AndroidManifest.xml" \
  --java "$OUT/gen" --min-sdk-version 34 --target-sdk-version 35 "$OUT/res.zip"

echo "== java"
"$JAVA_HOME/bin/javac" -source 17 -target 17 -nowarn -cp "$PLATFORM:$OUT/aar/classes.jar" -d "$OUT/classes" \
  "$OUT/gen/$(echo $PKG | tr . /)/R.java" app/src/main/java/jp/shibu/hwmediacodec/player/*.java
"$BT/d8" --release --min-api 34 --lib "$PLATFORM" --output "$OUT/dex" \
  $(find "$OUT/classes" -name '*.class') "$OUT/aar/classes.jar"

echo "== package"
cp "$OUT/base.apk" "$OUT/unaligned.apk"
for abi in arm64-v8a x86_64; do
  if [ -f "$OUT/aar/jni/$abi/libgojni.so" ]; then
    mkdir -p "$OUT/lib/$abi"
    cp "$OUT/aar/jni/$abi/libgojni.so" "$OUT/lib/$abi/"
  fi
done
(cd "$OUT/dex" && zip -q -u ../unaligned.apk classes.dex)
# Native libraries go in uncompressed so that the system maps them in place.
(cd "$OUT" && zip -q -u -0 unaligned.apk $(find lib -name '*.so'))
"$BT/zipalign" -P 16 -f 4 "$OUT/unaligned.apk" "$OUT/aligned.apk"

echo "== sign (debug key)"
if [ ! -f "$OUT/../debug.keystore" ]; then
  "$JAVA_HOME/bin/keytool" -genkeypair -keystore "$OUT/../debug.keystore" -storepass android -keypass android \
    -alias androiddebugkey -dname "CN=Android Debug,O=Android,C=US" -keyalg RSA -keysize 2048 -validity 10000 > /dev/null 2>&1
fi
"$BT/apksigner" sign --ks "$OUT/../debug.keystore" --ks-pass pass:android --key-pass pass:android \
  --out "$OUT/app-debug.apk" "$OUT/aligned.apk"
ls -la "$OUT/app-debug.apk" | awk '{print "   " $5 " bytes " $9}'

if [ "${INSTALL:-1}" = 1 ] && "$ANDROID_HOME/platform-tools/adb" get-state > /dev/null 2>&1; then
  echo "== install and launch"
  "$ANDROID_HOME/platform-tools/adb" install -r "$OUT/app-debug.apk"
  "$ANDROID_HOME/platform-tools/adb" shell am start -n "$PKG/.MainActivity"
fi
