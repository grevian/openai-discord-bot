#!/usr/bin/env bash
# Builds the bot as a static binary for the instance and packs it with the
# files it reads from its working directory (prompts/).
#
#   scripts/build-release.sh VERSION [GOARCH]     (default arm64: Graviton)
#
# Output: dist/danbot-linux-<arch>.tar.gz and its .sha256 (sha256sum format).
# VERSION (the git sha in CI) is reported as deploy.version in telemetry: the
# same -X flag the Dockerfile passes, so the container and the native binary
# report alike.
#
# The tarball is deterministic (sorted entries, fixed mtime and owner, no gzip
# timestamp), so building the same commit twice gives the same bytes and a
# re-run of a deploy can tell its upload is already there.
set -euo pipefail

if [ $# -lt 1 ] || [ $# -gt 2 ]; then
  echo "usage: scripts/build-release.sh VERSION [GOARCH]" >&2
  exit 2
fi
VERSION="$1"
ARCH="${2:-arm64}"
if ! [[ "$VERSION" =~ ^[A-Za-z0-9._+-]+$ ]]; then
  echo "build-release: invalid version '$VERSION'" >&2
  exit 2
fi

cd "$(dirname "$0")/.."
OUT="danbot-linux-${ARCH}.tar.gz"
STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT

CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -trimpath -buildvcs=false \
  -ldflags "-s -w -X openai-discord-bot/telemetry.Version=${VERSION}" \
  -o "$STAGE/danbot" .
mkdir "$STAGE/prompts"
cp prompts/danbo.json "$STAGE/prompts/"

mkdir -p dist
rm -f "dist/$OUT" "dist/$OUT.sha256"
tar --sort=name --mtime='@0' --owner=0 --group=0 --numeric-owner \
  -C "$STAGE" -cf - danbot prompts | gzip -n -9 > "dist/$OUT"
(cd dist && sha256sum "$OUT" > "$OUT.sha256")
ls -l "dist/$OUT"
cat "dist/$OUT.sha256"
