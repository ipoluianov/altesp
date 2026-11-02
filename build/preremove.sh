#!/bin/sh
# Runs before the package is removed (nfpm.yaml, deb and rpm): stops the
# running copies of /usr/bin/altesp, or they keep running from the
# deleted binary. Copies installed elsewhere (e.g. ~/.altbins by
# linux-install.sh) are left alone.

# Only on removal: deb passes "remove", rpm the number of versions left ("0");
# on an upgrade the running copies are left to the user
case "$1" in
  remove | 0) ;;
  *) exit 0 ;;
esac

BIN=/usr/bin/altesp

pids=""
for exe in /proc/[0-9]*/exe; do
  if [ "$(readlink "$exe" 2>/dev/null)" = "$BIN" ]; then
    pid=${exe#/proc/}
    pids="$pids ${pid%/exe}"
  fi
done
[ -n "$pids" ] || exit 0

# The settings and the project memory are saved as they change, and the logs are
# written as the data comes, so SIGTERM loses only the place of the window
kill $pids 2>/dev/null
i=0
while [ $i -lt 30 ]; do
  alive=""
  for pid in $pids; do
    kill -0 "$pid" 2>/dev/null && alive="$alive $pid"
  done
  [ -n "$alive" ] || exit 0
  pids=$alive
  sleep 0.1
  i=$((i + 1))
done
kill -9 $pids 2>/dev/null
exit 0
