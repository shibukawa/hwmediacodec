#!/bin/sh
# Verifies that the module builds with CGO_ENABLED=0 for every Tier 1 and
# Tier 2 target from a single host.
set -eu
cd "$(dirname "$0")/.."
for target in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do
  os=${target%/*}
  arch=${target#*/}
  printf '%-14s ' "$target"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build ./... && echo ok
done
# The browser: the WebCodecs backend (scripts/browser_test.sh runs it).
printf '%-14s ' "js/wasm"
GOOS=js GOARCH=wasm go build ./... && echo ok
