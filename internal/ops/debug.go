package ops

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/ipoluianov/altesp/internal/formats"
	"github.com/ipoluianov/altesp/internal/formats/elfdbg"
)

// PanicDecoder turns the panics in the monitor log into "function —
// file:line" with the application's .elf
type PanicDecoder struct {
	elf *elfdbg.ELF
	d   *elfdbg.Decoder
}

// NewPanicDecoder opens the .elf
func NewPanicDecoder(elfPath string) (*PanicDecoder, error) {
	e, err := elfdbg.Open(elfPath)
	if err != nil {
		return nil, err
	}
	return &PanicDecoder{elf: e, d: elfdbg.NewDecoder(e)}, nil
}

// Line takes a line of the log (without the colour codes) and returns the
// lines to show under it: the decoded frames of a panic, once
func (p *PanicDecoder) Line(line string) []string { return p.d.Line(line) }

// Flush returns what a panic left pending when the output stopped
func (p *PanicDecoder) Flush() []string { return p.d.Flush() }

// MatchesApp tells whether the .elf is the one the application on the
// board was built from, by the SHA-256 in its description; ok is false
// when the board doesn't tell
func (p *PanicDecoder) MatchesApp(img *formats.Image) (match, ok bool) {
	if img == nil || img.App == nil {
		return false, false
	}
	sha := fmt.Sprintf("%x", img.App.ELFSHA256)
	return p.elf.MatchSHA256(sha), true
}

// CoredumpTask is a task of a coredump in words
type CoredumpTask struct {
	Name     string
	State    string
	Crashed  bool
	Frames   []string
	Priority int
}

// CoredumpReport is a coredump in words
type CoredumpReport struct {
	Format, Version, Checksum string
	Exception                 string
	ELFMismatch               bool
	Tasks                     []CoredumpTask
}

func report(c *elfdbg.Coredump) *CoredumpReport {
	r := &CoredumpReport{Format: c.Format, Version: c.Version, Checksum: c.Checksum,
		Exception: c.Exception, ELFMismatch: c.ELFMismatch}
	for _, t := range c.Tasks {
		r.Tasks = append(r.Tasks, CoredumpTask{Name: t.Name, State: t.State, Crashed: t.Crashed,
			Priority: t.Priority, Frames: elfdbg.FormatFrames(t.Frames)})
	}
	return r
}

// ErrNoCoredump is a coredump partition without a dump
var ErrNoCoredump = elfdbg.ErrNoCoredump

// ParseCoredump reads a coredump from a file (partition data, an ELF core
// file or the base64 text printed on the UART) with the .elf ("" for
// addresses only)
func ParseCoredump(data []byte, elfPath string) (*CoredumpReport, error) {
	var e *elfdbg.ELF
	if elfPath != "" {
		var err error
		if e, err = elfdbg.Open(elfPath); err != nil {
			return nil, err
		}
	}
	c, err := elfdbg.ParseCoredump(data, e)
	if err != nil {
		return nil, err
	}
	return report(c), nil
}

// ParseCoredumpFile is ParseCoredump of a file
func ParseCoredumpFile(path, elfPath string) (*CoredumpReport, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseCoredump(data, elfPath)
}

// ReadCoredump reads the coredump partition of the board
func (s *Session) ReadCoredump(ctx context.Context, elfPath string) (*CoredumpReport, error) {
	p, ok := formats.FindPartition(s.board.Partitions, formats.PartData, 0x03)
	if !ok {
		return nil, errors.New("the board has no coredump partition")
	}
	data, err := s.Read(ctx, p.Offset, int(p.Size), nil)
	if err != nil {
		return nil, err
	}
	return ParseCoredump(data, elfPath)
}
