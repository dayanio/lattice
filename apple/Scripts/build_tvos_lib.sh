#!/usr/bin/env bash
# Build the Lattice engine as a tvOS static library (c-archive) for
# RefluxAppleTV. GOOS=ios + the appletvos SDK sysroot is the same technique
# the tvOS spike validated (2026-09-30).
set -euo pipefail
cd "$(dirname "$0")/../.."

OUT=apple/build/tvos
mkdir -p "$OUT"

SDK=$(xcrun --sdk appletvos --show-sdk-path)
CLANG=$(xcrun --sdk appletvos --find clang)

export GOTOOLCHAIN=go1.26.8
export CGO_ENABLED=1
export GOOS=ios
export GOARCH=arm64
export TVOS_DEPLOYMENT_TARGET=17.0
# The explicit -target is required: with only -isysroot the clang driver
# pairs the AppleTVOS sysroot with an inferred iOS target and fails with
# "using sysroot for 'AppleTVOS' but targeting 'iPhone'" (-Werror).
export CC="$CLANG -isysroot $SDK -target arm64-apple-tvos$TVOS_DEPLOYMENT_TARGET"

go build -buildmode=c-archive -o "$OUT/LatticeTVCore.a" ./apple/engine/tvoslib
# c-archive writes the header next to the archive as LatticeTVCore.h.
test -f "$OUT/LatticeTVCore.h"
echo "tvOS static lib: $OUT/LatticeTVCore.a (+ .h)"
