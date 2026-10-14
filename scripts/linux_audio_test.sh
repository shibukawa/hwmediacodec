#!/bin/sh
# Runs the audio tests on Linux, where the codecs are libraries of the
# distribution: libopus, libfdk-aac and libavcodec (FFmpeg). The test
# binaries are cross-compiled here and run in containers that have those
# libraries and the ffmpeg command the tests compare with, one container
# per distribution so that every libavcodec major version the backend
# accepts (58 to 62) is met.
#
# Each distribution runs three times: with every backend (AAC decodes
# through libavcodec and encodes through libfdk-aac), with
# HWMEDIACODEC_AUDIO_BACKENDS=libopus,fdk-aac (libfdk-aac decodes too) and
# with libopus,libavcodec (the AAC encoding tests skip).
#
# DISTROS overrides the images, TESTFLAGS is passed to the test binaries
# (for example TESTFLAGS=-test.v). Needs docker (or podman through
# DOCKER=podman) and Go.
set -eu
cd "$(dirname "$0")/.."
docker=${DOCKER:-docker}
distros=${DISTROS:-"debian:trixie debian:bookworm ubuntu:24.04 ubuntu:22.04 fedora:latest"}
# PLATFORM (for example linux/amd64) runs the containers, and so the test
# binaries, on another architecture than the host's through emulation.
platform=
suffix=
if [ -n "${PLATFORM:-}" ]; then
  platform="--platform $PLATFORM"
  suffix=-$(echo "$PLATFORM" | tr '/' '-')
fi

dockerfile() {
  case $1 in
    debian:*)
      # libfdk-aac is in Debian's non-free section.
      cat <<DOCKER
FROM $1
RUN sed -i 's/^Components: main\$/Components: main non-free/' /etc/apt/sources.list.d/debian.sources \\
 && apt-get update \\
 && apt-get install -y --no-install-recommends ffmpeg libopus0 libfdk-aac2 \\
 && rm -rf /var/lib/apt/lists/*
DOCKER
      ;;
    ubuntu:*)
      cat <<DOCKER
FROM $1
RUN apt-get update \\
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ffmpeg libopus0 libfdk-aac2 \\
 && rm -rf /var/lib/apt/lists/*
DOCKER
      ;;
    fedora:*)
      # Fedora's own packages: FFmpeg and the FDK library without the
      # parts Fedora does not ship (AAC-LC only).
      cat <<DOCKER
FROM $1
RUN dnf install -y ffmpeg-free opus fdk-aac-free && dnf clean all
DOCKER
      ;;
    *)
      echo "no recipe for $1" >&2
      return 1
      ;;
  esac
}

# ffmpeg cannot encode HE-AAC without a non-free library, and the test of
# it asks afconvert, which only macOS has. On a Mac the two files the test
# asks for are made here from the test's signal, and a stand-in afconvert
# hands them out in the containers.
mkdir -p bin/linuxaudio/he
rm -f bin/linuxaudio/he/*
if command -v afconvert >/dev/null 2>&1 && command -v ffmpeg >/dev/null 2>&1; then
  go run ./internal/audiocmp -make -rate 48000 -channels 2 -seconds 2 >bin/linuxaudio/he/in.pcm
  ffmpeg -v error -y -f s16le -ar 48000 -ac 2 -i bin/linuxaudio/he/in.pcm -c:a pcm_s16le bin/linuxaudio/he/in.wav
  afconvert -f m4af -d aach -b 64000 bin/linuxaudio/he/in.wav bin/linuxaudio/he/aach.m4a
  afconvert -f m4af -d aacp -b 32000 bin/linuxaudio/he/in.wav bin/linuxaudio/he/aacp.m4a
  cat >bin/linuxaudio/he/afconvert <<'SH'
#!/bin/sh
# Called as: afconvert -f m4af -d FORMAT -b BITRATE in.wav out.m4a
exec cp "$(dirname "$0")/$4.m4a" "$8"
SH
  chmod +x bin/linuxaudio/he/afconvert
fi

arch=
status=0
for distro in $distros; do
  image=hwmediacodec-audio-$(echo "$distro" | tr ':/' '--')$suffix
  dockerfile "$distro" | $docker build -q $platform -t "$image" - >/dev/null
  if [ -z "$arch" ]; then
    case $($docker run --rm $platform "$image" uname -m) in
      aarch64 | arm64) arch=arm64 ;;
      x86_64 | amd64) arch=amd64 ;;
      *) echo "unsupported container architecture" >&2; exit 1 ;;
    esac
    mkdir -p bin/linuxaudio
    for pkg in . ./playback ./mediacontainer/mp4 ./encoding/adts ./internal/audioutil; do
      name=$(basename "$(cd "$pkg" && pwd)")
      [ "$pkg" = . ] && name=hwmediacodec
      CGO_ENABLED=0 GOOS=linux GOARCH=$arch go test -c -o "bin/linuxaudio/$name.test" "$pkg"
    done
  fi
  for backends in "" "libopus,fdk-aac" "libopus,libavcodec"; do
    echo "== $distro, audio backends: ${backends:-all}"
    $docker run --rm $platform \
      -v "$PWD:/work:ro" -e HWMEDIACODEC_AUDIO_BACKENDS="$backends" \
      "$image" sh -c '
        PATH=/work/bin/linuxaudio/he:$PATH
        ffmpeg -version | head -1
        status=0
        cd /work && bin/linuxaudio/hwmediacodec.test -test.count=1 -test.run "Audio|SelectBackends|TestProbe" '"${TESTFLAGS:-}"' || status=1
        for dir in playback mediacontainer/mp4 encoding/adts internal/audioutil; do
          (cd /work/$dir && /work/bin/linuxaudio/$(basename $dir).test -test.count=1 -test.run "Audio|Sound|ADTS|Adts|Delay|Frames|Packet" '"${TESTFLAGS:-}"') || status=1
        done
        exit $status
      ' || status=1
  done
done
exit $status
