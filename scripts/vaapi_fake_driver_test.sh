#!/bin/sh
# Runs the vaapi backend end to end, without a GPU, against the fake VA
# driver in internal/vaapi/testdata/fakedriver: the test binary is
# cross-compiled here and run in a container that has libva, where the
# driver is built with gcc. The driver checks every buffer the backend hands
# it through the libva C headers; see fakevaapi_linux_test.go. libva is given
# /dev/null as its DRM device, which a preloaded shim (fake_drm.c) makes
# acceptable.
#
# The tests run once per driver personality:
#   packed   VA_ENC_PACKED_HEADER_* mask the encoder reports
#            (1 sequence only, as Mesa; 5 sequence and slice, as Intel; 0 none)
#   hevc     1 when the driver reports HEVC block sizes and features
#   derive   1 when vaDeriveImage works, 0 to force vaGetImage/vaPutImage
#
# TESTFLAGS is passed to the test binary (for example TESTFLAGS=-test.v).
# Needs docker (or podman through DOCKER=podman) and Go.
set -eu
cd "$(dirname "$0")/.."
docker=${DOCKER:-docker}
image=hwmediacodec-vafake
src=internal/vaapi/testdata/fakedriver
# PLATFORM (for example linux/amd64) runs the container, and so the test
# binary, on another architecture than the host's through emulation.
platform=
if [ -n "${PLATFORM:-}" ]; then
  platform="--platform $PLATFORM"
  image=$image-$(echo "$PLATFORM" | tr '/' '-')
fi

$docker build -q $platform -t "$image" "$src" >/dev/null
case $($docker run --rm $platform "$image" uname -m) in
  aarch64 | arm64) arch=arm64 ;;
  x86_64 | amd64) arch=amd64 ;;
  *) echo "unsupported container architecture" >&2; exit 1 ;;
esac
mkdir -p bin/fakevaapi
CGO_ENABLED=0 GOOS=linux GOARCH=$arch go test -c -o bin/fakevaapi/hwmediacodec.test .

# FAKE_VA_MODES overrides the personalities: "packed hevc derive" triples
# separated by semicolons.
modes=${FAKE_VA_MODES:-"1 1 1;5 0 0;0 1 0;5 1 1"}
status=0
old_ifs=$IFS
IFS=';'
for mode in $modes; do
  IFS=$old_ifs
  set -- $mode
  echo "== packed headers $1, HEVC attributes $2, vaDeriveImage $3"
  $docker run --rm $platform \
    -v "$PWD/bin/fakevaapi:/work/bin:ro" -v "$PWD/$src:/work/src:ro" \
    -e FAKE_VA_PACKED="$1" -e FAKE_VA_HEVC_ATTRS="$2" -e FAKE_VA_DERIVE="$3" \
    -e FAKE_VA_LOG=/tmp/fake-va.log \
    -e LIBVA_DRIVERS_PATH=/tmp/drv -e LIBVA_DRIVER_NAME=fake \
    -e HWMEDIACODEC_VAAPI_DEVICE=/dev/null -e HWMEDIACODEC_BACKENDS=vaapi \
    -e HWMEDIACODEC_TEST_FAKE_VAAPI=1 \
    "$image" sh -c '
      mkdir -p /tmp/drv /tmp/run &&
      gcc -O1 -Wall -Wno-unused-parameter -shared -fPIC -o /tmp/drv/fake_drv_video.so /work/src/fake_drv_video.c &&
      gcc -O1 -Wall -shared -fPIC -o /tmp/drv/libfakedrm.so /work/src/fake_drm.c &&
      cd /tmp/run &&
      LD_PRELOAD=/tmp/drv/libfakedrm.so /work/bin/hwmediacodec.test -test.count=1 -test.run FakeVAAPI '"${TESTFLAGS:-}"'
    ' || status=1
done
exit $status
