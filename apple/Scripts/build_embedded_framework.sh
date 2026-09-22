#!/bin/bash
# Rebuild the gomobile-bound LatticeEmbedded.xcframework(s) from
# ./apple/engine/embedded — the no-NE embedded engine (see
# docs/superpowers/specs/2026-09-22-apple-embedded-sdk-design.md, M2).
#
# These binaries are git-ignored (see .gitignore) — same pattern as
# build_framework.sh: generate locally, don't bloat git history.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/../.."

export GOTOOLCHAIN=auto
export PATH="$PATH:$(go env GOPATH)/bin"

TARGET="${1:-all}"

info() { echo -e "\033[32m[build_embedded_framework]\033[0m $1"; }

if ! command -v gomobile &>/dev/null; then
    info "gomobile not found, installing..."
    go install golang.org/x/mobile/cmd/gomobile@latest
fi

gomobile init

build_macos() {
    info "Building apple/Frameworks/MacOS/LatticeEmbedded.xcframework..."
    rm -rf apple/Frameworks/MacOS/LatticeEmbedded.xcframework
    # -prefix=Lattice matches build_framework.sh — class names come out as
    # LatticeEmbeddedEmbeddedEngine etc. (prefix + package + type).
    gomobile bind -prefix=Lattice -target=macos -o apple/Frameworks/MacOS/LatticeEmbedded.xcframework ./apple/engine/embedded
}

build_ios() {
    info "Building apple/Frameworks/iOS/LatticeEmbedded.xcframework..."
    rm -rf apple/Frameworks/iOS/LatticeEmbedded.xcframework
    gomobile bind -prefix=Lattice -target=ios -o apple/Frameworks/iOS/LatticeEmbedded.xcframework ./apple/engine/embedded
}

build_combined() {
    # One xcframework with every Apple slice — the form a Swift Package
    # binaryTarget can consume for both macOS and iOS consumers.
    info "Merging slices into apple/Frameworks/Combined/LatticeEmbedded.xcframework..."
    rm -rf apple/Frameworks/Combined/LatticeEmbedded.xcframework
    mkdir -p apple/Frameworks/Combined
    xcodebuild -create-xcframework \
        -framework apple/Frameworks/iOS/LatticeEmbedded.xcframework/ios-arm64/LatticeEmbedded.framework \
        -framework apple/Frameworks/iOS/LatticeEmbedded.xcframework/ios-arm64_x86_64-simulator/LatticeEmbedded.framework \
        -framework apple/Frameworks/MacOS/LatticeEmbedded.xcframework/macos-arm64_x86_64/LatticeEmbedded.framework \
        -output apple/Frameworks/Combined/LatticeEmbedded.xcframework
}

case "$TARGET" in
    macos) build_macos ;;
    ios) build_ios ;;
    all) build_macos; build_ios; build_combined ;;
    *) echo "usage: $0 [macos|ios|all]"; exit 1 ;;
esac

info "Done."
