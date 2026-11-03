package efuse

import (
	"errors"
	"fmt"

	"github.com/ipoluianov/altesp/internal/chips"
)

// Options of Burn
type Options struct {
	// APBMHz is the APB clock while the loader runs; the ESP32 and the
	// ESP32-S2 set the eFuse timing by it (efuse_hal_set_timing).
	//
	// unverified: the APB clock while the ROM loader or the stub runs:
	// the crystal (26 or 40 MHz) on the ESP32 and 40 MHz on the ESP32-S2
	// are assumed by the callers, and whether the stub changes it is not
	// known.
	APBMHz int
	// CrystalMHz is the crystal of an ESP32-C2 (26 or 40): its
	// TPGM_INACTIVE is crystal MHz * 5 (efuse_hal.c of the ESP32-C2)
	CrystalMHz int
	// Polls is how many times CMD is read while a command runs; 0 is 100.
	// Each read is a round trip over the port, much longer than a burn.
	Polls int
	// Trace records every register access in Result.Log
	Trace bool
}

// Stage is the burn of one block
type Stage struct {
	Block int
	// WRDis: the stage burns the bits of WR_DIS, deferred to the end
	WRDis    bool
	Attempts int
	OK       bool
	// CodingError: the controller reported a coding error for the block
	// after the burn (RD_RS_ERR, RD_REPEAT_ERR, DEC_STATUS)
	CodingError bool
	Err         string
	Skipped     bool
}

// Result tells what a burn did
type Result struct {
	Stages []Stage
	// After are the blocks read back at the end
	After [][]uint32
	// Notes are remarks on the read-back: blocks that are read-protected
	// now and can't be verified
	Notes []string
	// Log is every register access with Options.Trace
	Log []string
	// Programs is how many PGM commands were issued
	Programs int
}

// maxAttempts: ESP-IDF's burn_chip_opt tries a block up to three times
// when the read-back or the coding check fails
const maxAttempts = 3

