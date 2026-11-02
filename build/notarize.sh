#!/usr/bin/env bash
# Notarizes the signed macOS dmgs with Apple and staples the ticket to them.
# Usage: build/notarize.sh [dmg...]
# Without arguments takes the dmgs of the newest bin/<build>/ directory.
# The credentials come from the keychain profile saved once by
#   xcrun notarytool store-credentials notary --apple-id ... --team-id ... --password ...
# (MACOS_NOTARY_PROFILE picks another profile).
set -euo pipefail

NOTARY_PROFILE="${MACOS_NOTARY_PROFILE:-notary}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

if [ $# -eq 0 ]; then
  DIR="$(ls -td "$ROOT"/bin/*/ 2>/dev/null | head -1)"
  [ -n "$DIR" ] || { echo "Error: no builds in ${ROOT}/bin/" >&2; exit 1; }
  set -- "$DIR"*.dmg
  [ -e "$1" ] || { echo "Error: no dmg in ${DIR}" >&2; exit 1; }
fi

for DMG in "$@"; do
  if xcrun stapler validate -q "$DMG" 2>/dev/null; then
    echo "${DMG} is already notarized"
    continue
  fi
  # Apple takes only files signed with a Developer ID: fail before the upload
  codesign --verify --strict "$DMG" ||
    { echo "Error: ${DMG} is not signed, see build/_dmg.sh" >&2; exit 1; }

  echo "Notarizing ${DMG}"
  NOTARY_OUT="$(xcrun notarytool submit "$DMG" --keychain-profile "$NOTARY_PROFILE" --wait 2>&1)" || true
  echo "$NOTARY_OUT"
  if ! grep -q "status: Accepted" <<<"$NOTARY_OUT"; then
    NOTARY_ID="$(sed -n 's/^ *id: //p' <<<"$NOTARY_OUT" | head -1)"
    [ -n "$NOTARY_ID" ] && xcrun notarytool log "$NOTARY_ID" --keychain-profile "$NOTARY_PROFILE" >&2
    echo "Error: notarization of ${DMG} failed" >&2
    exit 1
  fi
  # The ticket inside the dmg lets Gatekeeper check it offline
  xcrun stapler staple "$DMG"
  spctl -a -t open --context context:primary-signature -v "$DMG"
done
