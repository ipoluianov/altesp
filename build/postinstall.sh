#!/bin/sh
# Runs after the package is installed: the serial ports belong to a group
# the user may not be in yet
group=dialout
getent group uucp >/dev/null 2>&1 && ! getent group dialout >/dev/null 2>&1 && group=uucp
echo "AltEsp: to open the serial ports without root, add yourself to the group ${group}"
echo "and log in again:  sudo usermod -aG ${group} \$USER"
exit 0
