#!/bin/bash
# Rebuild the gomobile-bound LatticeCore.xcframework(s) from ./apple/engine.
#
# These binaries are git-ignored (see .gitignore) — Xcode needs them present
# on disk to build/embed, but they are not committed, same pattern as
# reflux/PlayerKit's build_ffmpeg.sh: generate locally, don't bloat git history.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/../.."

export GOTOOLCHAIN=auto
export PATH="$PATH:$(go env GOPATH)/bin"

TARGET="${1:-all}"

info() { echo -e "\033[32m[build_framework]\033[0m $1"; }

if ! command -v gomobile &>/dev/null; then
    info "gomobile not found, installing..."
    go install golang.org/x/mobile/cmd/gomobile@latest
fi

gomobile init

build_macos() {
    info "Building apple/Frameworks/MacOS/LatticeCore.xcframework..."
    rm -rf apple/Frameworks/MacOS/LatticeCore.xcframework
    # -prefix=Lattice reproduces the gomobile class names the Swift
    # side imports (LatticeEngineEngine etc.) — without it the prefix
    # defaults to the package name ("Engine") and the app fails to compile.
    gomobile bind -prefix=Lattice -target=macos -o apple/Frameworks/MacOS/LatticeCore.xcframework ./apple/engine
}

build_ios() {
    info "Building apple/Frameworks/iOS/LatticeCore.xcframework..."
    rm -rf apple/Frameworks/iOS/LatticeCore.xcframework
    gomobile bind -prefix=Lattice -target=ios -o apple/Frameworks/iOS/LatticeCore.xcframework ./apple/engine
}

case "$TARGET" in
    macos) build_macos ;;
    ios) build_ios ;;
    all) build_macos; build_ios ;;
    *) echo "usage: $0 [macos|ios|all]"; exit 1 ;;
esac

info "Done."
