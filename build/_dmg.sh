#!/usr/bin/env bash
# Packs a macOS binary into AltEsp.app and then into a .dmg next to it.
# Usage: build/_dmg.sh <binary> <version>
# Uses hdiutil on macOS; elsewhere genisoimage/mkisofs/xorriso (an ISO image
# that macOS mounts as a usual dmg). Skips with a warning when none is found.
set -euo pipefail

BIN="$1"
VERSION="$2"
APP=altesp
DISPLAY_NAME=AltEsp
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DMG="${BIN}.dmg"

TOOL=""
for t in hdiutil genisoimage mkisofs xorriso; do
  if command -v "$t" >/dev/null 2>&1; then TOOL="$t"; break; fi
done
if [ -z "$TOOL" ]; then
  echo "Warning: no hdiutil/genisoimage/mkisofs/xorriso found, skipping ${DMG}" >&2
  exit 0
fi

# The numbers of the version for Info.plist: v1.2.3-4-gabc -> 1.2.3
SHORT_VERSION="$(printf '%s' "$VERSION" | sed -E 's/^v//; s/-.*//')"
[[ "$SHORT_VERSION" =~ ^[0-9]+(\.[0-9]+)*$ ]] || SHORT_VERSION="0.0.0"

STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT

BUNDLE="$STAGE/${DISPLAY_NAME}.app"
mkdir -p "$BUNDLE/Contents/MacOS" "$BUNDLE/Contents/Resources"
cp "$BIN" "$BUNDLE/Contents/MacOS/$APP"
chmod 755 "$BUNDLE/Contents/MacOS/$APP"

cat > "$BUNDLE/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>${DISPLAY_NAME}</string>
  <key>CFBundleDisplayName</key><string>${DISPLAY_NAME}</string>
  <key>CFBundleIdentifier</key><string>com.github.ipoluianov.${APP}</string>
  <key>CFBundleExecutable</key><string>${APP}</string>
  <key>CFBundleIconFile</key><string>${APP}.icns</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleVersion</key><string>${VERSION}</string>
  <key>CFBundleShortVersionString</key><string>${SHORT_VERSION}</string>
  <key>LSMinimumSystemVersion</key><string>11.0</string>
  <key>NSHighResolutionCapable</key><true/>
</dict>
</plist>
PLIST

# .icns of one PNG entry ('ic08' is 256x256, as icon.png)
be32() {
  local n=$1
  printf "$(printf '\\%03o\\%03o\\%03o\\%03o' \
    $(((n >> 24) & 255)) $(((n >> 16) & 255)) $(((n >> 8) & 255)) $((n & 255)))"
}
PNG="$ROOT/icon.png"
PNG_SIZE=$(wc -c < "$PNG" | tr -d ' ')
{
  printf 'icns'; be32 $((8 + 8 + PNG_SIZE))
  printf 'ic08'; be32 $((8 + PNG_SIZE))
  cat "$PNG"
} > "$BUNDLE/Contents/Resources/${APP}.icns"

# Developer ID signing from the login keychain; skipped with a warning where
# codesign or the certificate is missing (other machines, Linux, Windows)
SIGN_IDENTITY="${MACOS_SIGN_IDENTITY:-Developer ID Application: Ivan Poluianov (GKCU2PYV7S)}"
SIGN=0
if command -v codesign >/dev/null 2>&1 &&
  security find-identity -v -p codesigning | grep -qF "\"${SIGN_IDENTITY}\""; then
  SIGN=1
  # The hardened runtime (--options runtime) is required for notarization
  codesign --force --options runtime --timestamp --sign "$SIGN_IDENTITY" "$BUNDLE"
  codesign --verify --strict "$BUNDLE"
else
  echo "Warning: no \"${SIGN_IDENTITY}\" in the keychain, ${DMG} is unsigned" >&2
fi

# The link to drag the app to (cannot be made on Windows, so optional)
ln -s /Applications "$STAGE/Applications" 2>/dev/null || true

rm -f "$DMG"
case "$TOOL" in
  hdiutil)
    hdiutil create -quiet -volname "$DISPLAY_NAME" -srcfolder "$STAGE" -ov -format UDZO "$DMG" ;;
  genisoimage|mkisofs)
    "$TOOL" -quiet -V "$DISPLAY_NAME" -D -R -apple -no-pad -o "$DMG" "$STAGE" ;;
  xorriso)
    xorriso -as mkisofs -quiet -V "$DISPLAY_NAME" -D -R -hfsplus -no-pad -o "$DMG" "$STAGE" ;;
esac

if [ "$SIGN" = 1 ]; then
  codesign --force --timestamp --sign "$SIGN_IDENTITY" "$DMG"
fi
# Notarization is separate and slower: build/notarize.sh
echo "Packed ${DMG}"
