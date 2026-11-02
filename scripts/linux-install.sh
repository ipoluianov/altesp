#!/usr/bin/env bash
# Installer for __DISPLAY_NAME__ __TAG__ (linux/amd64 and linux/arm64,
# picked by the machine it runs on)
# Usage: curl -fsSL https://github.com/__REPO__/releases/latest/download/linux-install.sh | bash
#
# Layout (flat, shared by all utilities):
#   ~/.altbins/<app>                  - binary
#   ~/.altbins/.<app>-uninstall.sh    - uninstaller
#   ~/.altbins/.<app>-icon.{png,svg}  - icon (optional)
#   ~/.local/share/applications/<app>.desktop
set -euo pipefail

APP="__APP__"
DISPLAY_NAME="__DISPLAY_NAME__"
TAG="__TAG__"
REPO="__REPO__"

BIN_DIR="${HOME}/.altbins"
BIN="${BIN_DIR}/${APP}"
UNINSTALL="${BIN_DIR}/.${APP}-uninstall.sh"
APPS_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/applications"
DESKTOP_FILE="${APPS_DIR}/${APP}.desktop"

die() { echo "Error: $*" >&2; exit 1; }

[ "$(uname -s)" = "Linux" ] || die "this installer is for Linux only"
case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64|armv8*) ARCH=arm64 ;;
  *) die "unsupported architecture: $(uname -m) (expected x86_64 or aarch64)" ;;
esac
# A 64-bit kernel may run a 32-bit system (e.g. Raspberry Pi OS): the binary
# would start, but find no 64-bit libX11 to load
if [ "$(getconf LONG_BIT 2>/dev/null || echo 64)" != 64 ]; then
  die "a 64-bit system is required, this one is $(getconf LONG_BIT)-bit"
fi
URL="https://github.com/${REPO}/releases/download/${TAG}/${APP}-${TAG}-linux-${ARCH}.tar.gz"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "Downloading ${DISPLAY_NAME} ${TAG} (${ARCH})..."
if command -v curl >/dev/null 2>&1; then
  curl -fL --progress-bar -o "$TMP/app.tar.gz" "$URL"
elif command -v wget >/dev/null 2>&1; then
  wget -q --show-progress -O "$TMP/app.tar.gz" "$URL"
else
  die "curl or wget is required"
fi

tar -xzf "$TMP/app.tar.gz" -C "$TMP"
[ -f "$TMP/$APP" ] || die "binary '$APP' not found in archive"

mkdir -p "$BIN_DIR" "$APPS_DIR"

# Migrate from the old layout (~/.altbins/<app>/ directory)
if [ -d "$BIN" ]; then
  pkill -x "$APP" 2>/dev/null || true
  rm -rf "$BIN"
fi

# cp + mv: atomic replace, works even if the app is currently running
cp "$TMP/$APP" "$BIN_DIR/.$APP.new"
chmod 755 "$BIN_DIR/.$APP.new"
mv -f "$BIN_DIR/.$APP.new" "$BIN"

# Icon: use one from the archive if present, otherwise a theme icon
rm -f "$BIN_DIR/.$APP-icon.png" "$BIN_DIR/.$APP-icon.svg"
ICON="utilities-terminal"
for ext in png svg; do
  if [ -f "$TMP/$APP.$ext" ]; then
    ICON="$BIN_DIR/.$APP-icon.$ext"
    cp "$TMP/$APP.$ext" "$ICON"
    break
  fi
done

# --- uninstall script ---
{
  echo '#!/usr/bin/env bash'
  printf 'APP=%q\n' "$APP"
  printf 'DISPLAY_NAME=%q\n' "$DISPLAY_NAME"
  printf 'BIN_DIR=%q\n' "$BIN_DIR"
  printf 'APPS_DIR=%q\n' "$APPS_DIR"
  printf 'DESKTOP_FILE=%q\n' "$DESKTOP_FILE"
  cat <<'EOF'
set -u
MSG="Uninstall ${DISPLAY_NAME}?"

if [ -t 0 ]; then
  read -r -p "$MSG [y/N] " ans
  [[ "$ans" =~ ^[Yy] ]] || exit 0
elif command -v kdialog >/dev/null 2>&1; then
  kdialog --title "$DISPLAY_NAME" --yesno "$MSG" || exit 0
elif command -v zenity >/dev/null 2>&1; then
  zenity --question --title="$DISPLAY_NAME" --text="$MSG" || exit 0
fi

pkill -x "$APP" 2>/dev/null || true
rm -f "$DESKTOP_FILE" \
      "$BIN_DIR/$APP" \
      "$BIN_DIR/.$APP-icon.png" \
      "$BIN_DIR/.$APP-icon.svg" \
      "$BIN_DIR/.$APP-uninstall.sh"

# Remove ~/.altbins and its PATH entries only if no other utilities are left
if rmdir "$BIN_DIR" 2>/dev/null; then
  for rc in "$HOME/.profile" "$HOME/.bash_profile" "$HOME/.bashrc" \
            "$HOME/.zprofile" "$HOME/.zshrc"; do
    [ -f "$rc" ] && sed -i --follow-symlinks '/# added by altbins$/d' "$rc"
  done
  rm -f "${XDG_CONFIG_HOME:-$HOME/.config}/fish/conf.d/altbins.fish"
fi

command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database "$APPS_DIR" 2>/dev/null
{ kbuildsycoca6 || kbuildsycoca5; } >/dev/null 2>&1 || true

if [ -t 1 ]; then
  echo "${DISPLAY_NAME} has been removed."
else
  command -v notify-send >/dev/null 2>&1 && notify-send "$DISPLAY_NAME" "${DISPLAY_NAME} has been removed."
fi
exit 0
EOF
} > "$UNINSTALL"
chmod 755 "$UNINSTALL"

