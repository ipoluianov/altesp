# AltEsp — technical specification

Nov 2, 2026 · @Ivan

## Purpose and core principle

AltEsp is a portable desktop application in the AltBins series for working with ESP32 microcontrollers over USB/UART: connecting, chip information, flashing, reading and analyzing flash, and a port monitor. It is the ESP32 counterpart of STM32 ST-Link Utility, but it understands the structure of ESP32 flash: partitions, application images, NVS, file systems, eFuse.

**Core principle: simple outside, powerful inside.**

- Someone who has just plugged in a board for the first time must be able to flash it in three actions: pick the port → pick the file → "Flash". No reading of documentation.
- The default screen shows only what is needed in 80% of cases. No dozens of fields for addresses, frequencies and SPI modes.
- Everything advanced is available but stays out of the way: in context menus (right click), expandable "Details" blocks, the "Tools" menu and hotkeys.
- Sensible defaults are detected automatically from the chip and the files: addresses, baud rate, flash mode. Manual settings are only overrides.
- Dangerous operations (eFuse, full erase, encryption) are never in plain sight and always require confirmation.
- When something cannot be done (driver, permissions, protection on the chip), the program says why and what to do, instead of a bare error.

## Users and typical scenarios

The main users are embedded developers, hobbyists and service engineers. The scenarios below are ordered by frequency; the first three must be doable from the main screen without opening any menu.

| Scenario | Who | How it looks in the program |
| --- | --- | --- |
| Flash a ready-made firmware | everyone | Port → file(s) → "Flash" |
| Watch the board's log | everyone | "Monitor" tab, port already selected |
| Find out what board this is and what is flashed on it | everyone | "Connect" → chip card and partition list |
| Back up and restore flash | service | "Read flash" button → file; back again by flashing |
| Understand the cause of a crash | developer | Monitor + point to the .elf → backtrace decoded |
| Change an NVS setting or a LittleFS file without reflashing | developer, service | Right click on a partition → "Open" |
| Inspect or burn eFuses | experienced developer | "Tools" menu → "eFuse" (with protection) |

## Platforms and build

Requirements are the same as for the other utilities in the AltBins series:

- **Windows**: a single portable .exe (x64). Optional installation via the Install link in the status bar.
- **macOS**: a signed and notarized .dmg for Apple Silicon.
- **Linux**: portable x64 and arm64 binaries, .deb and .rpm packages, installation into `~/.altbins` without root.
- Language is Go, built with a single `go build`, with no external tools or runtimes and without cgo. System APIs that Go does not cover (IOKit on macOS, WinUSB on Windows) are called via purego or syscalls, as NUI already does. Python, esptool, IDF, addr2line, OpenOCD, GDB are **not required**.
- Everything is embedded into the application: chip descriptions, official Espressif stub loaders, partition table templates, eFuse layouts. Nothing is downloaded from the internet during operation.
- **Network.** The program makes no network connections, with two exceptions, both off until the user turns them on: an update check on explicit request, and the optional local API/MCP server (listens on localhost only). No telemetry.
- The UI is built on NUI, as in the whole series. All code lives in the AltEsp repository: ready-made code from other utilities in the series may be copied and adapted, but not imported as a dependency or referenced. The program does not rely on other installed utilities — the hex viewer, terminal and everything else are implemented inside.

**License: MIT.** The About window and the website carry the line: "ESP32 is a trademark of Espressif Systems; AltEsp is not affiliated with or endorsed by Espressif." Espressif's logo and brand identity are not used.

- Third-party code, data and binaries are included only under MIT-compatible licenses (MIT, BSD, ISC, Apache-2.0), with their license text and attribution (for Apache-2.0 — also the NOTICE, if any) next to them and in the About window.
- Sources under GPL/LGPL — esptool, espefuse, idf.py monitor, OpenOCD, the old esptool stub — are not copied, ported or used as a reference implementation. What they do is implemented from Espressif's official documentation: the chip Technical Reference Manuals, the serial protocol description, the ESP-IDF Programming Guide (image, partition table, NVS, coredump, eFuse formats), and from Apache-2.0 sources of ESP-IDF (e.g. `esp_efuse_table.csv`, partition table templates).
- Every embedded table (chips, eFuse fields, reset sequences) states in a comment which document and version it was taken from.

