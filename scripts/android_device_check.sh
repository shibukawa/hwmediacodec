#!/bin/sh
# Runs the hwmediacodec CLI on an Android device or emulator over adb and
# checks it against ffmpeg on the host, the way the iOS Simulator run in
# the README was done: probe, decode a B-frame H.264 and an HEVC stream
# (per-frame SHA-256 in display order against ffmpeg's decode), encode
# H.264 and HEVC from raw NV12 (frame count and PSNR by ffprobe/ffmpeg),
# and transcode H.264 to HEVC. Then the audio codecs: AAC and Opus files
# from ffmpeg decoded on the device (every sample against ffmpeg's
# decoder), and the test signal encoded on the device, decoded by ffmpeg
# and compared with the input.
#
# Needs: adb with one device attached (or ANDROID_SERIAL set), the NDK at
# ANDROID_NDK_HOME, ffmpeg and ffprobe, python3. SOFTWARE=1 passes
# -software to every command, for emulators and devices whose codecs are
# the framework's software ones. ARCH=amd64 targets an x86_64 emulator.
set -eu
cd "$(dirname "$0")/.."
: "${ANDROID_NDK_HOME:?set ANDROID_NDK_HOME, for example \$ANDROID_HOME/ndk/<version>}"
ARCH=${ARCH:-arm64}
API=${ANDROID_API:-34}
SW=${SOFTWARE:+-software}
case $(uname -s) in
  Darwin) HOST=darwin-x86_64 ;;
  *) HOST=linux-x86_64 ;;
esac
case $ARCH in
  arm64) TRIPLE=aarch64-linux-android ;;
  amd64) TRIPLE=x86_64-linux-android ;;
  *) echo "ARCH must be arm64 or amd64"; exit 2 ;;
esac
CC=$ANDROID_NDK_HOME/toolchains/llvm/prebuilt/$HOST/bin/$TRIPLE$API-clang
WORK=$(mktemp -d)
DEV=/data/local/tmp/hwmediacodec-check
trap 'rm -rf "$WORK"' EXIT

echo "== build cmd/hwmediacodec for android/$ARCH"
CGO_ENABLED=1 GOOS=android GOARCH=$ARCH CC="$CC" go build -o "$WORK/hwmediacodec" ./cmd/hwmediacodec

echo "== make test media with ffmpeg"
ffmpeg -hide_banner -loglevel error -y -f lavfi -i testsrc2=size=320x240:rate=30 -frames:v 30 -pix_fmt yuv420p \
  -c:v libx264 -preset veryfast -bf 2 -g 25 -f h264 "$WORK/in.h264"
ffmpeg -hide_banner -loglevel error -y -f lavfi -i testsrc2=size=320x240:rate=30 -frames:v 30 -pix_fmt yuv420p \
  -c:v libx265 -preset veryfast -x265-params "bframes=2:open-gop=0:keyint=25:log-level=error" -f hevc "$WORK/in.hevc"
ffmpeg -hide_banner -loglevel error -y -f lavfi -i testsrc2=size=320x240:rate=30 -frames:v 10 -pix_fmt nv12 \
  -f rawvideo "$WORK/in.nv12"

# reference prints "frame N pts P WxH sha256" lines like the CLI, from
# ffmpeg's decode in display order.
reference() {
  ffmpeg -hide_banner -loglevel error -i "$1" -f rawvideo -pix_fmt nv12 - |
    python3 -c '
import hashlib, sys
w, h, n = 320, 240, 0
size = w * h * 3 // 2
data = sys.stdin.buffer.read()
for off in range(0, len(data) - size + 1, size):
    print("frame %5d %dx%d %s" % (n, w, h, hashlib.sha256(data[off:off+size]).hexdigest()))
    n += 1
'
}
# device_frames strips the pts column (the CLI numbers packets in decode
# order) so that the lines compare with reference.
device_frames() { awk '/^frame/ {print $1, $2, $5, $6}' "$1" | awk '{printf "frame %5d %s %s\n", $2, $3, $4}'; }

echo "== push to the device"
adb shell mkdir -p "$DEV"
adb push "$WORK/hwmediacodec" "$WORK/in.h264" "$WORK/in.hevc" "$WORK/in.nv12" "$DEV/" > /dev/null
adb shell chmod 755 "$DEV/hwmediacodec"
run() { adb shell "cd $DEV && ./hwmediacodec $*"; }

echo "== probe"
run probe

fail=0
for c in h264 hevc; do
  echo "== decode $c (display order, NV12 hashes against ffmpeg)"
  run decode -codec $c $SW -hash in.$c > "$WORK/dev_$c.txt" || { echo "decode $c failed"; fail=1; continue; }
  tail -1 "$WORK/dev_$c.txt"
  reference "$WORK/in.$c" > "$WORK/ref_$c.txt"
  if device_frames "$WORK/dev_$c.txt" | diff -q - "$WORK/ref_$c.txt" > /dev/null; then
    echo "   identical to ffmpeg: $(wc -l < "$WORK/ref_$c.txt") frames"
  else
    echo "   DIFFERS from ffmpeg (lossless decoders must agree; a software decoder with different rounding may not):"
    device_frames "$WORK/dev_$c.txt" | diff - "$WORK/ref_$c.txt" | head -6
    fail=1
  fi
done

