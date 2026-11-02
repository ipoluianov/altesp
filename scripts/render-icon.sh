#!/usr/bin/env bash
# Renders icon.png (256x256) from the drawing of the appicon package; draw
# icon.svg to match when the drawing changes
set -euo pipefail
cd "$(dirname "$0")/.."
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
go run ./cmd/mkicon -out "$TMP" -sizes 256 -ico=false
cp "$TMP/altesp-256.png" icon.png
echo "icon.png updated"
