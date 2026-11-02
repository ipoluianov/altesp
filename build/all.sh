#!/usr/bin/env bash
# Builds altesp for every supported platform into one bin/<yyyy-mm-dd-HH-MM>-<version>/.
set -euo pipefail
DIR="$(dirname "$0")"
BUILD_STAMP="$(date +%Y-%m-%d-%H-%M)"
export BUILD_STAMP
for t in linux/amd64 linux/arm64 windows/amd64 darwin/arm64; do
  "$DIR/_build.sh" "${t%/*}" "${t#*/}"
done
# Notarizes the signed dmg with Apple (a few minutes); on macOS only, the dmg
# made on the other systems is unsigned
if [ "$(uname)" = Darwin ]; then
  "$DIR/notarize.sh" "$DIR/../bin/${BUILD_STAMP}"-*/*.dmg
fi
