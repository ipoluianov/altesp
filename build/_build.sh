#!/usr/bin/env bash
# Builds altesp for one platform into bin/<yyyy-mm-dd-HH-MM>-<version>/.
# Usage: build/_build.sh <goos> <goarch>
# BUILD_STAMP (set by all.sh) puts several platforms into one directory.
set -euo pipefail

GOOS_T="$1"
GOARCH_T="$2"
APP=altesp
DISPLAY_NAME=AltEsp
REPO=ipoluianov/altesp
NFPM_VERSION=v2.47.0
WINRES_VERSION=v0.3.3
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
# The numbers of the version for the exe: v1.2.3-4-gabc -> 1.2.3
NUM_VERSION="$(printf '%s' "$VERSION" | sed -E 's/^v//; s/-.*//')"
[[ "$NUM_VERSION" =~ ^[0-9]+(\.[0-9]+)*$ ]] || NUM_VERSION="0.0.0"
LDFLAGS="-s -w -X github.com/ipoluianov/altesp/app.Version=${VERSION}"
EXT=""
if [ "$GOOS_T" = "windows" ]; then
  EXT=".exe"
  LDFLAGS="${LDFLAGS} -H=windowsgui"
fi

DIR="bin/${BUILD_STAMP:-$(date +%Y-%m-%d-%H-%M)}-${VERSION}"
OUT="${DIR}/${APP}-${GOOS_T}-${GOARCH_T}${EXT}"
# Windows is built for amd64 only, so its exe needs no platform suffix
[ "$GOOS_T" = "windows" ] && OUT="${DIR}/${APP}.exe"
mkdir -p "$DIR"
echo "Building ${OUT}"
if [ "$GOOS_T" = "windows" ]; then
  # The icon and the version shown by Explorer; go build links the .syso in
  trap 'rm -f "$ROOT"/rsrc_windows_*.syso' EXIT
  go run "github.com/tc-hib/go-winres@${WINRES_VERSION}" simply \
    --arch "$GOARCH_T" --out rsrc --manifest none --icon icon.png \
    --product-name "$DISPLAY_NAME" --file-description "$DISPLAY_NAME" --original-filename "${APP}.exe" \
    --copyright "Ivan Poluianov" --file-version "$NUM_VERSION" --product-version "$VERSION"
fi
CGO_ENABLED=0 GOOS="$GOOS_T" GOARCH="$GOARCH_T" \
  go build -trimpath -ldflags="${LDFLAGS}" -o "$OUT" .

if [ "$GOOS_T" = "darwin" ]; then
  "$ROOT/build/_dmg.sh" "$OUT" "$VERSION"
fi

if [ "$GOOS_T" = "linux" ]; then
  # nfpm is pure Go, so the packages are built on any system.
  # It doesn't expand variables in src, so the binary is staged at bin/.pkg (see nfpm.yaml).
  mkdir -p bin/.pkg
  cp "$OUT" "bin/.pkg/${APP}"
  for fmt in deb rpm; do
    PKG_ARCH="$GOARCH_T" PKG_VERSION="$VERSION" \
      go run "github.com/goreleaser/nfpm/v2/cmd/nfpm@${NFPM_VERSION}" \
      pkg --config build/nfpm.yaml --packager "$fmt" --target "${OUT}.${fmt}"
  done

  # The archive for scripts/linux-install.sh: the binary and the menu icon
  cp icon.svg "bin/.pkg/${APP}.svg"
  chmod 755 "bin/.pkg/${APP}"
  tar -czf "${DIR}/${APP}-${VERSION}-linux-${GOARCH_T}.tar.gz" -C bin/.pkg "$APP" "${APP}.svg"
  rm -rf bin/.pkg

  # The installer downloads the archive for its machine from the release of
  # this tag; it is the same for both architectures, so each build writes it
  tr -d '\r' < scripts/linux-install.sh | sed \
    -e "s|__APP__|${APP}|g" \
    -e "s|__DISPLAY_NAME__|${DISPLAY_NAME}|g" \
    -e "s|__TAG__|${VERSION}|g" \
    -e "s|__REPO__|${REPO}|g" > "${DIR}/linux-install.sh"
  chmod 755 "${DIR}/linux-install.sh"
  [[ "$VERSION" =~ ^v[0-9.]+$ ]] ||
    echo "Warning: ${VERSION} is not a clean tag, linux-install.sh points to a release that may not exist" >&2
fi
