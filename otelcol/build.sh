#!/usr/bin/env bash
# Builds the minimal collector from otelcol/builder-config.yaml.
#
#   otelcol/build.sh [GOARCH]     (default arm64: the instance is Graviton)
#
# Output: otelcol/_build/otelcol-danbot-linux-<arch> and its .sha256.
#
# The build is meant to be reproducible: a pinned Go toolchain, pinned ocb,
# CGO off, -trimpath, no VCS stamping, so the same manifest gives the same
# bytes on any host.
set -euo pipefail

OCB_VERSION=0.164.0
GO_TOOLCHAIN=go1.27.4
ARCH="${1:-arm64}"

cd "$(dirname "$0")"
rm -rf _build

export GOTOOLCHAIN="$GO_TOOLCHAIN"
export CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" GOFLAGS="-trimpath -buildvcs=false"

# ocb itself runs on the build host, so it is built for the host, not GOARCH.
OCB_DIR="$(mktemp -d)"
trap 'rm -rf "$OCB_DIR"' EXIT
GOOS= GOARCH= GOBIN="$OCB_DIR" \
  go install "go.opentelemetry.io/collector/cmd/builder@v${OCB_VERSION}"

# -s -w: no symbol table or DWARF (smaller binary, no runtime effect)
"$OCB_DIR/builder" --config=builder-config.yaml --ldflags="-s -w -buildid="

OUT="otelcol-danbot-linux-${ARCH}"
mv _build/otelcol-danbot "_build/$OUT"
(cd _build && sha256sum "$OUT" > "$OUT.sha256")
ls -l "_build/$OUT"
cat "_build/$OUT.sha256"
