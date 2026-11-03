package ops

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"path"
	"sort"
	"strings"

	"github.com/ipoluianov/altesp/internal/chips"
	"github.com/ipoluianov/altesp/internal/chips/efuse"
	"github.com/ipoluianov/altesp/internal/formats"
	"github.com/ipoluianov/altesp/internal/formats/elfdbg"
	"github.com/ipoluianov/altesp/internal/formats/littlefs"
	"github.com/ipoluianov/altesp/internal/formats/nvs"
	"github.com/ipoluianov/altesp/internal/jtag"
	"github.com/ipoluianov/altesp/internal/serial"
)

// What the window's NVS editor, panic decoding and tests need besides the
// operations on the board

// NVSType is the type of an NVS value
type NVSType = nvs.Type

// NVSTypes are the types a key can have, as the NVS editor offers them
var NVSTypes = []NVSType{nvs.U8, nvs.I8, nvs.U16, nvs.I16, nvs.U32, nvs.I32, nvs.U64, nvs.I64,
	nvs.String, nvs.Blob, nvs.Float, nvs.Double}

// NVS types the editor treats apart
const (
	NVSString = nvs.String
	NVSBlob   = nvs.Blob
	NVSFloat  = nvs.Float
	NVSDouble = nvs.Double
)

// ParseNVSType reads a type name: "u8", "string", "blob"...
func ParseNVSType(s string) (NVSType, error) { return nvs.ParseType(s) }

// ParseNVS parses an NVS partition image; an encrypted one comes back with
// Encrypted set and no entries
func ParseNVS(data []byte) (*NVS, error) { return nvs.Parse(data) }

// ParseNVSWithKeys parses an encrypted NVS with the content of an nvs_keys
// partition or of a key file
func ParseNVSWithKeys(data, keys []byte) (*NVS, error) {
	k, err := nvs.ParseKeys(keys)
	if err != nil {
		return nil, err
	}
	return nvs.ParseEncrypted(data, k)
}

// ReadNVSCSV reads entries from a CSV of nvs_partition_gen; the files it
// names are found in dir
func ReadNVSCSV(r io.Reader, dir string) ([]NVSEntry, error) { return nvs.ReadCSV(r, dir) }

// NVSValueSize is the size of the value in bytes: a string with its NUL
func NVSValueSize(e NVSEntry) int {
	switch v := e.Value.(type) {
	case string:
		return len(v) + 1
	case []byte:
		return len(v)
	}
	return int(e.Type & 0x0F)
}

// Lookup tells where an address is in the .elf: "function — file:line",
// with the inlined calls
func (p *PanicDecoder) Lookup(addr uint32) []string {
	return elfdbg.FormatFrames(p.elf.Lookup(addr))
}

// Symbol is the address of a symbol of the .elf
func (p *PanicDecoder) Symbol(name string) (uint32, bool) {
	addr, _, ok := p.elf.Symbol(name)
	return addr, ok
}

// SimOptions changes the board of SimulateBoardWith
type SimOptions struct {
	// AppLog is printed after the usual boot log
	AppLog string
	// NVS fills the nvs partition (0x9000, 24 KB)
	NVS []NVSEntry
	// Files fill a LittleFS partition "storage" (0x110000, 256 KB); a
	// name with "/" puts the file in folders
	Files map[string][]byte
	// EFuse puts a model of blank eFuses into the register space: they
	// can be read and burned
	EFuse bool
	// FlashEncryption reports flash encryption on (development mode):
	// FLASH_CRYPT_CNT 1 in GET_SECURITY_INFO
	FlashEncryption bool
}

