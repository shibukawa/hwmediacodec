#!/bin/sh
# Builds the module for the mobile targets. Unlike crossbuild.sh these need
# CGO_ENABLED=1: purego's dlopen goes through the real cgo runtime on iOS
# and Android (the library itself has no cgo code), so a C toolchain for the
# target is required. iOS needs Xcode; Android needs the NDK at
# $ANDROID_NDK_HOME (skipped when unset).
set -eu
cd "$(dirname "$0")/.."

build() {
  printf '%-22s ' "$1"
  shift
  env "$@" go build ./... && echo ok
}

if command -v xcrun >/dev/null 2>&1; then
  for sdk in iphoneos iphonesimulator; do
    SDK=$(xcrun --sdk $sdk --show-sdk-path)
    CLANG=$(xcrun --sdk $sdk -f clang)
    case $sdk in
      iphoneos) min="-miphoneos-version-min=15.0"; archs="arm64" ;;
      *) min="-mios-simulator-version-min=15.0"; archs="arm64 amd64" ;;
    esac
    for arch in $archs; do
      carch=$arch; [ "$arch" = amd64 ] && carch=x86_64
      build "ios/$arch ($sdk)" CGO_ENABLED=1 GOOS=ios GOARCH=$arch CC="$CLANG" \
        CGO_CFLAGS="-isysroot $SDK -arch $carch $min" CGO_LDFLAGS="-isysroot $SDK -arch $carch $min"
    done
  done
else
  echo "ios: skipped (no Xcode)"
fi

# The Android backend's own Go code is type-checked on any host with the
# mediacodec_typecheck tag (it needs no NDK; nothing runs).
printf '%-22s ' "android (typecheck)"
go vet -tags mediacodec_typecheck ./internal/mediacodec/... && echo ok

if [ -n "${ANDROID_NDK_HOME:-}" ]; then
  HOST=$(uname -s | tr '[:upper:]' '[:lower:]')-x86_64
  BIN="$ANDROID_NDK_HOME/toolchains/llvm/prebuilt/$HOST/bin"
  API=${ANDROID_API:-28}
  build "android/arm64" CGO_ENABLED=1 GOOS=android GOARCH=arm64 CC="$BIN/aarch64-linux-android$API-clang"
  build "android/amd64" CGO_ENABLED=1 GOOS=android GOARCH=amd64 CC="$BIN/x86_64-linux-android$API-clang"
else
  echo "android: skipped (ANDROID_NDK_HOME is not set)"
fi
