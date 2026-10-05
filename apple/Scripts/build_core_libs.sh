#!/usr/bin/env bash
# Build the Core Lattice engine (apple/engine) as static xcframeworks for the
# macOS/iOS Network Extensions, replacing the gomobile bind route. Same
# c-archive technique as build_tvos_lib.sh: GOOS/GOARCH plus an explicit
# clang -isysroot/-target pair — with only -isysroot the driver mispairs the
# sysroot and the inferred target ("using sysroot for 'AppleTVOS' but
# targeting 'iPhone'"). Design:
# docs/superpowers/specs/2026-10-05-apple-engine-carchive-migration-design.md.
#
# Artifacts land at the same paths gomobile used:
#   apple/Frameworks/iOS/LatticeCore.xcframework    (ios-arm64, static)
#   apple/Frameworks/MacOS/LatticeCore.xcframework  (macos-arm64, static)
# The first run copies the gomobile originals aside to
# apple/Frameworks-gomobile-backup/ — rollback is swapping the directory
# back; the backup is never refreshed by later runs.
#
# usage: build_core_libs.sh [ios|macos|all]   (default all)
set -euo pipefail
cd "$(dirname "$0")/../.."

FRAMEWORKS=apple/Frameworks
BACKUP=apple/Frameworks-gomobile-backup
STAGE=apple/build/corelib
IOS_MIN=17.0      # project.yml deploymentTarget (Lattice / LatticeTunnel)
MACOS_MIN=26.0    # project.yml deploymentTarget (LatticeMac / LatticeTunnelMac)
TARGET="${1:-all}"

# First run: keep the gomobile binaries as the rollback path. Never refresh
# afterwards — a later run would otherwise back up our own static output.
if [ -d "$FRAMEWORKS" ] && [ ! -d "$BACKUP" ]; then
    echo "backing up gomobile frameworks → $BACKUP"
    cp -R "$FRAMEWORKS" "$BACKUP"
fi

export GOTOOLCHAIN=go1.26.8
export CGO_ENABLED=1

build_slice() {  # name target goos goarch sdk
    local name=$1 target=$2 goos=$3 goarch=$4 sdk=$5
    local out="$STAGE/$name"
    rm -rf "$out"
    mkdir -p "$out"
    local sdk_path clang
    sdk_path=$(xcrun --sdk "$sdk" --show-sdk-path)
    clang=$(xcrun --sdk "$sdk" --find clang)
    GOOS="$goos" GOARCH="$goarch" \
        CC="$clang -isysroot $sdk_path -target $target" \
        go build -buildmode=c-archive -o "$out/LatticeCore.a" ./apple/engine/corelib
    # c-archive writes the header next to the archive, named after it; move
    # it into Headers/ — create-xcframework -headers wants a directory and
    # installs it as <slice>/Headers/.
    mkdir -p "$out/Headers"
    mv "$out/LatticeCore.h" "$out/Headers/"
}

case "$TARGET" in
    ios)
        build_slice ios "arm64-apple-ios$IOS_MIN" ios arm64 iphoneos
        ;;
    macos)
        build_slice macos "arm64-apple-macos$MACOS_MIN" darwin arm64 macosx
        ;;
    all)
        build_slice ios "arm64-apple-ios$IOS_MIN" ios arm64 iphoneos
        build_slice macos "arm64-apple-macos$MACOS_MIN" darwin arm64 macosx
        ;;
    *) echo "usage: $0 [ios|macos|all]"; exit 1 ;;
esac

mkdir -p "$FRAMEWORKS/iOS" "$FRAMEWORKS/MacOS"
rm -rf "$FRAMEWORKS/iOS/LatticeCore.xcframework" "$FRAMEWORKS/MacOS/LatticeCore.xcframework"
if [ -d "$STAGE/ios" ]; then
    xcodebuild -create-xcframework \
        -library "$STAGE/ios/LatticeCore.a" -headers "$STAGE/ios/Headers" \
        -output "$FRAMEWORKS/iOS/LatticeCore.xcframework" >/dev/null
    echo "iOS static xcframework: $FRAMEWORKS/iOS/LatticeCore.xcframework"
fi
if [ -d "$STAGE/macos" ]; then
    xcodebuild -create-xcframework \
        -library "$STAGE/macos/LatticeCore.a" -headers "$STAGE/macos/Headers" \
        -output "$FRAMEWORKS/MacOS/LatticeCore.xcframework" >/dev/null
    echo "macOS static xcframework: $FRAMEWORKS/MacOS/LatticeCore.xcframework"
fi
