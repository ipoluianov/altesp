package ops

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/ipoluianov/altesp/internal/chips"
	"github.com/ipoluianov/altesp/internal/formats"
	"github.com/ipoluianov/altesp/internal/loader"
	"github.com/ipoluianov/altesp/internal/serial"
)

// The window and the command line call only this package: the types and
// the helpers they need from the packages below are here.

// PortInfo describes a port of the computer
type PortInfo = serial.PortInfo

// Reset is the way a board is reset by the control lines
type Reset = loader.Reset

const (
	ResetAuto    = loader.ResetAuto
	ResetBridge  = loader.ResetBridge
	ResetUSBJTAG = loader.ResetUSBJTAG
	ResetManual  = loader.ResetManual
)

// WaitPort waits for a board with built-in USB to come back after a reset
// and returns its port; gone: it must disappear first
func WaitPort(ctx context.Context, prev PortInfo, gone bool) (PortInfo, error) {
	if !simulated {
		return serial.Wait(ctx, prev, gone)
	}
	// The simulated port stays: it is there again at once
	for {
		if p, ok := FindPort(prev); ok {
			return p, nil
		}
		select {
		case <-ctx.Done():
			return PortInfo{}, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// FindPort finds the port of a board among the ports now
func FindPort(prev PortInfo) (PortInfo, bool) {
	ports, err := listPorts()
	if err != nil {
		return PortInfo{}, false
	}
	return serial.Find(prev, ports)
}

// File is a file to flash as the window lists it
type File struct {
	Path string
	Addr uint32
	// Kind is what the file is by its content: "Bootloader",
	// "Application", "Partition table", "Merged image"...
	Kind string
	// Size in bytes
	Size int
}

// Project is a build folder recognized: its files at their addresses
type Project struct {
	Dir   string
	Tool  string // "ESP-IDF", "PlatformIO", "Arduino"
	Chip  string // the chip key the build is for, "" when unknown
	ELF   string
	Files []File
}

// LoadProject recognizes a build folder or a project folder with one
func LoadProject(dir string) (*Project, error) {
	b, err := formats.LoadBuild(dir)
	if err != nil {
		return nil, err
	}
	p := &Project{Dir: dir, Tool: b.Tool, Chip: b.Chip, ELF: b.ELF}
	for _, f := range b.Files {
		size := 0
		if st, err := os.Stat(f.Path); err == nil {
			size = int(st.Size())
		}
		p.Files = append(p.Files, File{Path: f.Path, Addr: f.Addr, Kind: f.Kind.String(), Size: size})
	}
	return p, nil
}

// FileFor reads what a single file is and where it goes on this board:
// an application to its partition, a bootloader to the chip's address, a
// merged image to its start, a partition table to its place. ok is false
// when the place can't be told and the user has to give it; b may be the
// zero Board when no board is connected.
func FileFor(path string, b Board) (f File, ok bool, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return File{}, false, err
	}
	kind := formats.Detect(data)
	f = File{Path: path, Kind: kind.String(), Size: len(data)}
	switch kind {
	case formats.App:
		f.Addr, ok = AppAddress(b), true
	case formats.Merged:
		f.Addr, ok = formats.MergedAddr(data), true
	case formats.Bootloader:
		if b.Chip != nil {
			f.Addr, ok = b.Chip.BootloaderAddr, true
		} else if img, err := formats.ParseImage(data); err == nil {
			if c := chipByID(img.ChipID); c != nil {
				f.Addr, ok = c.BootloaderAddr, true
			}
		}
	case formats.PartitionTable:
		f.Addr, ok = 0x8000, true
		if b.PartitionTableAddr != 0 {
			f.Addr = b.PartitionTableAddr
		}
	}
	return f, ok, nil
}

// ItemsOf reads the files into items to flash
func ItemsOf(files []File) ([]Item, error) {
	items := make([]Item, 0, len(files))
	for _, f := range files {
		data, err := os.ReadFile(f.Path)
		if err != nil {
			return nil, err
		}
		items = append(items, Item{Name: filepath.Base(f.Path), Addr: f.Addr, Data: data})
	}
	return items, nil
}

// chipByID is the chip of an image header's chip ID
func chipByID(id uint16) *chips.Chip { return chips.ByChipID(uint32(id)) }

// BuiltinUSB tells a port of the chip's own USB: it has no baud rate, and
// it disappears and comes back when the chip resets
func BuiltinUSB(p PortInfo) bool { return builtinUSB(p) }

// The errors of a port the window tells apart: closed by the program, or
// gone with the device
var (
	ErrPortClosed = serial.ErrClosed
	ErrPortGone   = serial.ErrGone
)
