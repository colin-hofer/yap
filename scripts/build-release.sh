#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

VERSION="${1:-}"
if [[ -z "$VERSION" ]]; then
  echo "usage: $0 <version>" >&2
  exit 1
fi

if [[ ! "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?$ ]]; then
  echo "version must look like v0.1.0" >&2
  exit 1
fi

VERSION_NO_V="${VERSION#v}"
DIST_DIR="$ROOT/dist/$VERSION"
TARGETS=(
  "linux/amd64"
  "linux/arm64"
  "darwin/amd64"
  "darwin/arm64"
  "windows/amd64"
  "windows/arm64"
)

rm -rf "$DIST_DIR"
mkdir -p "$DIST_DIR"

for target in "${TARGETS[@]}"; do
  GOOS="${target%/*}"
  GOARCH="${target#*/}"
  WORK_DIR="$DIST_DIR/${GOOS}_${GOARCH}"
  BIN_NAME="yap"
  if [[ "$GOOS" == "windows" ]]; then
    BIN_NAME="yap.exe"
  fi

  mkdir -p "$WORK_DIR"

  echo "building $GOOS/$GOARCH"
  GOCACHE="$ROOT/.gocache" GOOS="$GOOS" GOARCH="$GOARCH" \
    go build -trimpath -ldflags="-s -w" -o "$WORK_DIR/$BIN_NAME" ./cmd/yap

  ARCHIVE_BASE="yap_${VERSION_NO_V}_${GOOS}_${GOARCH}"
  if [[ "$GOOS" == "windows" ]]; then
    (cd "$WORK_DIR" && zip -q "$DIST_DIR/${ARCHIVE_BASE}.zip" "$BIN_NAME")
  else
    (cd "$WORK_DIR" && tar -czf "$DIST_DIR/${ARCHIVE_BASE}.tar.gz" "$BIN_NAME")
  fi

  rm -rf "$WORK_DIR"
done

(cd "$DIST_DIR" && sha256sum *.tar.gz *.zip > checksums.txt)

echo "wrote release assets to $DIST_DIR"
find "$DIST_DIR" -maxdepth 1 -type f | sort
