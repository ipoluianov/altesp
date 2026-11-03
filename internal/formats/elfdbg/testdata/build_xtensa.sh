#!/bin/sh
# Builds xtensa_app.elf from xtensa_app.c with the ESP32 toolchain
# (xtensa-esp32-elf-gcc in PATH, or GCC=/path/to/xtensa-esp32-elf-gcc)
set -e
cd "$(dirname "$0")"
CC=${GCC:-xtensa-esp32-elf-gcc}
$CC -O2 -g -mlongcalls -ffunction-sections -ffile-prefix-map="$PWD"=testdata \
	-nostdlib -nostartfiles -Wl,-e,app_main -Wl,--gc-sections \
	-Wl,-Ttext=0x400d0020 -Wl,-Tdata=0x3ffb0000 xtensa_app.c -o xtensa_app.elf
