// Package chain reaches one TAP of a JTAG chain: the other TAPs are put
// in BYPASS by every IR scan and add their one-bit bypass register to
// every DR scan (IEEE 1149.1: BYPASS is the all-ones instruction, 8.4;
// its register captures 0, 10.1; after Test-Logic-Reset each TAP selects
// its IDCODE register if it has one, whose bit 0 is 1, or BYPASS,
// 12.1.1). The ESP32 has a TAP per CPU core.
package chain

import (
	"errors"
	"fmt"

	"github.com/ipoluianov/altesp/internal/jtag/espjtag"
)

// Scanner is a TAP driver: espjtag.TAP, ftdi.TAP
type Scanner interface {
	IR(n int, v uint64)
	DR(n int, v uint64, capture int, idle int) *espjtag.Result
	Run() error
}

// Resetter is a Scanner that can reset the TAPs
type Resetter interface {
	Scanner
	Reset()
}

// Chain is a Scanner for one TAP of a chain. The TAPs are numbered from
// TDO: TAP 0 is the one whose TDO is the adapter's TDO, whose bits come
// out first.
type Chain struct {
	tap    Scanner
	irLens []int
	target int
}

// New selects TAP target of the chain whose IR lengths are irLens
func New(tap Scanner, irLens []int, target int) (*Chain, error) {
	if target < 0 || target >= len(irLens) {
		return nil, fmt.Errorf("TAP %d of a chain of %d", target, len(irLens))
	}
	total := 0
	for _, n := range irLens {
		total += n
	}
	if total > 64 {
		return nil, errors.New("the IR of the chain is longer than 64 bits")
	}
	return &Chain{tap: tap, irLens: irLens, target: target}, nil
}

// Target is the selected TAP
func (c *Chain) Target() int { return c.target }

// IR shifts v into the target TAP and BYPASS into the others: the bits of
// TAP 0 go in first
func (c *Chain) IR(n int, v uint64) {
	if n != c.irLens[c.target] {
		panic(fmt.Sprintf("chain: IR of %d bits for a TAP with %d", n, c.irLens[c.target]))
	}
	var all uint64
	pos := 0
	for i, l := range c.irLens {
		x := uint64(1)<<uint(l) - 1
		if i == c.target {
			x = v & x
		}
		all |= x << uint(pos)
		pos += l
	}
	c.tap.IR(pos, all)
}

// DR shifts v through the target's data register; the TAPs before it
// (nearer TDO) add their bypass bit in front, the ones after it behind
func (c *Chain) DR(n int, v uint64, capture int, idle int) *espjtag.Result {
	before := c.target
	after := len(c.irLens) - 1 - c.target
	total := before + n + after
	if total > 64 {
		panic(fmt.Sprintf("chain: DR scan of %d bits", total))
	}
	r := c.tap.DR(total, v<<uint(before), min(before+capture, total), idle)
	if capture == 0 {
		return nil
	}
	return r.Sub(before, capture)
}

// Run sends the queued scans
func (c *Chain) Run() error { return c.tap.Run() }

// ScanIDCodes resets the TAPs and reads the chain as it comes out of
// Test-Logic-Reset: a 32-bit IDCODE for each TAP that has one, 0 for one
// in BYPASS, from TAP 0. Ones are shifted in, so an all-ones IDCODE marks
// the end. At most 64 bits are read: two TAPs with an IDCODE.
func ScanIDCodes(tap Resetter) ([]uint32, error) {
	tap.Reset()
	r := tap.DR(64, ^uint64(0), 64, 0)
	if err := tap.Run(); err != nil {
		return nil, err
	}
	v := r.Value()
	var ids []uint32
	for pos := 0; pos < 64; {
		if v>>uint(pos)&1 == 0 {
			ids = append(ids, 0)
			pos++
			continue
		}
		if pos+32 > 64 {
			break
		}
		id := uint32(v >> uint(pos))
		if id == 0xFFFFFFFF {
			break
		}
		ids = append(ids, id)
		pos += 32
	}
	if len(ids) == 0 {
		return nil, errors.New("no TAP in the JTAG chain")
	}
	return ids, nil
}