// Burn carries out a plan through the eFuse controller: for each block,
// the last first and BLOCK0 last, it writes the bits to program into the
// programming registers (with the RS check bytes, or the ESP32's 3/4 or
// repeat coding), runs the program command, re-reads the eFuses and checks
// the error registers and that every bit to program is set, trying a
// block up to three times. A failed block stops the burn: the blocks
// after it, BLOCK0 with its protection bits among them, are not burned.
// The bits of WR_DIS are burned last, after everything else succeeded.
// Before burning, the blocks are read and compared with the plan's dump.
//
// The sequence follows ESP-IDF's esp_efuse_utility_burn_chip and the
// efuse_hal_program / efuse_hal_read of each chip.
func Burn(chip *chips.Chip, r chips.Regs, p *Plan, opt Options) (*Result, error) {
	if p == nil || p.t == nil {
		return nil, errors.New("no burn plan")
	}
	if chip == nil || p.chip == nil || chip.Key != p.chip.Key {
		return nil, fmt.Errorf("the plan is for the %v, not the %v", p.chip, chip)
	}
	l := p.l
	b := &burner{l: l, opt: opt, res: &Result{}}
	if opt.Polls <= 0 {
		b.opt.Polls = 100
	}
	b.r = r
	if opt.Trace {
		b.r = &tracer{r: r, log: &b.res.Log}
	}
	if err := b.checkClock(chip); err != nil {
		return nil, err
	}
	if p.Empty() {
		return b.res, nil
	}

	// the chip still is as the plan saw it
	if err := b.read(); err != nil {
		return nil, err
	}
	now, err := b.readBlocks()
	if err != nil {
		return nil, err
	}
	for _, w := range p.Writes {
		if !equal(now[w.Block], p.old[w.Block]) {
			return nil, fmt.Errorf("eFuse BLOCK%d changed since it was read: read the eFuses again and plan again", w.Block)
		}
	}
	if !equal(now[0], p.old[0]) {
		return nil, errors.New("eFuse BLOCK0 changed since it was read: read the eFuses again and plan again")
	}

	type stage struct {
		blk   int
		set   []uint32
		wrDis bool
	}
	var stages []stage
	var deferred uint32
	for _, w := range p.Writes {
		set := append([]uint32(nil), w.Set...)
		if w.Block == 0 {
			deferred = set[0] & p.wrDis
			set[0] &^= p.wrDis
			if zero(set) {
				continue
			}
		}
		stages = append(stages, stage{blk: w.Block, set: set})
	}
	if deferred != 0 {
		set := make([]uint32, len(p.old[0]))
		set[0] = deferred
		stages = append(stages, stage{blk: 0, set: set, wrDis: true})
	}

	failed := false
	for _, s := range stages {
		st := Stage{Block: s.blk, WRDis: s.wrDis}
		if failed {
			st.Skipped = true
			st.Err = "not burned: an earlier block failed"
			b.res.Stages = append(b.res.Stages, st)
			continue
		}
		var err error
		if l.kind == kindESP32 {
			err = b.burnESP32(&st, s.blk, s.set, p)
		} else {
			err = b.burnRS(&st, s.blk, s.set)
		}
		if err != nil {
			// the port failed: the state of the chip is unknown
			st.Err = err.Error()
			b.res.Stages = append(b.res.Stages, st)
			return b.res, fmt.Errorf("burning eFuse BLOCK%d: %w; read the eFuses to see what was burned", s.blk, err)
		}
		if !st.OK {
			failed = true
		}
		b.res.Stages = append(b.res.Stages, st)
	}

	after, err := b.readBlocks()
	if err != nil {
		return b.res, err
	}
	b.res.After = after
	for _, w := range p.Writes {
		if blockProtected(p.t.rd, after, w.Block) {
			b.res.Notes = append(b.res.Notes, fmt.Sprintf("BLOCK%d is read-protected now; it was verified before the protection was burned", w.Block))
			continue
		}
		for i, v := range w.Set {
			if after[w.Block][i]&v != v && !failed {
				failed = true
				b.res.Notes = append(b.res.Notes, fmt.Sprintf("BLOCK%d word %d: 0x%08X read back, bits 0x%08X expected", w.Block, i, after[w.Block][i], v))
			}
		}
	}
	if failed {
		return b.res, errors.New("the eFuse burn failed: see the result for the blocks; some bits may be burned already")
	}
	return b.res, nil
}

// DryRun runs the plan against a Model of the chip made of the plan's
// dump, with every register access traced: what Burn would do, without
// touching a chip
func DryRun(p *Plan, opt Options) (*Result, error) {
	if p == nil || p.t == nil {
		return nil, errors.New("no burn plan")
	}
	m := NewModel(p.chip, p.rev)
	if m == nil {
		return nil, errNoChip(p.chip)
	}
	for b, w := range p.old {
		m.setRead(b, w)
	}
	opt.Trace = true
	return Burn(p.chip, m, p, opt)
}

type burner struct {
	r   chips.Regs
	l   *layout
	opt Options
	res *Result
}

func (b *burner) write(addr, v uint32) error { return b.r.WriteReg(addr, v, 0xFFFFFFFF, 0) }

// field writes bits [s, s+n) of a register, keeping the others
func (b *burner) field(addr uint32, s, n uint, v uint32) error {
	old, err := b.r.ReadReg(addr)
	if err != nil {
		return err
	}
	m := uint32(1<<n-1) << s
	return b.write(addr, old&^m|v<<s&m)
}

func (b *burner) checkClock(chip *chips.Chip) error {
	switch b.l.timing {
	case timingESP32, timingS2:
		if b.opt.APBMHz <= 0 {
			return fmt.Errorf("%s: the APB clock is needed for the eFuse timing", chip.Name)
		}
	case timingC2:
		if b.opt.CrystalMHz != 26 && b.opt.CrystalMHz != 40 {
			return fmt.Errorf("%s: the crystal (26 or 40 MHz) is needed for the eFuse timing", chip.Name)
		}
	}
	return nil
}