# --- add ~/.altbins to PATH ---
# One marked line per rc file; idempotent, removed by the last uninstaller.
PATH_MARKER="# added by altbins"
PATH_LINE='case ":$PATH:" in *":$HOME/.altbins:"*) ;; *) export PATH="$HOME/.altbins:$PATH" ;; esac '"$PATH_MARKER"

add_path_line() {
  local rc="$1"
  grep -qF "$PATH_MARKER" "$rc" 2>/dev/null && return 0
  printf '\n%s\n' "$PATH_LINE" >> "$rc"
}

# ~/.profile: read by login shells and most GNOME/KDE sessions
[ -f "$HOME/.profile" ] || touch "$HOME/.profile"
add_path_line "$HOME/.profile"

# bash reads ~/.bash_profile instead of ~/.profile when it exists;
# ~/.bashrc covers interactive terminals. zsh ignores ~/.profile.
for rc in "$HOME/.bash_profile" "$HOME/.bashrc" "$HOME/.zprofile" "$HOME/.zshrc"; do
  [ -f "$rc" ] && add_path_line "$rc"
done

# fish
if command -v fish >/dev/null 2>&1 || [ -d "${XDG_CONFIG_HOME:-$HOME/.config}/fish" ]; then
  FISH_CONF="${XDG_CONFIG_HOME:-$HOME/.config}/fish/conf.d"
  mkdir -p "$FISH_CONF"
  echo 'contains -- $HOME/.altbins $PATH; or set -gx PATH $HOME/.altbins $PATH' > "$FISH_CONF/altbins.fish"
fi

# --- .desktop file (GNOME / KDE) ---
# Exec values are quoted per the Desktop Entry spec
esc() { printf '%s' "$1" | sed -e 's/[\\"`$]/\\&/g'; }

cat > "$DESKTOP_FILE" <<EOF
[Desktop Entry]
Type=Application
Name=${DISPLAY_NAME}
Comment=${DISPLAY_NAME} ${TAG}
Exec="$(esc "$BIN")"
Icon=${ICON}
Terminal=false
Categories=Development;Electronics;Utility;
StartupNotify=false
Actions=Uninstall;

[Desktop Action Uninstall]
Name=Uninstall
Name[ru]=Удалить
Exec="$(esc "$UNINSTALL")"
Icon=edit-delete
EOF
chmod 644 "$DESKTOP_FILE"

command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database "$APPS_DIR" 2>/dev/null || true
{ kbuildsycoca6 || kbuildsycoca5; } >/dev/null 2>&1 || true

echo
echo "Installed ${DISPLAY_NAME} ${TAG} to ${BIN}"

# The serial ports belong to a group (dialout, uucp on Arch) the user may not be in
GROUP=""
for g in dialout uucp; do
  if getent group "$g" >/dev/null 2>&1; then GROUP="$g"; break; fi
done
if [ -n "$GROUP" ] && ! id -nG | tr ' ' '\n' | grep -qx "$GROUP"; then
  echo
  echo "To open the serial ports, add yourself to the group ${GROUP} and log in again:"
  echo "  sudo usermod -aG ${GROUP} \$USER"
fi
echo "Menu entry: ${DESKTOP_FILE}"
echo "Uninstall:  ${UNINSTALL}  (or right-click the app in the menu -> Uninstall)"

case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *)
    echo
    echo "${BIN_DIR} was added to PATH. Open a new terminal, or run now:"
    echo '  export PATH="$HOME/.altbins:$PATH"'
    ;;
esac