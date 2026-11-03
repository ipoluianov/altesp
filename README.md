# AltEsp

A tool for ESP32-family microcontrollers over USB: connect a board, see what
it is and what is flashed on it, flash firmware, read and analyze the flash,
edit NVS and file systems in place, watch the log with decoded panics. Like
STM32 ST-Link Utility, but it understands the structure of ESP32 flash:
partitions, application images, NVS, LittleFS/SPIFFS/FAT, eFuses. It runs on
Linux, Windows and macOS as a single binary and needs nothing else: no
Python, esptool, ESP-IDF or OpenOCD.

Website: https://altbins.pro/altesp/

Simple outside, powerful inside: a board is flashed in three actions — pick
the port, drop the build folder, press **Flash**. Everything else is one
right-click away.

## Features

- **Connect and know the board.** Chip and revision, MAC, flash size and ID,
  PSRAM, crystal, Flash Encryption / Secure Boot / Secure Download Mode, the
  partition table and the applications in it, which one boots. The ESP32
  family: ESP32, S2, S3, C2, C3, C5, C6, C61, H2, H4, P4 and newer chips the
  official stub loader supports.
- **Ports that survive resets.** Boards with built-in USB disappear on every
  reset and come back, maybe under another name: AltEsp finds them again by
  their USB ID, serial number or place in USB. USB-UART bridges (CP210x,
  CH340/CH343, FTDI) and the chips' own USB are recognized on every system.
- **Flashing.** Drop an ESP-IDF build folder, a PlatformIO or Arduino
  project, or single files: types and addresses come from the content.
  Before writing, the files are checked against the board — built for
  another chip or revision, overlapping, past the end of the flash. Writes
  are compressed, verified by MD5, and "only changed" skips what is already
  there.
- **Memory.** A hex view of the whole flash, read as you scroll, with a map
  of the partitions; compare with a file (by an MD5 computed on the chip),
  edit bytes, find, save regions, erase sectors.
- **Partitions.** The table with the application details, OTA slot
  switching, CSV export and import, a visual partition table editor.
- **In place, without reflashing.** An NVS editor (both format versions,
  encrypted NVS with its keys) and a file browser for LittleFS, SPIFFS and
  FAT with wear levelling.
- **Monitor.** The log with ESP-IDF levels colored and panics decoded into
  `function at file:line` with the application's .elf — Xtensa backtraces
  and RISC-V register dumps alike. Coredump analysis per task.
- **eFuses.** Every field decoded, with write and read protection; burning
  only in a separate mode, after a review of what can't be undone and
  typing BURN.
- **JTAG.** ESP32-C3, C6 and H2 are flashed over their built-in USB-JTAG,
  without the UART bootloader.
- **Image generators.** NVS from CSV, LittleFS/SPIFFS/FAT from a folder, a
  merged image from a set of files.
- **For scripts and AI agents.** The command line does what the window does,
  with JSON output; an MCP server and a local HTTP API give the same tools
  to agents and scripts.
- **Safe by design.** Erasing asks first and offers a backup; irreversible
  operations are hidden behind their own mode; every dangerous operation is
  written to a local log.

## Command line

```
altesp ports                           the ports and the boards on them
altesp info    [--port P]              the chip, its flash and what is flashed
altesp flash   [--port P] build/       a build folder, or <address> <file>...
altesp read    [--port P] 0 4MB backup.bin
altesp erase   [--port P] [<address> <size>]
altesp monitor [--port P] [--elf app.elf] [--reset]
altesp coredump [--port P] [--elf app.elf] [<file>]
altesp efuse   [--port P]              (efuse burn ... --confirm BURN)
altesp mcp                             an MCP server on stdin/stdout
altesp api     [--listen 127.0.0.1:8765]
```

`--jtag` works over the built-in USB-JTAG, `--json` gives machine-readable
output, `altesp help` lists everything. The port can be omitted when exactly
one board is connected.

## Installation

Download a build for your system from the
[releases](https://github.com/ipoluianov/altesp/releases):

- **Windows:** `altesp.exe`.
- **macOS** (Apple Silicon): the `.dmg` image.
- **Linux** (x86-64 and ARM64): a `.deb` or `.rpm` package (they also add
  the udev rules for the boards), or a user-level install:

  ```sh
  curl -fsSL https://github.com/ipoluianov/altesp/releases/latest/download/linux-install.sh | bash
  ```

Settings, projects and the operations log are stored in
`~/.altbins/.altesp/`.

### Linux

The serial ports belong to the group `dialout` (`uucp` on Arch): add
yourself to it and log in again (`sudo usermod -aG dialout $USER`).
ModemManager may grab the ports of boards with built-in USB and brltty may
grab CH340 adapters; AltEsp tells when it finds them running.

### JTAG

- **Linux:** a udev rule for `303a:1001` (the packages install it).
- **Windows:** the JTAG interface of the board ("Interface 2") needs the
  WinUSB driver, installed with Zadig; the serial port keeps its driver.
- **macOS:** nothing to install.

## Building

```sh
build/all.sh            # every platform; build\all.bat on Windows
build/linux-amd64.sh    # one platform: linux-arm64, windows-amd64, darwin-arm64
```

No C compiler is needed. Trying the window without a board:
`ALTESP_SIMULATE=1 go run .` shows a simulated ESP32-C3.

## Code

| Package | What it does |
| --- | --- |
| `internal/serial` | Ports on every system, USB IDs and place, ports that reappear |
| `internal/loader` | The serial protocol of the ROM loader and the stub, resets, a simulated board |
| `internal/chips` | The chip table, chip details through registers; `efuse`: every eFuse field, reading and burning |
| `internal/formats` | Images, partition tables, build folders; `nvs`, `littlefs`, `spiffs`, `fatfs`, `elfdbg` (panics, coredumps) |
| `internal/jtag` | USB-JTAG: USB without cgo, the Espressif JTAG protocol, the RISC-V debug module, flashing through the ROM |
| `internal/ops` | What the window and the command line do with a board |
| `forms`, `cli`, `cli/mcp` | The window, the command line, the MCP server and the local API |

Tests: `go test ./...`. The tests on a real board run only when asked
(`ALTESP_HW_PORT`, `ALTESP_HW_JTAG`); see "Hardware checks" in
[TZ.md](TZ.md), the specification.

## License

MIT. ESP32 is a trademark of Espressif Systems; AltEsp is not affiliated with
or endorsed by Espressif. The stub loaders are those of
[esp-flasher-stub](https://github.com/espressif/esp-flasher-stub) by
Espressif, used under the MIT license (`internal/loader/stubs/LICENSE-MIT`).