**Stub loader.** The official stubs from [espressif/esp-flasher-stub](https://github.com/espressif/esp-flasher-stub) are used — licensed Apache-2.0 or MIT at your option; since esptool v5.3 this is the default stub.

- The ready-made JSON files from the project's releases are embedded into the binary via `//go:embed` (about 90 KB for all chips). The stub is chosen by chip and revision (e.g. ESP32-P4 and ESP32-P4 rev1 have separate stubs).
- The MIT license is chosen: its text sits next to the stubs in `internal/loader/stubs/`, and the project is credited in the About window.
- By default all work goes through the stub. If it fails to load — automatic fallback to the ROM loader with a warning naming what is unavailable. Per the [serial protocol documentation](https://docs.espressif.com/projects/esptool/en/latest/esp32/advanced-topics/serial-protocol.html), `ERASE_FLASH`, `ERASE_REGION`, `READ_FLASH` and `RUN_USER_CODE` are stub-only; writing (including compressed) and `SPI_FLASH_MD5` work in ROM too. So without the stub:
  - Flashing and verification by MD5 work, only slower.
  - "Erase all" and "Erase partition" are done by writing 0xFF (`FLASH_BEGIN` erases the region it covers) — slow, and the program warns about it.
  - Reading flash uses the slow ROM `READ_FLASH_SLOW` command, documented for ESP32; on other chips the program tries it and, if the ROM rejects it, reading is unavailable. Without reading, the Memory and Partitions tabs and backup do not work, and the program says so instead of showing empty views.
  - Writing above 16 MB is tried in ROM as well; if the ROM rejects it or the MD5 check after writing fails, the program stops and says the stub is required.
- A "Without stub loader" mode is in the menu of the "Flash" button.
- With every AltEsp release the stubs are checked against the latest esp-flasher-stub release (new chips and revisions).

**JTAG.** Designed into the architecture from the start, implemented as the last stages. The goal is flashing and reading flash without the UART bootloader; full debugging (breakpoints, stepping) is out of scope.

- From stage 1 the transport is separated from the operations: flashing, reading, erasing and chip info in `ops` go through a common interface at the level of "read / write / erase a flash region, read a register", under which JTAG is later plugged in without reworking the rest of the code.
- Stage 6 — the built-in USB-JTAG of RISC-V chips: ESP32-C3, C6, H2 (standard RISC-V debug module). Stage 7 — Xtensa chips (ESP32-S3, ESP32), which have a different debug module, and external adapters (ESP-Prog on FT2232H).
- Everything inside the program, without OpenOCD and without its code (GPL). Flashing over JTAG calls the flash functions of the chip's ROM (`esp_rom_spiflash_*`, addresses from the ESP-IDF ROM linker scripts): the hart is halted, the arguments set, the return address points at an `ebreak`, and the ROM does the work; the data goes through a RAM buffer, verification uses the ROM's MD5. esp-flasher-stub can't be driven over JTAG: it has no JTAG transport (its transports are UART, USB-OTG, USB-Serial-JTAG CDC and SDIO — `src/transport.c`), so the ROM functions replace it.
- USB access without cgo: Linux — usbfs; Windows — WinUSB via syscalls; macOS — IOKit via purego. Implemented for all three OSes.
- On Windows the JTAG interface needs the WinUSB driver; on Linux a udev rule. The program detects a missing driver or permission and explains how to fix it.
- If JTAG is disabled by eFuse, the program reports it on connect.
- Xtensa (stage 7) is blocked on documentation: the public Xtensa ISA Reference Manual defers the On-Chip Debug protocol (halting the core, executing instructions through DIR0EXEC/DDR, resuming) to the Tensilica On-Chip Debugging Guide, which only Cadence licensees get, and the only open implementation is OpenOCD (GPL, excluded above). Implemented: the TAP chain, the debug module register access (NAR/NDR, power control) and a probe of ESP32/ESP32-S3; halting and running code report "Xtensa JTAG is not available yet" with the missing facts (`internal/jtag/xtensa`, `Missing`). ESP32 and ESP32-S3 are flashed through the serial loader meanwhile.

## Supported chips and adapters

Stage 1 covers ESP32, ESP32-S3 and ESP32-C3 (the most common); other families are added as entries in the chip table. Bootloader addresses are checked against the ESP-IDF "Bootloader" page of each chip (e.g. [ESP32-C5](https://docs.espressif.com/projects/esp-idf/en/latest/esp32c5/api-guides/bootloader.html)); on C5, P4 and H4 the first 8 KB of flash are reserved, so the bootloader sits at 0x2000. Interface details are checked against the TRMs before implementation.

| Chip | Stage | Bootloader address | Built-in USB for flashing |
| --- | --- | --- | --- |
| ESP32 | 1 | 0x1000 | no, USB-UART bridge only |
| ESP32-S3 | 1 | 0x0 | USB-Serial-JTAG |
| ESP32-C3 | 1 | 0x0 | USB-Serial-JTAG |
| ESP32-S2 | 2 | 0x1000 | USB OTG (CDC in ROM) |
| ESP32-C6 | 2 | 0x0 | USB-Serial-JTAG |
| ESP32-H2 | 3 | 0x0 | USB-Serial-JTAG |
| ESP32-C2 | 3 | 0x0 | no |
| ESP32-C5, ESP32-P4 | 5 | 0x2000 | USB-Serial-JTAG |
| ESP32-C61 | 5 | 0x0 | per TRM |
| ESP32-H4 | 5 | 0x2000 | per TRM |
| Other chips supported by esp-flasher-stub | 5 | per ESP-IDF docs | per TRM |
| ESP8266 | not planned | — | — |

**USB adapters.** The program recognizes them by VID/PID and shows a friendly port name: CP210x, CH340/CH343, FTDI, Espressif's built-in USB-Serial-JTAG and USB OTG. VID/PID are read on every OS: Windows — device instance ID, Linux — sysfs, macOS — IORegistry via purego (the device name alone is not enough: with the built-in macOS driver a CH340 shows up as `usbserial-*`). Each type has its own DTR/RTS sequence for entering the bootloader and for reset.

## Connecting to the board

**Two board states.** Chip information, reading and writing work only in the ROM download mode; the monitor needs the application running. Switching between them is a reset of the board, and the program never does it silently:

- "Connect" resets the board into download mode — the application stops. The button says so in its tooltip.
- Opening the Monitor does not reset the board: it shows the log of the running application. The "Reset" button is next to it.
- After flashing, the board is reset into the application (unless turned off), and the monitor, if open, reconnects.

**Port reappearance.** With built-in USB (USB-Serial-JTAG, USB OTG) the port disappears and reappears on every reset, and on Windows its COM number may change. The program tracks the board by VID/PID, serial number and USB location, waits for it to come back (with a timeout), and keeps the selection. ESP32-S2 in download mode appears as a different USB device (different PID) and is reset into the application by the watchdog, not by RTS.

**When automatic entry fails.** If the application itself uses the USB pins (TinyUSB, Arduino "USB CDC On Boot", repurposed GPIOs), is in deep sleep, or the adapter has no DTR/RTS wired, the program shows the hint "hold BOOT and press RESET" and waits for the board.

**Exclusive access.** The port is opened exclusively (on Linux and macOS with `TIOCEXCL`, since otherwise two programs can open it at once and corrupt flashing). If the port is busy, the program says so and, where the OS allows it, names the process holding it.

**Baud rate.** Flashing starts at a fast rate (921600 by default) and drops automatically on errors — CH340 clones are often unstable at high rates. For built-in USB the baud rate does not matter and is not shown; the status bar shows "USB" instead.

**Linux.** Typical obstacles are detected and explained with the exact fix: the user is not in the `dialout` group; ModemManager grabs `ttyACM*` ports (this is how USB-Serial-JTAG appears); brltty grabs CH340. The .deb and .rpm packages install udev rules; the portable binary cannot do this without root and shows the command to run.

## Main window

One window, four zones: port and connection on top, board card on the left, tabs on the right, status bar at the bottom. Values in the sketch are examples.

```
┌──────────────────────────────────────────────────────────────────────────┐
│ [Port: COM5 · CP2102 ▾]  [Connect]                          [Tools ▾]    │
├──────────────────────────────────────────────────────────────────────────┤
│ ┌───────────────────────┐ [ Flash ] [ Memory ] [ Partitions ] [ Monitor ]│
│ │ ESP32-S3 (rev 0.2)    │                                                │
│ │ MAC 7C:DF:A1:00:12:34 │   File                  Address    Size        │
│ │ Flash 8MB · PSRAM 8MB │   ──────────────────────────────────────────── │
│ │ Crystal 40 MHz        │   bootloader.bin        0x0        21 KB       │
│ │ Encryption: off       │   partition-table.bin   0x8000     3 KB        │
│ │                       │   sensor-node.bin       0x10000    1.2 MB      │
│ │ On board:             │   + Drop files or the build folder here        │
│ │   sensor-node 1.4.2   │                                                │
│ └───────────────────────┘   [x] Verify after writing   ┌───────────┬─┐   │
│ ┌───────────────────────┐   [x] Reset after writing    │   Flash   │▾│   │
│ │ Recent projects       │                              └───────────┴─┘   │
│ │   sensor-node/build   │   [░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░]   │
│ │   gateway/build       │   Ready to flash · 3 files, 1.2 MB             │
│ └───────────────────────┘                                                │
├──────────────────────────────────────────────────────────────────────────┤
│ COM5 · 921600 baud · stub loader                                 Install │
└──────────────────────────────────────────────────────────────────────────┘
```

Outside — the port, the board and "Flash"; everything else is a right click away. Right click on the card — eFuse, security, report; on a file — address, contents, hex. The arrow next to "Flash" — write modes; "Tools" — eFuse, partition table editor, images, coredump.

Basic workflow:

1. The user plugs in a board. The program notices the new port and selects it itself (if it is the only one).
2. "Connect" (or automatically on the first action that needs download mode: flashing, reading, erasing) — the board is reset into download mode and the card fills in: chip, MAC, flash, PSRAM, protection status and what is currently flashed.
3. The user drags in files or a build folder — addresses are filled in automatically.
4. "Flash" — progress, verification, reset into the application, and optionally an automatic switch to "Monitor".

A board without firmware to boot is told in the card, in place of "On board:", as information rather than an error: "No firmware" when the bootloader's sector is erased (a new or erased board), "Incomplete firmware" when there is a bootloader but no partition table, or a partition table with no application. The card says what the board does then: without AltEsp connected it restarts over and over (the ROM finds no image and the watchdog resets the chip) — with built-in USB its port keeps disappearing and coming back, through a bridge the monitor repeats "invalid header". The erased sector is told by its MD5, which the ROM, the stub and JTAG all compute, so it works without the stub too. Nothing is told when the flash is encrypted or in Secure Download Mode. The state is checked again after flashing and erasing; the command line prints it as "Firmware:", the JSON as `firmware`.

The board card is visible on all tabs. Without a connected board the tabs are still available for working with files: a flash or partition image can be opened from disk and viewed exactly as if it came from a board.

## Features by tab

Four tabs, each solving one task. Only the basic actions are in plain sight; the rest is in context menus (next section).

### 1. Flash

- List of files to write: name, address, size, type. Files are added by drag and drop or with the "Add" button.
- Type is detected by content, not by file name. Bootloader and application images both start with 0xE9; an application is told by the `esp_app_desc` structure (magic 0xABCD5432) and by its segment load addresses. Also recognized: partition table, merged image, NVS and FS images.
- Address is taken, in order of priority: from the build folder description; for a single application image — from the partition table read from the board (the `factory` or first OTA partition), not a fixed 0x10000; for the bootloader — from the chip table. The partition table offset is configurable in ESP-IDF (`CONFIG_PARTITION_TABLE_OFFSET`) and is never assumed to be 0x8000: on the board it is found by scanning the area after the bootloader.
- Build folders are recognized by their layout:
  - ESP-IDF: `build/flasher_args.json` — all files and addresses.
  - PlatformIO: `.pio/build/<env>/` with `firmware.bin`, `bootloader.bin`, `partitions.bin` and, for Arduino, `boot_app0.bin`; addresses by chip and framework conventions.
  - Arduino IDE: an exported merged image, or the separate `.bin` files from the sketch build folder.
- One big "Flash" button. Checkboxes next to it: "Verify after writing" (on by default), "Reset board after writing" (on).
- An "Erase all" button.
- Progress for each file and overall speed.
- Flash mode, frequency and size override means patching the bootloader image header and recalculating its appended SHA256. A bootloader signed for Secure Boot cannot be patched: the override is refused with an explanation. Verification and "Flash only changed blocks" compare against the image as actually written, after patching.

### 2. Memory (flash)

- Hex view of the whole flash, loaded as you scroll: only the visible and neighboring blocks are read from the board, read blocks are cached.
- On the left — a flash map as a strip: colored regions per partition, clicking jumps to the address.
- A "Save flash to file" button (whole size or a region).
- A "Compare with file" button: differing blocks are highlighted.
- Byte editing is off by default; it is enabled via the context menu.
- Needs the stub: without it (see "Stub loader") the tab explains why it is unavailable.

### 3. Partitions

- The partition table in readable form: name, type, subtype, address, size, usage.
- For application partitions — data from the image header: project name, version, IDF version, build date, SHA256; the active OTA slot is marked.
- Double click on a partition opens the matching viewer: application → details, NVS → key editor, SPIFFS/LittleFS/FAT → file browser, anything else → hex on the Memory tab.
- **NVS editor.** Editing rebuilds pages per the NVS format: entry and page states, CRC32, multi-page blobs (blob index + data chunks). Encrypted NVS is decrypted with the keys from the `nvs_keys` partition (when it can be read in plaintext) or from a key file the user points to; edits are encrypted back. Without keys it is shown as encrypted and is read-only.
- **File systems.** All three are implemented in pure Go from their format specifications; each is a separate piece of work.
  - LittleFS: parameters from the superblock; disk versions 2.0 and 2.1. Our own implementation, written from the [littlefs specification](https://github.com/littlefs-project/littlefs/blob/master/SPEC.md), not a port of existing code. Compatibility is tested against images made by ESP-IDF and Arduino tools, both reading theirs and having them read ours.
  - SPIFFS: parameters (page size, block size, object name length, metadata length) are not stored in the image — they are guessed from the common ESP-IDF and Arduino defaults and can be set manually in the context menu.
  - FAT: on top of the ESP-IDF wear-levelling layer (implemented too), and plain FAT without it.

### 4. Monitor

- Port terminal: baud rate, line ending, send a line, "Reset" and "Bootloader" buttons. Opening the tab does not reset the board.
- Color highlighting of ESP-IDF log levels (E/W/I/D/V).
- If an .elf is specified (or found next to the flashed application), panics are decoded into "function — file:line":
  - Xtensa (ESP32, S2, S3): addresses from the `Backtrace:` line.
  - RISC-V (C-, H-, P-series): ESP-IDF prints no backtrace, only registers and a stack dump; the program unwinds the stack itself using the DWARF call frame information (`.eh_frame` / `.debug_frame`) from the .elf.
  - The ELF/DWARF decoder and the stack unwinder for both architectures (including Xtensa register windows) are built in; the same unwinder is used for coredump.
- Pause, search, clear, save log to file.

The monitor and flashing share one port: before flashing the monitor releases the port by itself, and reconnects afterwards (see "Port reappearance").

## Advanced features via context menus

Rule: an advanced feature lives where the object it relates to is. A right click on a file, partition, byte or log line gives everything that can be done with it. The "Tools" menu in the main menu is for things not tied to a specific object.

| Where to right click | What is in the menu |
| --- | --- |
| Chip card | Copy MAC / all info · Save report · Show eFuse · Security status (Flash Encryption, Secure Boot, Secure Download Mode) · Manual reset / enter bootloader |
| File in the flash list | Change address · Skip file · Show contents (image header / partition table) · Open in hex · Remove from list |
| "Flash" button (arrow next to it) | Flash only changed blocks · No compression · Port baud rate · Flash mode and frequency (override) · Without stub loader · Don't reset after writing |
| Byte or selection in Memory | Go to address · Save selection to file · Write file here · Erase sector(s) · Enable editing · Find bytes / string · Copy as hex / C array |
| Row in the partition table | Save partition to file · Write file to partition · Erase partition · Make OTA slot active · Open in hex · Edit partition table… · Export / import CSV |
| Key in the NVS editor | Change value · Add key / namespace · Delete · Export namespace to CSV · Save NVS image to file |
| File in the FS browser | Download · Replace · Rename · Delete · Add files / folder · Save FS image · FS parameters (SPIFFS) |
| Log line in the monitor | Copy · Decode address · Show in hex (for addresses in flash) · Filter by tag / level |
| Port icon in the status bar | Reset sequence (auto / bridge / USB-JTAG / manual) · Reconnect · Adapter properties (VID/PID, serial number, USB location) |

**"Tools" menu:**

- **eFuse** — a table of all fields with decoding; writing in a separate mode (see "Protection").
- **Partition table editor** — visual, with overlap and alignment checks, generates the binary.
- **Image generator** — NVS from CSV, LittleFS/SPIFFS/FAT from a folder, merged image from a set of files.
- **Coredump analysis** — from the coredump partition on the board or from a file, linked to an .elf; a backtrace for every task via the built-in unwinder.

**Hotkeys** duplicate the main actions: flash, read flash, reset, switch tabs, go to address.

## Protection against dangerous operations

Operations fall into three levels. The higher the level, the further it is hidden and the more confirmations it needs.

| Level | Examples | Protection |
| --- | --- | --- |
| Normal | Flashing, reading, reset, monitor | No confirmations |
| Erasing | Erase all, erase partition, writing over the partition table | Confirmation dialog listing what will be lost; offer to make a backup first |
| Irreversible | Burning eFuses, enabling Flash Encryption or Secure Boot, disabling UART download | Only via "Tools" → eFuse; separate write mode; final list of changes; typing a confirmation word (e.g. BURN); operation log |

**eFuse.** Field layouts are per chip and revision, taken from ESP-IDF `esp_efuse_table.csv` (Apache-2.0) and the chip TRMs, not from espefuse. Writing follows the TRM: the block encoding scheme (3/4 coding or none on ESP32, Reed-Solomon on newer chips), write- and read-protection bits, all fields of one block in a single burn, read-back check afterwards. Writing is covered by tests against a model of the eFuse blocks; the check on real boards is listed in "Hardware checks".

**Checks before flashing.** The program checks that the image is built for the connected chip (chip ID and the minimum/maximum chip revision from the extended image header), and that the files do not overlap and fit into flash. On mismatch — a warning, not a silent write.

**Protected chips.** Reported on connect, with what is and is not possible:

- Secure Download Mode: the stub cannot be loaded and flash cannot be read; only the limited ROM command set is available. Reading, Memory, Partitions and backup are unavailable.
- Flash Encryption, development mode: plaintext images are written with on-chip encryption; reads return ciphertext.
- Flash Encryption, release mode: reads return ciphertext, so the partition table and "what is flashed" cannot be shown; writing an unencrypted image where the bootloader will not accept it is refused.
- A backup of an encrypted flash can only be restored onto the same chip; the program warns about it when saving.

**Stop.** Any operation can be interrupted with the "Stop" button; after interruption the program explains what state the board was left in.

## Command-line mode

The same binary opens the window when run without arguments, and works in the console with arguments. This is needed for scripts and CI. The command set mirrors the UI actions:

```
altesp ports
altesp info   --port COM5
altesp flash  --port COM5 build/
altesp flash  --port COM5 0x0 merged.bin
altesp read   --port COM5 0x0 4MB backup.bin
altesp erase  --port COM5
altesp monitor --port COM5 --elf build/app.elf
```

- The port can be omitted if exactly one board is connected.
- `monitor` does not reset the board unless `--reset` is given.
- Exit codes and machine-readable output (`--json`) for scripts.
- Irreversible operations are not performed from the command line without an explicit confirmation flag.
- A local API/MCP server with the same command set, off by default, localhost only.

## Settings, data storage, languages

- All settings and history are in `~/.altbins/.altesp` (Windows: `%USERPROFILE%\.altbins\.altesp`). Deleting the folder resets the program.
- Remembered: last port, recent file sets for flashing (per project folder), .elf path for each project, monitor baud rate, window size.
- There is almost no settings window as such: parameters are changed where they are used (context menus). "Settings" holds only language, theme, default port baud rate.
- The log of dangerous operations (erase, eFuse) is stored locally, with date, board MAC and list of changes.
- Light and dark theme, languages as in the whole series (up to 31). Technical terms (eFuse, NVS, OTA) are not translated.

## Code structure inside the application

One repository, one Go module, one binary. The top level follows the layout of the other AltBins utilities (AltSerialPort as the model); the ESP-specific code lives in `internal/` — those packages are not visible or used from outside.

```
altesp/
  main.go          — mode selection: window or command line
  uninstall.go     — removal of the installed program
  icon.png, icon.svg
  app/             — name, version, website
  config/          — settings in ~/.altbins/.altesp
  install/         — installation from the Install link
  forms/           — window, tabs, context menus, strings
  cli/             — console mode commands, local API/MCP
  appicon/, cmd/mkicon/  — the program icon
  build/, scripts/ — builds and packages
  internal/
    serial/        — ports, VID/PID on every OS, DTR/RTS,
                     exclusive open, tracking ports that reappear
    loader/        — SLIP, ROM loader and stub commands, compression,
                     reset sequences, a simulated loader for tests
    jtag/          — USB access and debug modules (stages 6–7)
    chips/         — chip table: detection, addresses, eFuse layouts
    formats/       — partition table, app image, NVS,
                     SPIFFS/LittleFS/FAT + wear levelling,
                     ELF/DWARF, stack unwinder, coredump
    ops/           — flashing, reading, comparison, eFuse
```

Dependency rule: `forms` and `cli` call only `ops` (plus `app`, `config`); `ops` uses `loader`, `jtag`, `chips`, `formats`; `loader` knows only `serial`. A new chip is an entry in `chips`, not edits all over the code.

## Development stages

Development is split into stages. These are stages of work, not release versions: release numbering is decided separately and does not have to match them. Each stage ends with a complete, useful product; the next one only adds. Everything described in this specification is implemented in full, including the cases that cannot be checked right away; nothing is cut back while waiting for hardware.

A stage is done when it is implemented and covered by automated tests: images and files from ESP-IDF, PlatformIO and Arduino builds, recorded loader exchanges, a simulated loader on the other end of the port. The criteria in the table are what the check on real boards confirms; that check is done separately, as boards become available, and does not block the next stage (see "Hardware checks").

Stage 1 starts with the connection layer, since everything else depends on it: VID/PID on every OS, port reappearance after reset with built-in USB, exclusive open, Linux permissions and ModemManager/brltty.

| Stage | What is included | Done criterion |
| --- | --- | --- |
| Stage 1 | Ports, connection, port tracking, platform diagnostics, chip card; flashing multiple files and a build folder (ESP-IDF, PlatformIO, Arduino); erase; reading flash to file; monitor; ESP32, S3, C3 | A newcomer flashes a board from scratch in 3 actions on Windows, macOS and Linux, via a bridge and via built-in USB |
| Stage 2 | Flash hex view, flash map, comparison and verification; "Partitions" tab, image details, OTA; detection of protected chips; S2, C6 | Any unprotected board can be "read" and you can understand what is flashed on it; a protected one is reported as such |
| Stage 3 | NVS editor; LittleFS, SPIFFS, FAT browser; backtrace decoder via .elf for Xtensa and RISC-V; H2, C2 | A setting or a file can be changed without reflashing; a panic is decoded on every declared chip |
| Stage 4 | eFuse (reading and writing), security status, encrypted NVS, coredump, image generators, partition table editor | All irreversible operations are protected as described in "Protection" |
| Stage 5 | CLI with `--json` and exit codes for every command; local API/MCP server; C5, P4, C61, H4 and the other chips supported by esp-flasher-stub | Every UI action of stages 1–4 runs from a script or through the API with machine-readable output; the new chips meet the stage 1 criterion |
| Stage 6 | Flashing and reading flash via built-in USB-JTAG of RISC-V chips (C3, C6, H2) | A board is flashed and read over USB-JTAG without the UART bootloader on Windows, macOS and Linux |
| Stage 7 | JTAG for Xtensa chips (S3, then ESP32) and external adapters (ESP-Prog) | Same as stage 6 for S3 and ESP32; ESP-Prog flashes C3/C6/H2. Xtensa is blocked on documentation (see JTAG above) |

## Hardware checks

Behavior that documentation does not settle is implemented so that the program finds out on the board itself and falls back gracefully. These points are then confirmed on real boards. The read-only tests in `internal/loader/hw_test.go` run against a board when `ALTESP_HW_PORT` and `ALTESP_HW_STUB` are set; `ALTESP_TRACE=1` prints every packet.

**Checked on an ESP32 v3.1 dev board (CP2102 bridge, Linux, ESP-AT firmware built with ESP-IDF v3.0.3), Nov 2, 2026:** bridge reset into download mode and into the application; ROM status of 4 bytes; chip detection by the magic word 0x00F01D83; MAC, revision, crystal and flash JEDEC ID through registers; esp-flasher-stub v1.4.1 start, baud change to 921600; partition table found; an application without a description (built before ESP-IDF v3.3) recognized. Through the stub: full 4 MB read (56 s) with its MD5; compressed writes verified by MD5 and read back; "only changed" skipping; region erase; whole flash erase (11 s); restore of the 4 MB backup (22 s) after which the board booted. Through the ROM loader alone: compressed write with the MD5 as hex text, region erase by FLASH_BEGIN, the slow READ_FLASH_SLOW read. The monitor showed the boot log after a reset. NVS: a key added to the board's version 1 NVS (written back as version 1) was read by the firmware, which booted normally (`internal/ops/hw_test.go`); the original partition was then restored. The mapping of the app's addresses to the flash ("Show in hex") matched the segment log of the board's bootloader for all four segments. eFuse reading: see the eFuse item below. Found there: after a baud rate change, READ_FLASH with 4 KB packets stalled after a few packets until another frame came (the stub didn't see an acknowledgement); with 1 KB packets it didn't. Reading uses 1 KB packets and resumes a stalled read; the cause is not found yet.

**Checked again on the same board, Nov 3, 2026:** the loader read tests at 115200 and 921600; the NVS edit, the eFuse read and the address mapping (all seven segments); the command line on the board: `ports`, `info` (with and without the stub, text and `--json`), `efuse`, `efuse burn` without `--confirm` (only the plan shown, nothing burned), `coredump` (no partition reported), write, `--changed`, read and region erase in a free part of the flash through the stub and through the ROM; the MCP server and the HTTP API (board info, reading a partition, flashing with "only changed", the monitor, a reset; a foreign `Origin` refused with 403). An ESP32-C3 v0.4 with an erased flash (USB-Serial-JTAG): the restart loop and the USB port coming and going every 2–3 s, "No firmware" told with the stub and with the ROM alone. Found and fixed there: opening the monitor restarted the application on Linux (the port is opened with DTR and RTS raised, and releasing DTR first left RTS alone, which pulls EN low; RTS is now released first); `--changed` reported skipped files as flashed (the CLI and MCP now list them as unchanged); once, an MCP call failed with a lost READ_REG answer while the chip was being identified, not reproduced in later runs (the connection is now retried once in that case). Once, the first monitor run after the tests showed about 80 resets in a row in the ROM's first line; not reproduced.

- [ ] Connection on Windows and macOS; on Linux with CH340, FTDI, USB-Serial-JTAG, USB OTG: VID/PID, port reappearance after reset, reset sequences.
- [ ] The READ_FLASH stall: the cause, other chips and adapters, whether resuming is ever needed with 1 KB packets.
- [ ] "Incomplete firmware" (a bootloader without a partition table, a table without an application) on a real board, and no notice on a working board after the firmware check was added.
- [ ] The monitor opening without a reset on Windows and macOS (DTR and RTS on open), and the burst of resets seen once on Linux.
- [ ] ROM loader without stub: `READ_FLASH_SLOW` on chips other than ESP32 (works on the ESP32); writing above 16 MB (needs a board with 32 MB flash).
- [ ] Assumptions of the simulated board used by the tests (`internal/loader/sim.go`): the length of the ROM status on chips other than ESP32, the ROM's error code for an unknown command, the timings of the bridge reset (EN held 100 ms or 500 ms, GPIO0 held 50 ms).
- [ ] Chip table (`internal/chips`), marked `unverified:` in the code: the magic word of the ESP32-S2 (0x000007C6); the bit positions of the GET_SECURITY_INFO flags; the SPI user-command sequence for the flash ID on chips other than ESP32; JEDEC capacity codes 0x20–0x22; in-package memory of ESP32-PICO-D4 and ESP32-PICO-V3-02.
- [ ] Formats (`internal/formats`): finding `esp_bootloader_desc_t`; detecting signed images (Secure Boot v1 and v2 signature blocks); otadata entries with `ota_seq` 0; FAT under wear levelling; SPIFFS other than 256-byte pages and 4 KB blocks; real Arduino IDE 2 and PlatformIO (pioarduino, P4/C5) build folders.
- [ ] NVS (`internal/formats/nvs`): version 2 images and encrypted images built here, read by firmware; CSV compatibility with nvs_partition_gen (relative paths, spaces, float/double); the blob split thresholds.
- [ ] LittleFS (`internal/formats/littlefs`; checked both ways against mklittlefs 0.2.3, disk version 2.0): disk version 2.1 and its FCRC against a 2.1 littlefs; the board's littlefs appending to our commits; inline files up to 128 bytes vs the board's cache; NameMax 64 vs builds with other limits; images written by a board after superblock expansion, relocations, power loss; images from ESP-IDF's littlefs generator.
- [ ] SPIFFS and FAT (`internal/formats/spiffs`, `fatfs`; checked both ways against mkspiffs and mkfatfs): images mounted and written by a chip; the wear-levelling layout against a real partition whose dummy sector has moved, and against fatfsgen.py / wl_fatfsgen.py / fatfsparse.py (`IDF_PATH=... go test`); SPIFFS with metadata 4 and aligned index tables; other page/block sizes; FAT long names on builds without LFN; recovery after power loss; code page of 8.3 names; safe mode sizes.
- [ ] Panics and coredumps (`internal/formats/elfdbg`; checked with real Xtensa and RISC-V ELFs and synthetic dumps): a real panic and a real coredump from a board; coredump format versions of ESP-IDF v5.x; binary format details; Xtensa PRSTATUS with a non-zero window base; TCB layouts without DWARF; P4 dual-core dumps and frame-pointer backtraces of ESP-IDF v5.3+. Settled from the ESP-IDF source: the PCs printed on the Xtensa "Backtrace:" line are already adjusted (esp_cpu_process_stack_pc).
- [ ] Flashing (writing) on real boards of every declared chip but the ESP32, with verification.
- [ ] The baud rate fallback (checked only against a simulated adapter that loses data above 460800) on a CH340 clone that is unstable at 921600.
- [ ] Encrypted writing (Flash Encryption in development mode) through the stub and the ROM (the fifth FLASH_BEGIN word), and the release-mode detection by DIS_DOWNLOAD_MANUAL_ENCRYPT / DISABLE_DL_ENCRYPT (needs a board that can be sacrificed).
- [ ] ESP-Prog (`internal/jtag/ftdi`; checked only against its simulator): the FTDI vendor requests on real silicon, the MPSSE bit order for partial bytes, the interface class FF/FF/FF, WinUSB on interface 0 under the composite parent on Windows, the macOS FTDI driver holding the channel, throughput at 5 MHz, pad JTAG of C3/C6/H2 once routed (eFuse/strapping). Steps: `ALTESP_HW_JTAG=1 ALTESP_HW_JTAG_ADAPTER=esp-prog ALTESP_HW_JTAG_CHIP=esp32c3 go test -v -run HW ./internal/jtag/`.
- [ ] Xtensa probe on ESP32 (ESP-Prog) and ESP32-S3 (USB-JTAG): the IDCODE (0x120034E5 assumed), the number and order of TAPs, NAR/NDR semantics, PWRCTL/PWRSTAT behaviour: `... ALTESP_HW_JTAG_CHIP=esp32 go test -v -run HWXtensaProbe ./internal/jtag/`.
- [ ] eFuse writing on every declared chip (needs boards that can be sacrificed). Reading is checked on the ESP32 above (MAC, revision, package, no coding errors). Before any burn on a chip after the ESP32, check first: the Reed-Solomon (44,32) parity of BLOCK1 and up (generator roots α^0..α^11, data byte 0 as the highest coefficient, parity order in PGM_CHECK_VALUE0–2 are assumptions; the program warns when a plan writes such a block); the APB clock under the ROM loader for the ESP32/S2 burn timing; the ESP32-S2 burn sequence (IDF uses ROM functions there); the eFuse model's assumptions (`internal/chips/efuse/model.go`); the 9 reserved BLOCK0 bits of the ESP32-C3.
- [ ] Flash encryption and Secure Download Mode handling (needs boards that can be sacrificed).
- [ ] JTAG (`internal/jtag`; every layer above USB checked only against its simulator, the USB layer only compiled): USB access on all three OSes (Linux usbfs + udev rule, Windows WinUSB via Zadig on the JTAG interface, macOS IOUSBInterfaceInterface function-table indexes and UUIDs through purego); the Espressif JTAG command protocol details (nibble order, TDO bit packing and sampling, the controller's buffer size; REP not used until its encoding is confirmed); IDCODEs of C3/C6/H2; ndmreset keeping the force-download bit and whether it re-enumerates the USB device; the RAM work areas and the ROM functions run with our stack and interrupts off; which memory path each chip's debug module offers; the watchdog disable sequence; throughput (about 1–2 s per 64 KB expected). Steps: `ALTESP_HW_JTAG=1 ALTESP_HW_JTAG_CHIP=esp32c3 go test -v -run HW ./internal/jtag/`, with `ALTESP_HW_JTAG_SCRATCH=0x3F0000` for the write test.
