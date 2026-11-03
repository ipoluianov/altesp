package ops

import (
	"bytes"
	"context"
	"crypto/md5"
)

// Firmware is what the flash tells about the firmware on it
type Firmware int

const (
	// FirmwareUnknown: not told — the flash can't be read, is encrypted,
	// or the chip is in Secure Download Mode
	FirmwareUnknown Firmware = iota
	// FirmwarePresent: a bootloader, a partition table and an application
	FirmwarePresent
	// FirmwareEmpty: the place of the bootloader is erased — a new or
	// erased board. The ROM finds nothing to boot ("invalid header:
	// 0xffffffff") and the watchdog restarts the chip over and over.
	FirmwareEmpty
	// FirmwareNoTable: a bootloader, but no partition table
	FirmwareNoTable
	// FirmwareNoApp: a partition table with no application in it
	FirmwareNoApp
)

// Text is the state in words, "" when there is firmware or it is unknown
func (f Firmware) Text() string {
	switch f {
	case FirmwareEmpty:
		return "none: the flash is empty"
	case FirmwareNoTable:
		return "incomplete: a bootloader without a partition table"
	case FirmwareNoApp:
		return "incomplete: no application in the partitions"
	}
	return ""
}

// erasedMD5 is the MD5 of an erased sector
var erasedMD5 = md5.Sum(bytes.Repeat([]byte{0xFF}, sector))

// checkFirmware tells the firmware on the board. The bootloader's sector
// is told erased by its MD5, which the ROM, the stub and JTAG all compute:
// it works where the flash can't be read.
func (s *Session) checkFirmware(ctx context.Context) {
	b := &s.board
	b.Firmware = FirmwareUnknown
	if b.Chip == nil || b.SecureDownload || b.Security.FlashEncryption {
		return
	}
	sum, err := s.t.FlashMD5(ctx, b.Chip.BootloaderAddr, sector)
	switch {
	case err != nil:
		return
	case sum == erasedMD5:
		b.Firmware = FirmwareEmpty
		return
	}
	switch {
	case b.Partitions == nil:
		// Only when the start of the flash was searched: the ROM reads
		// the usual place alone, and the table may be elsewhere
		if b.tableScanned {
			b.Firmware = FirmwareNoTable
		}
	case len(b.Apps) == 0 && b.appsRead:
		b.Firmware = FirmwareNoApp
	case len(b.Partitions) > 0 && b.Apps != nil:
		b.Firmware = FirmwarePresent
	}
}
