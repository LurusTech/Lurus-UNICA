#!/usr/bin/env bash
# Builds the judge release archives: one per platform, each with the binary,
# README, LICENSE and examples/, so the README's first command runs straight
# out of the unpacked folder. Builds outside the go.work (GOWORK=off): the
# binary must not depend on anything but pkg.
#
#   judge/scripts/dist.sh [out-dir]      (default: judge/dist, gitignored)
#   VERSION=v0.1.0 judge/scripts/dist.sh
set -euo pipefail

here="$(cd "$(dirname "$0")/.." && pwd)"
out="${1:-$here/dist}"
version="${VERSION:-$(git -C "$here" describe --tags --always --dirty 2>/dev/null || echo dev)}"
targets="${TARGETS:-windows/amd64 linux/amd64 linux/arm64 darwin/amd64 darwin/arm64}"

mkdir -p "$out"
out="$(cd "$out" && pwd)"
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT

for t in $targets; do
  os="${t%/*}" arch="${t#*/}"
  name="judge-$version-$os-$arch"
  dir="$stage/$name"
  mkdir -p "$dir"
  bin="judge"; [ "$os" = windows ] && bin="judge.exe"
  (cd "$here" && GOWORK=off CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
    go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$dir/$bin" ./cmd/judge)
  cp "$here/README.md" "$here/LICENSE" "$dir/"
  cp -r "$here/examples" "$dir/"
  tar -C "$stage" -czf "$out/$name.tar.gz" "$name"
  echo "built $out/$name.tar.gz"
done

(cd "$out" && sha256sum judge-"$version"-*.tar.gz > "judge-$version-SHA256SUMS")
echo "checksums $out/judge-$version-SHA256SUMS"