// timing is efuse_hal_set_timing of each chip
func (b *burner) timing() error {
	l := b.l
	type f struct {
		addr uint32
		s, n uint
		v    uint32
	}
	var fs []f
	switch l.timing {
	case timingFixed, timingC2:
		fs = []f{{l.dacConf, 9, 8, 0xFF}, {l.dacConf, 0, 8, 0x28}, {l.wrTim1, 8, 16, 0x3000}, {l.wrTim2, 0, 16, 0x190}}
		if l.timing == timingC2 {
			fs = append(fs, f{l.wrTim0, 8, 8, uint32(b.opt.CrystalMHz * 5)})
		}
	case timingS2:
		// tsup_a, tpgm, thp_a, tpgm_inactive, clk_div, power_on,
		// power_off, tsur_a, trd, thr_a by APB 80, 40, else
		v := [10]uint32{0x1, 0xC8, 0x1, 0x1, 0x28, 0x2880, 0x40, 0x1, 0x1, 0x1}
		switch b.opt.APBMHz {
		case 80:
			v = [10]uint32{0x2, 0x320, 0x2, 0x4, 0xA0, 0xA200, 0x100, 0x2, 0x4, 0x2}
		case 40:
			v = [10]uint32{0x1, 0x190, 0x1, 0x2, 0x50, 0x5100, 0x80, 0x1, 0x2, 0x1}
		}
		fs = []f{{l.wrTim1, 0, 8, v[0]}, {l.wrTim0, 16, 16, v[1]}, {l.wrTim0, 0, 8, v[2]},
			{l.wrTim0, 8, 8, v[3]}, {l.dacConf, 0, 8, v[4]}, {l.wrTim1, 8, 16, v[5]},
			{l.wrTim2, 0, 16, v[6]}, {l.rdTim, 16, 8, v[7]}, {l.rdTim, 8, 8, v[8]}, {l.rdTim, 0, 8, v[9]}}
	case timingESP32:
		// efuse_hal_set_timing of the ESP32: CLK_SEL0, CLK_SEL1,
		// DAC_CLK_DIV by the APB clock
		sel0, sel1, div := uint32(80), uint32(128), uint32(100)
		switch {
		case b.opt.APBMHz <= 26:
			sel0, sel1, div = 250, 255, 52
		case b.opt.APBMHz <= 40:
			sel0, sel1, div = 160, 255, 80
		}
		fs = []f{{l.dacConf, 0, 8, div}, {l.clk, 0, 8, sel0}, {l.clk, 8, 8, sel1}}
	}
	for _, x := range fs {
		if err := b.field(x.addr, x.s, x.n, x.v); err != nil {
			return err
		}
	}
	return nil
}

// run starts a command and polls CMD until its bit clears
func (b *burner) run(op, cmd, bit uint32) error {
	if err := b.field(b.l.conf, 0, 16, op); err != nil {
		return err
	}
	if err := b.write(b.l.cmd, cmd); err != nil {
		return err
	}
	return b.poll(bit)
}

// poll reads CMD until bit is clear
func (b *burner) poll(bit uint32) error {
	for i := 0; i < b.opt.Polls; i++ {
		v, err := b.r.ReadReg(b.l.cmd)
		if err != nil {
			return err
		}
		if v&bit == 0 {
			return nil
		}
	}
	return errors.New("the eFuse controller did not finish the command")
}

// read is efuse_hal_read: the read op code and READ_CMD; the later chips
// poll READ_CMD twice ("due to a hardware error")
func (b *burner) read() error {
	if b.l.timing != timingESP32 && b.l.timing != timingNone {
		if err := b.timing(); err != nil {
			return err
		}
	}
	if b.l.kind == kindESP32 {
		// the ESP32 waits for all of CMD (efuse_ll_get_cmd)
		return b.run(opRead, cmdRead, cmdRead|cmdPgm)
	}
	if err := b.run(opRead, cmdRead, cmdRead); err != nil {
		return err
	}
	return b.poll(cmdRead)
}

