package loader

import (
	"encoding/binary"
	"fmt"
)

// The commands of the serial protocol of the ROM loader and the stub
// (esptool documentation "Serial Protocol"; esp-flasher-stub
// src/commands.h, MIT)
const (
	opFlashBegin      byte = 0x02
	opFlashData       byte = 0x03
	opFlashEnd        byte = 0x04
	opMemBegin        byte = 0x05
	opMemEnd          byte = 0x06
	opMemData         byte = 0x07
	opSync            byte = 0x08
	opWriteReg        byte = 0x09
	opReadReg         byte = 0x0A
	opSPISetParams    byte = 0x0B
	opSPIAttach       byte = 0x0D
	opReadFlashSlow   byte = 0x0E // ROM only; not in the protocol documentation
	opChangeBaud      byte = 0x0F
	opFlashDeflBegin  byte = 0x10
	opFlashDeflData   byte = 0x11
	opFlashDeflEnd    byte = 0x12
	opSPIFlashMD5     byte = 0x13
	opGetSecurityInfo byte = 0x14
	opEraseFlash      byte = 0xD0 // stub only
	opEraseRegion     byte = 0xD1 // stub only
	opReadFlash       byte = 0xD2 // stub only
	opRunUserCode     byte = 0xD3 // stub only
)

var opNames = map[byte]string{
	opFlashBegin: "FLASH_BEGIN", opFlashData: "FLASH_DATA", opFlashEnd: "FLASH_END",
	opMemBegin: "MEM_BEGIN", opMemEnd: "MEM_END", opMemData: "MEM_DATA",
	opSync: "SYNC", opWriteReg: "WRITE_REG", opReadReg: "READ_REG",
	opSPISetParams: "SPI_SET_PARAMS", opSPIAttach: "SPI_ATTACH",
	opReadFlashSlow: "READ_FLASH_SLOW", opChangeBaud: "CHANGE_BAUDRATE",
	opFlashDeflBegin: "FLASH_DEFL_BEGIN", opFlashDeflData: "FLASH_DEFL_DATA",
	opFlashDeflEnd: "FLASH_DEFL_END", opSPIFlashMD5: "SPI_FLASH_MD5",
	opGetSecurityInfo: "GET_SECURITY_INFO", opEraseFlash: "ERASE_FLASH",
	opEraseRegion: "ERASE_REGION", opReadFlash: "READ_FLASH", opRunUserCode: "RUN_USER_CODE",
}

func opName(op byte) string {
	if n, ok := opNames[op]; ok {
		return n
	}
	return fmt.Sprintf("0x%02X", op)
}

const (
	dirRequest  = 0x00
	dirResponse = 0x01
	headerSize  = 8
	// checksumSeed starts the XOR checksum of the data of the _DATA commands
	checksumSeed = 0xEF
)

// request builds a command packet: direction, command, size, checksum, data
func request(op byte, data []byte, checksum uint32) []byte {
	p := make([]byte, headerSize+len(data))
	p[0] = dirRequest
	p[1] = op
	binary.LittleEndian.PutUint16(p[2:], uint16(len(data)))
	binary.LittleEndian.PutUint32(p[4:], checksum)
	copy(p[headerSize:], data)
	return p
}

// checksum is the XOR of the bytes, seeded with 0xEF
func checksum(data []byte) uint32 {
	c := byte(checksumSeed)
	for _, b := range data {
		c ^= b
	}
	return uint32(c)
}

// words packs 32-bit little-endian words
func words(w ...uint32) []byte {
	b := make([]byte, 4*len(w))
	for i, v := range w {
		binary.LittleEndian.PutUint32(b[4*i:], v)
	}
	return b
}

// response is a parsed response packet
type response struct {
	op    byte
	value uint32
	data  []byte // the data without the status bytes
	// status and code are the status bytes: 0, 0 is success
	status, code byte
}

// parseResponse splits a response packet; statusLen is 2 for the stub and
// 4 for the ROM loaders of the ESP32 family (only the first two of them
// carry the status)
func parseResponse(p []byte, statusLen int) (response, bool) {
	if len(p) < headerSize || p[0] != dirResponse {
		return response{}, false
	}
	size := int(binary.LittleEndian.Uint16(p[2:]))
	data := p[headerSize:]
	if size > len(data) {
		return response{}, false
	}
	data = data[:size]
	r := response{op: p[1], value: binary.LittleEndian.Uint32(p[4:])}
	if len(data) < statusLen {
		// A short status: the ROM of some chips sends two bytes
		statusLen = 2
	}
	if len(data) < statusLen {
		return response{}, false
	}
	st := data[len(data)-statusLen:]
	r.status, r.code = st[0], st[1]
	r.data = data[:len(data)-statusLen]
	return r, true
}

// Error is a command that the loader answered with a failure
type Error struct {
	Op           byte
	Status, Code byte
	Stub         bool
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s failed: %s", opName(e.Op), e.Reason())
}

// Reason tells the error in words
func (e *Error) Reason() string {
	if e.Stub {
		// The stub sends its 16-bit code big-endian: 0xC100 is C1 00
		if s, ok := stubErrors[uint16(e.Status)<<8|uint16(e.Code)]; ok {
			return s
		}
		return fmt.Sprintf("stub error 0x%02X%02X", e.Status, e.Code)
	}
	if s, ok := romErrors[e.Code]; ok {
		return s
	}
	return fmt.Sprintf("ROM error 0x%02X", e.Code)
}

// Unsupported tells whether the loader doesn't know the command
func (e *Error) Unsupported() bool {
	if e.Stub {
		return e.Status == 0xFF || e.Status == 0xC3
	}
	return e.Code == 0x05 || e.Code == 0x01
}

// romErrors are the error codes of the ROM loader (protocol documentation)
var romErrors = map[byte]string{
	0x00: "undefined error",
	0x01: "invalid input parameter",
	0x02: "failed to allocate memory",
	0x03: "failed to send message",
	0x04: "failed to receive message",
	0x05: "invalid message format",
	0x06: "wrong result",
	0x07: "checksum error",
	0x08: "flash write error",
	0x09: "flash read error",
	0x0a: "flash read length error",
	0x0b: "deflate failed",
	0x0c: "deflate Adler32 error",
	0x0d: "deflate parameter error",
	0x0e: "invalid RAM binary size",
	0x0f: "invalid RAM binary address",
	0x64: "invalid parameter",
	0x65: "invalid format",
	0x66: "description too long",
	0x67: "bad encoding description",
	0x69: "insufficient storage",
}

// stubErrors are the response codes of esp-flasher-stub (src/commands.h)
var stubErrors = map[uint16]string{
	0xC000: "bad data length",
	0xC100: "bad data checksum",
	0xC200: "bad block size",
	0xC300: "invalid command",
	0xC400: "SPI operation failed",
	0xC500: "SPI unlock failed",
	0xC600: "not in flash mode",
	0xC700: "inflate error",
	0xC800: "not enough data",
	0xC900: "too much data",
	0xCA00: "NAND program failed",
	0xCB00: "NAND erase failed",
	0xFF00: "command not implemented",
}