// SimulateBoardWith is SimulateBoard with an NVS of values, a LittleFS
// partition of files and more log
func SimulateBoardWith(o SimOptions) (end func()) {
	b := c3Board()
	b.AppLog += o.AppLog
	parts := []Partition{
		{Name: "nvs", Type: formats.PartData, SubType: formats.SubDataNVS, Offset: 0x9000, Size: 0x6000},
		{Name: "phy_init", Type: formats.PartData, SubType: 0x01, Offset: 0xF000, Size: 0x1000},
		{Name: "factory", Type: formats.PartApp, SubType: formats.SubFactory, Offset: 0x10000, Size: 0x100000},
		{Name: "storage", Type: formats.PartData, SubType: formats.SubDataLittleFS, Offset: 0x110000, Size: 0x40000},
	}
	table, err := formats.BuildPartitionTable(parts)
	if err != nil {
		panic(err)
	}
	copy(b.Flash[0x8000:], table)
	if o.NVS != nil {
		img, err := nvs.Build(o.NVS, 0x6000)
		if err != nil {
			panic(err)
		}
		copy(b.Flash[0x9000:], img)
	}
	fs, err := simLittleFS(o.Files, 0x40000)
	if err != nil {
		panic(err)
	}
	copy(b.Flash[0x110000:], fs)
	if o.FlashEncryption {
		b.SecurityInfo = append([]byte(nil), b.SecurityInfo...)
		b.SecurityInfo[4] = 1
	}
	if o.EFuse {
		b.Regs = efuse.NewModel(chips.ByKey("esp32c3"), chips.Revision{})
	}
	return simulate(b)
}

// simLittleFS makes a LittleFS image of the files
func simLittleFS(files map[string][]byte, size int) ([]byte, error) {
	img := bytes.Repeat([]byte{0xFF}, size)
	f, err := littlefs.Format(img, littlefs.Config{BlockSize: 4096, BlockCount: uint32(size / 4096)})
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		// The folders first
		for dir := path.Dir(n); dir != "." && dir != "/"; dir = path.Dir(dir) {
			if err := f.Mkdir(dir); err != nil && !errors.Is(err, iofs.ErrExist) {
				return nil, fmt.Errorf("%s: %w", dir, err)
			}
		}
		if err := f.WriteFile(n, files[n]); err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
	}
	return f.Image(), nil
}

// EFuseRefusal returns the reasons a burn is refused for, when err is a
// refusal of PlanEFuse
func EFuseRefusal(err error) ([]string, bool) {
	var r *efuse.RefusedError
	if errors.As(err, &r) {
		return r.Reasons, true
	}
	return nil, false
}

// SimulateJTAGBoard makes the package see one ESP32-C6 on its built-in
// USB-JTAG (serial "C6SIM") and no serial ports: its flash has the
// partition table and an application "c6-app 3.1". end puts the real
// devices back.
func SimulateJTAGBoard() (end func()) {
	flash := bytes.Repeat([]byte{0xFF}, 4<<20)
	copy(flash[0x8000:], partitionTable())
	copy(flash[0x10000:], appHead(13, "c6-app", "3.1"))
	sim, err := jtag.NewSim(jtag.SimBoard{Chip: "esp32c6", Flash: flash, SBA: true, MAC: [6]byte{0x40, 0x4c, 0xca, 1, 2, 3}})
	if err != nil {
		panic(err)
	}
	oldList, oldOpen, oldPorts := listJTAG, openJTAG, listPorts
	dev := JTAGDevice{VID: 0x303a, PID: 0x1001, Serial: "C6SIM", Product: "USB JTAG/serial debug unit"}
	listJTAG = func() ([]JTAGDevice, error) { return []JTAGDevice{dev}, nil }
	openJTAG = func(d JTAGDevice, key string, opt jtag.Options) (*jtag.Session, error) {
		opt.Reopen = sim.Reopen
		return jtag.OpenLink(sim.Link(), key, opt)
	}
	listPorts = func() ([]serial.PortInfo, error) { return nil, nil }
	return func() { listJTAG, openJTAG, listPorts = oldList, oldOpen, oldPorts }
}

// CheckWrite is Check for a write with the options: a write the chip
// encrypts (development mode) has no warning about plain images
func CheckWrite(b Board, items []Item, opt FlashOptions) []Problem {
	ps := Check(b, items)
	if !opt.Encrypt || !b.Security.FlashEncryption || b.EncryptionRelease {
		return ps
	}
	out := ps[:0]
	for _, p := range ps {
		if p.Item == "" && p.Severity == Warning && strings.HasPrefix(p.Text, "Flash encryption is on:") {
			continue
		}
		out = append(out, p)
	}
	return out
}