func (b *burner) readBlocks() ([][]uint32, error) {
	out := make([][]uint32, len(b.l.blocks))
	for i, bl := range b.l.blocks {
		out[i] = make([]uint32, bl.words)
		for w := range out[i] {
			v, err := b.r.ReadReg(bl.rd + uint32(4*w))
			if err != nil {
				return nil, err
			}
			out[i][w] = v
		}
	}
	return out, nil
}

// codingError is efuse_hal_is_coding_error_in_block
func (b *burner) codingError(blk int, scheme34 bool) (bool, error) {
	l := b.l
	if l.kind == kindESP32 {
		if blk == 0 || !scheme34 {
			return false, nil
		}
		v, err := b.r.ReadReg(l.decStatus)
		return v>>(4*uint(blk-1))&0xF != 0, err
	}
	if blk == 0 {
		for _, a := range l.repeatErr {
			v, err := b.r.ReadReg(a)
			if err != nil || v != 0 {
				return v != 0, err
			}
		}
		return false, nil
	}
	errs := make([]uint32, len(l.rsErr))
	for i, a := range l.rsErr {
		v, err := b.r.ReadReg(a)
		if err != nil {
			return false, err
		}
		errs[i] = v
	}
	_, _, bad := l.rsBlockError(errs, blk)
	return bad, nil
}

// verify: every bit to program reads back set
// (esp_efuse_utility_is_correct_written_data)
func (b *burner) verify(blk int, set []uint32, words int) (bool, error) {
	for i := 0; i < words && i < len(set); i++ {
		v, err := b.r.ReadReg(b.l.blocks[blk].rd + uint32(4*i))
		if err != nil {
			return false, err
		}
		if v&set[i] != set[i] {
			return false, nil
		}
	}
	return true, nil
}

// clearPgm zeroes PGM_DATA0-7 and PGM_CHECK_VALUE0-2
// (ets_efuse_clear_program_registers)
func (b *burner) clearPgm() error {
	for i := uint32(0); i < 11; i++ {
		if err := b.write(b.l.pgmData+4*i, 0); err != nil {
			return err
		}
	}
	return nil
}

