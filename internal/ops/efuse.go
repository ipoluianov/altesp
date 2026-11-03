package ops

import (
	"context"
	"errors"
	"fmt"

	"github.com/ipoluianov/altesp/internal/chips/efuse"
)

// EFuseDump is everything read from the eFuses of the chip
type EFuseDump = efuse.Dump

// EFuseChange is a field to burn with its new value
type EFuseChange = efuse.Change

// EFusePlan is a checked burn: the changes in words, the warnings, the
// blocks
type EFusePlan = efuse.Plan

// EFuseResult is what a burn did
type EFuseResult = efuse.Result

// EFuseUint is the change of a field to a number
func EFuseUint(field string, v uint64) EFuseChange { return efuse.Uint(field, v) }

// BurnWord must be typed to burn eFuses: the change can't be undone
const BurnWord = "BURN"

// RSUnverified is the warning added to a plan that burns a block coded
// with Reed-Solomon: the parity byte order is an assumption until it is
// checked on a sacrificial board (TZ.md "Hardware checks")
const RSUnverified = "This writes a block protected by Reed-Solomon coding; AltEsp's encoding of it is not verified on a real chip yet. A wrong parity leaves the block reading as damaged."

// ReadEFuse reads and decodes all eFuses of the chip
func (s *Session) ReadEFuse(ctx context.Context) (*EFuseDump, error) {
	b := s.board
	if b.SecureDownload {
		return nil, errors.New("Secure Download Mode doesn't allow reading the eFuses")
	}
	return efuse.Read(b.Chip, b.Info.Revision, regs{ctx: ctx, l: s.t})
}

// PlanEFuse checks the changes against the eFuses read: what each one
// does, what can't be undone, what is refused
func (s *Session) PlanEFuse(d *EFuseDump, changes []EFuseChange) (*EFusePlan, error) {
	p, err := efuse.PlanBurn(d, changes)
	if err != nil {
		return nil, err
	}
	for _, w := range p.Writes {
		if w.Coding == efuse.CodingRS {
			p.Warnings = append(p.Warnings, RSUnverified)
			break
		}
	}
	return p, nil
}

// BurnEFuse burns a plan. confirm must be BurnWord. The ESP32 and the
// ESP32-S2 are burned under the ROM loader only, whose clock the timing
// is computed for: connect without the stub.
func (s *Session) BurnEFuse(ctx context.Context, p *EFusePlan, confirm string) (*EFuseResult, error) {
	if confirm != BurnWord {
		return nil, fmt.Errorf("type %s to burn: eFuses can't be changed back", BurnWord)
	}
	b := s.board
	opt := efuse.Options{}
	switch b.Chip.Key {
	case "esp32", "esp32s2":
		if b.Stub {
			return nil, errors.New("connect without the stub loader to burn eFuses on this chip: the burn timing depends on the ROM loader's clock")
		}
		opt.APBMHz = 40
		if b.Chip.Key == "esp32" {
			if b.Info.CrystalMHz == 0 {
				return nil, errors.New("the crystal frequency is unknown: the burn timing can't be set")
			}
			opt.APBMHz = b.Info.CrystalMHz
		}
	case "esp32c2":
		if b.Info.CrystalMHz == 0 {
			return nil, errors.New("the crystal frequency is unknown: the burn timing can't be set")
		}
		opt.CrystalMHz = b.Info.CrystalMHz
	}
	return efuse.Burn(b.Chip, regs{ctx: ctx, l: s.t}, p, opt)
}

// encryptionRelease tells whether the eFuse that stops the loader from
// encrypting what it writes is burned: DIS_DOWNLOAD_MANUAL_ENCRYPT, on the
// ESP32 DISABLE_DL_ENCRYPT (esp_efuse_table.csv of each chip)
func (s *Session) encryptionRelease(ctx context.Context) bool {
	d, err := s.ReadEFuse(ctx)
	if err != nil {
		return false
	}
	for _, v := range d.Values {
		switch v.Field.Name {
		case "DIS_DOWNLOAD_MANUAL_ENCRYPT", "DISABLE_DL_ENCRYPT":
			return v.Uint() != 0
		}
	}
	return false
}
