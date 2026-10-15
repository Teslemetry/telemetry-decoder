#!/usr/bin/env bash
# Builds static telemetry-decoder binaries for each release platform into dist/,
# with a SHA256SUMS file.
#
# Usage: scripts/build.sh [version]   (version defaults to git describe)
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
version="${1:-$(git -C "$root" describe --tags --always --dirty 2>/dev/null || echo dev)}"
platforms=(linux/amd64 linux/arm64 darwin/amd64 darwin/arm64)

rm -rf "$root/dist"
mkdir -p "$root/dist"
for platform in "${platforms[@]}"; do
  os="${platform%/*}" arch="${platform#*/}"
  out="dist/telemetry-decoder-$os-$arch"
  (cd "$root" && CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
    go build -trimpath -ldflags "-s -w -buildid= -X main.version=$version" -o "$out" .)
  echo "built $out"
done
(cd "$root/dist" && sha256sum telemetry-decoder-* >SHA256SUMS)
cat "$root/dist/SHA256SUMS"