// burnRS burns a block of a later chip (burn_chip_opt of components/
// efuse/<chip>/esp_efuse_utility.c with efuse_hal_program)
func (b *burner) burnRS(st *Stage, blk int, set []uint32) error {
	l := b.l
	if blk > l.blkNumMax {
		return fmt.Errorf("BLOCK%d can't be selected by CMD.BLK_NUM", blk)
	}
	if err := b.clearPgm(); err != nil {
		return err
	}
	data := append([]uint32(nil), set...)
	var check [3]uint32
	if blk > 0 {
		check = rsEncode(data)
	}
	load := func() error {
		// the words follow each other from PGM_DATA0; BLOCK0 of the
		// ESP32-S31 has 9 and reaches PGM_CHECK_VALUE0, as ESP-IDF's
		// memcpy does
		for i, v := range data {
			if err := b.write(l.pgmData+uint32(4*i), v); err != nil {
				return err
			}
		}
		if blk > 0 {
			for i, v := range check {
				if err := b.write(l.pgmData+pgmCheckOff+uint32(4*i), v); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := load(); err != nil {
		return err
	}
	before, err := b.codingError(blk, false)
	if err != nil {
		return err
	}
	for st.Attempts < maxAttempts {
		st.Attempts++
		if l.timing != timingNone {
			if err := b.timing(); err != nil {
				return err
			}
		}
		if err := b.run(opWrite, uint32(blk)<<cmdBlkNumS|cmdPgm, cmdPgm); err != nil {
			return err
		}
		b.res.Programs++
		if err := b.clearPgm(); err != nil {
			return err
		}
		if err := b.read(); err != nil {
			return err
		}
		after, err := b.codingError(blk, false)
		if err != nil {
			return err
		}
		st.CodingError = after && !before
		ok, err := b.verify(blk, set, len(set))
		if err != nil {
			return err
		}
		if ok && !st.CodingError {
			st.OK = true
			return nil
		}
		if err := load(); err != nil {
			return err
		}
	}
	if err := b.clearPgm(); err != nil {
		return err
	}
	if st.CodingError {
		st.Err = fmt.Sprintf("BLOCK%d reports a coding error after the burn", blk)
	} else {
		st.Err = fmt.Sprintf("BLOCK%d: the burned bits don't read back", blk)
	}
	return nil
}

// clearESP32 zeroes BLK0_WDATA0-6 and BLK1-3_WDATA0-7
// (efuse_hal_clear_program_registers of the ESP32)
func (b *burner) clearESP32() error {
	for _, bl := range b.l.blocks {
		for w := 0; w < bl.words; w++ {
			if err := b.write(bl.wr+uint32(4*w), 0); err != nil {
				return err
			}
		}
	}
	return nil
}

// burnESP32 burns a block of the ESP32 (burn_chip_opt of components/
// efuse/esp32/esp_efuse_utility.c with its efuse_hal_program)
func (b *burner) burnESP32(st *Stage, blk int, set []uint32, p *Plan) error {
	l := b.l
	coding := CodingNone
	if blk > 0 {
		coding = l.codings(p.old)[blk]
	}
	words := append([]uint32(nil), set...)
	switch coding {
	case Coding34:
		e := encode34(set)
		words = e[:]
	case CodingRepeat:
		e := encodeRepeat(set)
		words = e[:]
	}
	if err := b.timing(); err != nil {
		return err
	}
	if err := b.clearESP32(); err != nil {
		return err
	}
	load := func() error {
		for i, v := range words {
			if err := b.write(l.blocks[blk].wr+uint32(4*i), v); err != nil {
				return err
			}
		}
		return nil
	}
	if err := load(); err != nil {
		return err
	}
	scheme34 := coding == Coding34
	before, err := b.codingError(blk, scheme34)
	if err != nil {
		return err
	}
	for st.Attempts < maxAttempts {
		st.Attempts++
		if err := b.run(opWrite, cmdPgm, cmdPgm|cmdRead); err != nil {
			return err
		}
		b.res.Programs++
		if err := b.read(); err != nil {
			return err
		}
		after, err := b.codingError(blk, scheme34)
		if err != nil {
			return err
		}
		st.CodingError = after && !before
		ok, err := b.verify(blk, set, coding.dataWords(len(set)))
		if err != nil {
			return err
		}
		if ok && !st.CodingError {
			st.OK = true
			return b.clearESP32()
		}
		if err := load(); err != nil {
			return err
		}
	}
	if err := b.clearESP32(); err != nil {
		return err
	}
	if st.CodingError {
		st.Err = fmt.Sprintf("BLOCK%d reports a 3/4 coding error after the burn", blk)
	} else {
		st.Err = fmt.Sprintf("BLOCK%d: the burned bits don't read back", blk)
	}
	return nil
}

func equal(a, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// tracer records the register accesses
type tracer struct {
	r   chips.Regs
	log *[]string
}

func (t *tracer) ReadReg(addr uint32) (uint32, error) {
	v, err := t.r.ReadReg(addr)
	if err != nil {
		*t.log = append(*t.log, fmt.Sprintf("read  0x%08X: %v", addr, err))
	} else {
		*t.log = append(*t.log, fmt.Sprintf("read  0x%08X = 0x%08X", addr, v))
	}
	return v, err
}

func (t *tracer) WriteReg(addr, value, mask, delayUS uint32) error {
	*t.log = append(*t.log, fmt.Sprintf("write 0x%08X = 0x%08X", addr, value))
	return t.r.WriteReg(addr, value, mask, delayUS)
}
