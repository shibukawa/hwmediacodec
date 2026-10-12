#!/bin/sh
# Runs "go test" for GOOS=js GOARCH=wasm in a real browser, where the
# WebCodecs backend has something to drive, with ffmpeg on this machine as
# the oracle (see internal/browserexec). Arguments go to "go test"; without
# any, the conformance tests of the root package run.
#
#   scripts/browser_test.sh
#   scripts/browser_test.sh -run 'Decode' -v .
#   HWMEDIACODEC_BROWSER_HEADED=1 scripts/browser_test.sh
#   HWMEDIACODEC_BROWSER=/Applications/Firefox.app/Contents/MacOS/firefox scripts/browser_test.sh
#
# Needs: Chrome, Chromium or Edge (or HWMEDIACODEC_BROWSER), ffmpeg and
# ffprobe. Whether the codecs behind WebCodecs are hardware ones is the
# browser's answer; a headless browser on a machine without a GPU has
# none, and the hardware tests skip.
set -eu
cd "$(dirname "$0")/.."
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
# "go test" looks for go_js_wasm_exec on PATH to run a js/wasm test binary.
go build -o "$WORK/go_js_wasm_exec" ./internal/browserexec
[ $# -gt 0 ] || set -- .
PATH="$WORK:$PATH" GOOS=js GOARCH=wasm go test -count=1 "$@"
