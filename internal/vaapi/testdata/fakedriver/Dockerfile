# Image for scripts/vaapi_fake_driver_test.sh: libva with its headers and a C
# compiler to build the fake VA driver, and ffmpeg (libx264, libx265, libaom,
# SVT-AV1) plus aomenc to generate the test streams; aomenc reaches AV1
# coding tools (super-resolution, reference scaling, S-frames, tile groups)
# that ffmpeg's libaom wrapper has no option for. No GPU and no VA driver
# package is involved.
FROM debian:trixie
RUN apt-get update \
 && apt-get install -y --no-install-recommends gcc libc6-dev libva-dev ffmpeg aom-tools \
 && rm -rf /var/lib/apt/lists/*