if ffmpeg -hide_banner -encoders 2>/dev/null | grep -q libsvtav1; then
  echo "== decode av1 (IVF temporal units, NV12 hashes against ffmpeg)"
  ffmpeg -hide_banner -loglevel error -y -f lavfi -i testsrc2=size=320x240:rate=30 -frames:v 30 -pix_fmt yuv420p \
    -c:v libsvtav1 -preset 10 -g 25 -f ivf "$WORK/in.ivf"
  adb push "$WORK/in.ivf" "$DEV/" > /dev/null
  if run decode -codec av1 $SW -hash in.ivf > "$WORK/dev_av1.txt"; then
    tail -1 "$WORK/dev_av1.txt"
    reference "$WORK/in.ivf" > "$WORK/ref_av1.txt"
    if device_frames "$WORK/dev_av1.txt" | diff -q - "$WORK/ref_av1.txt" > /dev/null; then
      echo "   identical to ffmpeg: $(wc -l < "$WORK/ref_av1.txt") frames"
    else
      echo "   DIFFERS from ffmpeg:"; device_frames "$WORK/dev_av1.txt" | diff - "$WORK/ref_av1.txt" | head -6; fail=1
    fi
  else
    echo "   decode av1 failed (no AV1 decoder, or a software one without SOFTWARE=1)"; fail=1
  fi
fi

echo "== decode h264 to RGBA"
run decode -codec h264 -format rgba $SW -hash in.h264 | tail -1

for c in h264 hevc; do
  echo "== encode $c from NV12"
  if run encode -size 320x240 -codec $c -rate 30 -bitrate 800k -gop 5 $SW -o out.$c in.nv12; then
    adb pull "$DEV/out.$c" "$WORK/out.$c" > /dev/null
    ffprobe -hide_banner -loglevel error -count_frames -select_streams v -show_entries stream=codec_name,width,height,nb_read_frames -of csv=p=0 "$WORK/out.$c"
    ffmpeg -hide_banner -i "$WORK/out.$c" -f rawvideo -pix_fmt nv12 -s 320x240 -i "$WORK/in.nv12" -lavfi "[0:v]format=nv12[a];[a][1:v]psnr" -f null - 2>&1 | grep -o 'PSNR.*' || true
  else
    echo "   encode $c failed"; fail=1
  fi
done

echo "== transcode h264 -> hevc"
if run transcode -in h264 -codec hevc -bitrate 800k $SW -o tc.hevc in.h264; then
  adb pull "$DEV/tc.hevc" "$WORK/tc.hevc" > /dev/null
  ffprobe -hide_banner -loglevel error -count_frames -select_streams v -show_entries stream=codec_name,nb_read_frames -of csv=p=0 "$WORK/tc.hevc"
else
  echo "   transcode failed"; fail=1
fi
# Audio: the framework's AAC and Opus codecs, which are software on every
# device (no -software needed). The samples are compared on the host.
cmp_audio() { go run ./internal/audiocmp "$@"; }
go run ./internal/audiocmp -make -rate 48000 -channels 2 -seconds 2 > "$WORK/signal.pcm"
ffmpeg -hide_banner -loglevel error -y -f s16le -ar 48000 -ac 2 -i "$WORK/signal.pcm" -c:a aac -b:a 128k "$WORK/in_aac.mp4"
ffmpeg -hide_banner -loglevel error -y -f s16le -ar 48000 -ac 2 -i "$WORK/signal.pcm" -c:a aac -b:a 128k "$WORK/in_adts.aac"
audio_in="in_aac.mp4 in_adts.aac"
if ffmpeg -hide_banner -encoders 2>/dev/null | grep -q libopus; then
  ffmpeg -hide_banner -loglevel error -y -f s16le -ar 48000 -ac 2 -i "$WORK/signal.pcm" -c:a libopus -b:a 96k "$WORK/in_opus.mp4"
  audio_in="$audio_in in_opus.mp4"
fi
adb push "$WORK/signal.pcm" $(for f in $audio_in; do echo "$WORK/$f"; done) "$DEV/" > /dev/null
for f in $audio_in; do
  echo "== decode audio $f (every sample against ffmpeg's decoder)"
  if run decode-audio -o dec.pcm "$f"; then
    adb pull "$DEV/dec.pcm" "$WORK/dec.pcm" > /dev/null
    ffmpeg -hide_banner -loglevel error -y -flags2 +skip_manual -i "$WORK/$f" -f s16le -c:a pcm_s16le "$WORK/ref.pcm"
    cmp_audio -channels 2 -min-snr 50 "$WORK/ref.pcm" "$WORK/dec.pcm" || { echo "   DIFFERS from ffmpeg"; fail=1; }
  else
    echo "   decode-audio $f failed"; fail=1
  fi
done
for c in aac opus; do
  echo "== encode audio $c (ffmpeg decodes the MP4 file; against the input)"
  if run encode-audio -codec $c -bitrate 128k -o enc_$c.mp4 signal.pcm; then
    adb pull "$DEV/enc_$c.mp4" "$WORK/enc_$c.mp4" > /dev/null
    ffprobe -hide_banner -loglevel error -select_streams a -count_packets -show_entries stream=codec_name,sample_rate,channels,nb_read_packets,start_time,duration -of csv=p=0 "$WORK/enc_$c.mp4"
    # ffmpeg applies the edit list, so what it plays starts at the input's
    # first sample; behind the end of an Opus track it plays the padding.
    ffmpeg -hide_banner -loglevel error -y -i "$WORK/enc_$c.mp4" -f s16le -c:a pcm_s16le "$WORK/heard.pcm"
    cmp_audio -channels 2 -prefix -min-snr 15 "$WORK/signal.pcm" "$WORK/heard.pcm" || { echo "   the decoded file is not the input in place"; fail=1; }
  else
    echo "   encode-audio $c failed"; fail=1
  fi
done

adb shell rm -rf "$DEV"
[ $fail -eq 0 ] && echo "== all checks passed" || { echo "== some checks failed"; exit 1; }
