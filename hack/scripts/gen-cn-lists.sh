#!/usr/bin/env bash
# Regenerate the embedded CN IPv4 block list from APNIC's delegated file.
# Usage: hack/scripts/gen-cn-lists.sh [-in <url-or-path>] [-out <file>]
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
exec go run ./apple/engine/split/internal/gencn "$@"
