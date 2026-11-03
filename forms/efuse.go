package forms

import (
	"context"
	hexenc "encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ipoluianov/altesp/internal/ops"
	"github.com/ipoluianov/nui/ui"
)

// The eFuse window: all the fields of the chip, read-only by default. A
// separate write mode, behind a red banner, takes new values; they are
// burned only after the plan is reviewed and the confirmation word typed.

// efuseView is the eFuse window
type efuseView struct {
	m    *MainForm
	dump *ops.EFuseDump

	// rows are the lines of the table: a category header (field -1) or a
	// field (an index into dump.Values)
	rows []efRow
	// pending are the new values, in the order they were given
	pending []ops.EFuseChange

	c        *ui.DialogContent
	info     *ui.Label
	errs     *ui.Label
	filter   *ui.TextBox
	hideZero *ui.Checkbox
	write    *ui.Checkbox
	banner   *banner
	list     *ui.ListView
	model    *ui.FuncModel
	pendBox  *ui.Panel
	pendText *ui.Label

	// confirmDlg is the burn confirmation shown last, keyDlg the dialog of
	// a key (for the tests)
	confirmDlg, keyDlg *ui.DialogContent
}

type efRow struct {
	header string
	field  int
}

// The columns of the table
var efCols = []int{0, 260, 470, -90}

// showEFuse reads the eFuses and shows them
func (m *MainForm) showEFuse() {
	var d *ops.EFuseDump
	m.run(true, func(ctx context.Context, s *ops.Session) error {
		var err error
		d, err = s.ReadEFuse(ctx)
		return err
	}, func(err error) {
		if err != nil {
			m.showError(T().ToolEfuse, err)
			return
		}
		v := &efuseView{m: m, dump: d}
		v.show()
	})
}

func (v *efuseView) show() {
	m := v.m
	c := &ui.DialogContent{}
	c.InitWidget()
	c.SetCellPadding(6)
	v.c = c
	v.info = ui.NewLabel("")
	v.info.SetFontBold(true)
	c.AddWidget(0, 0, v.info)
	v.errs = ui.NewLabel("")
	v.errs.SetForegroundColor(colorError.get())
	c.AddWidget(1, 0, v.errs)

	bar := ui.NewHBox()
	bar.SetCellPadding(6)
	v.filter = ui.NewTextBox()
	v.filter.SetHintFunc(func() string { return T().EFuseFilter })
	v.filter.SetMinWidth(240)
	v.filter.OnTextChanged.Connect(func(string) { v.fill() })
	bar.Add(v.filter)
	v.hideZero = ui.NewCheckbox("")
	v.hideZero.SetTextFunc(func() string { return T().HideZero })
	v.hideZero.SetChecked(true)
	v.hideZero.OnCheckedChanged.Connect(func(bool) { v.fill() })
	bar.Add(v.hideZero)
	bar.Add(textButton(func() string { return T().Refresh }, v.reread))
	bar.AddStretch()
	v.write = ui.NewCheckbox("")
	v.write.SetTextFunc(func() string { return T().WriteMode })
	v.write.SetName("efuse-write-mode")
	v.write.OnCheckedChanged.Connect(func(on bool) { v.setWriteMode(on) })
	bar.Add(v.write)
	c.AddWidget(2, 0, bar)

	// The banner of the write mode
	v.banner = newBanner(func() string { return T().WriteModeBanner })
	v.banner.SetVisible(false)
	c.AddWidget(3, 0, v.banner)

	c.AddWidget(4, 0, newColumns(efCols, func() []string {
		return []string{T().ColName, T().ColValue, T().ColDescription, ""}
	}))
	v.model = ui.NewFuncModel(func() int { return len(v.rows) }, 1, func(row, col int) string {
		if r := v.rows[row]; r.field >= 0 {
			return v.dump.Values[r.field].Field.Name
		}
		return v.rows[row].header
	})
	v.list = ui.NewListView()
	v.list.SetName("efuse-fields")
	v.list.SetModel(v.model)
	v.list.SetMinSize(860, 380)
	v.list.SetItemPainter(v.paintRow)
	v.list.OnActivated.Connect(v.edit)
	c.AddWidget(5, 0, v.list)

	// The changes waiting for the review
	v.pendBox = ui.NewPanel()
	v.pendBox.SetPanelPadding(0)
	v.pendText = ui.NewLabel("")
	v.pendText.SetXExpandable(true)
	v.pendBox.AddWidget(0, 0, v.pendText)
	pb := ui.NewHBox()
	pb.SetCellPadding(6)
	pb.Add(textButton(func() string { return T().Review }, v.review))
	pb.Add(textButton(func() string { return T().ClearChanges }, func() {
		v.pending = nil
		v.fill()
	}))
	v.pendBox.AddWidget(0, 1, pb)
	v.pendBox.SetVisible(false)
	c.AddWidget(6, 0, v.pendBox)

	buttons := ui.NewDialogButtonBox()
	buttons.AddButton(T().CopyText, ui.ButtonOther, func() {
		ui.ClipboardSetText(v.text())
		m.status.ShowMessage(T().Copied)
	})
	buttons.AddButton(T().CloseDiff, ui.ButtonReject, func() { c.Form().Close() })
	c.AddWidget(7, 0, buttons)
	c.OnDialogShow = func() { c.Form().SetTitle(T().ToolEfuse) }
	v.fill()
	m.Form.Panel().ShowDialog(c)
	m.efuse = v
}

// fill makes the rows: the fields that pass the filter, by category
func (v *efuseView) fill() {
	d := v.dump
	chip := ""
	if d.Chip != nil {
		chip = d.Chip.Name + " · "
	}
	v.info.SetText(chip + T().Coding + ": " + d.CodingScheme)
	v.errs.SetText(strings.Join(d.Errors, "\n"))
	v.errs.SetVisible(len(d.Errors) > 0)

	q := strings.ToLower(strings.TrimSpace(v.filter.Text()))
	idx := make([]int, 0, len(d.Values))
	for i, val := range d.Values {
		f := val.Field
		if v.hideZero.Checked() && val.Uint() == 0 && !val.WriteProtected && !val.ReadProtected && !v.isPending(f.Name) {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(f.Name+" "+strings.Join(f.Aliases, " ")+" "+f.Desc), q) {
			continue
		}
		idx = append(idx, i)
	}
	sort.SliceStable(idx, func(a, b int) bool { return d.Values[idx[a]].Field.Category < d.Values[idx[b]].Field.Category })
	v.rows = nil
	cat := "\x00"
	for _, i := range idx {
		if c := d.Values[i].Field.Category; c != cat {
			cat = c
			v.rows = append(v.rows, efRow{header: c, field: -1})
		}
		v.rows = append(v.rows, efRow{field: i})
	}
	v.model.Changed()
	v.list.Update()

	var lines []string
	for _, ch := range v.pending {
		lines = append(lines, fmt.Sprintf("%s → %s", ch.Field, v.pendingText(ch.Field)))
	}
	v.pendText.SetText(T().PendingChanges + " " + strings.Join(lines, ", "))
	v.pendBox.SetVisible(len(v.pending) > 0)
}

func (v *efuseView) paintRow(p *ui.ItemPaint) {
	if p.Row >= len(v.rows) {
		return
	}
	r := v.rows[p.Row]
	if r.field < 0 {
		cnv := p.Canvas
		cnv.SetFontFamily(ui.FontFamilySans)
		cnv.SetFontSize(ui.ThemeFontSize())
		cnv.SetColor(colorSecondary.get())
		cnv.SetVAlign(ui.VAlignCenter)
		cnv.DrawText(8, 0, p.Width-16, p.Height, strings.ToUpper(r.header))
		return
	}
	val := v.dump.Values[r.field]
	value := val.Text
	if v.isPending(val.Field.Name) {
		value += " → " + v.pendingText(val.Field.Name)
	}
	var marks []string
	if val.WriteProtected {
		marks = append(marks, "WP")
	}
	if val.ReadProtected {
		marks = append(marks, "RP")
	}
	paintColumns(p, efCols, []string{"  " + val.Field.Name, value, val.Field.Desc, strings.Join(marks, " ")}, 1)
	if v.isPending(val.Field.Name) {
		p.Canvas.FillRect(0, 2, 3, p.Height-4, colorError.get())
	}
}

func (v *efuseView) isPending(name string) bool {
	for _, c := range v.pending {
		if c.Field == name {
			return true
		}
	}
	return false
}

// pendingText is the new value of a field as typed: a number, or the
// bytes of a key
func (v *efuseView) pendingText(name string) string {
	for _, c := range v.pending {
		if c.Field != name {
			continue
		}
		if len(c.Value) > 8 {
			b := c.Value
			return fmt.Sprintf("%d B: %02X %02X %02X … %02X", len(b), b[0], b[1], b[2], b[len(b)-1])
		}
		return fmt.Sprintf("0x%X", rawValue(c.Value))
	}
	return ""
}

// rawValue is the number of the bits of a change
func rawValue(b []byte) uint64 {
	var x uint64
	for i := len(b) - 1; i >= 0; i-- {
		x = x<<8 | uint64(b[i])
	}
	return x
}

// setWriteMode allows new values, behind the banner
func (v *efuseView) setWriteMode(on bool) {
	v.banner.SetVisible(on)
	if !on {
		v.pending = nil
	}
	v.fill()
}

// edit takes a new value of a field, in write mode
func (v *efuseView) edit(row int) {
	if !v.write.Checked() || row < 0 || row >= len(v.rows) || v.rows[row].field < 0 {
		return
	}
	val := v.dump.Values[v.rows[row].field]
	f := val.Field
	switch {
	case val.WriteProtected:
		ui.ShowMessageBox(v.c.Form().Panel(), f.Name, T().FieldProtected)
		return
	case f.Bits > 64:
		v.keyDialog(val.Field.Name, f.Desc, f.Block, f.Bits, val.Text)
		return
	}
	ui.ShowInputDialog(v.c.Form().Panel(), ui.InputDialog{
		Title:  f.Name,
		Prompt: fmt.Sprintf(T().EFusePrompt, f.Desc, f.Bits, val.Text),
		Value:  fmt.Sprintf("0x%X", val.Uint()),
		Validate: func(text string) error {
			_, err := parseFuse(text, f.Bits)
			return err
		},
	}, func(text string) {
		n, _ := parseFuse(text, f.Bits)
		v.setPending(f.Name, n, val.Uint())
	})
}

// parseFuse reads a value of a field of bits bits
func parseFuse(text string, bits int) (uint64, error) {
	n, err := strconv.ParseUint(strings.TrimSpace(text), 0, 64)
	if err != nil {
		return 0, fmt.Errorf("%s", T().BadFuseValue)
	}
	if bits < 64 && n >= 1<<uint(bits) {
		return 0, fmt.Errorf(T().FuseTooLarge, bits)
	}
	return n, nil
}

// setPending keeps the new value of a field; its old value drops it
func (v *efuseView) setPending(name string, n, old uint64) {
	var kept []ops.EFuseChange
	for _, c := range v.pending {
		if c.Field != name {
			kept = append(kept, c)
		}
	}
	if n != old {
		kept = append(kept, ops.EFuseUint(name, n))
	}
	v.pending = kept
	v.fill()
}

// review checks the changes and asks for the confirmation word
func (v *efuseView) review() {
	m := v.m
	if len(v.pending) == 0 || m.sess == nil {
		if m.sess == nil {
			ui.ShowMessageBox(v.c.Form().Panel(), T().ToolEfuse, T().ConnectFirst)
		}
		return
	}
	plan, err := m.sess.PlanEFuse(v.dump, v.pending)
	if err != nil {
		text := err.Error()
		if reasons, ok := ops.EFuseRefusal(err); ok {
			text = T().BurnRefused + "\n\n• " + strings.Join(reasons, "\n• ")
		}
		ui.ShowMessageBox(v.c.Form().Panel(), T().Review, text)
		return
	}
	if plan.Empty() {
		ui.ShowMessageBox(v.c.Form().Panel(), T().Review, T().NothingToBurn)
		return
	}
	v.confirm(plan)
}

// confirm shows the plan; Burn is enabled only by the word typed
func (v *efuseView) confirm(plan *ops.EFusePlan) {
	c := &ui.DialogContent{}
	c.InitWidget()
	c.SetCellPadding(6)
	var b strings.Builder
	b.WriteString(T().BurnChanges + "\n")
	for _, ch := range plan.Changes {
		b.WriteString("  • " + chunk(ch, 96, "     ") + "\n")
	}
	fmt.Fprintf(&b, "\n"+T().BurnBlocks+"\n", blocksText(plan.Blocks))
	warn := ui.NewLabel(T().BurnIrreversible)
	warn.SetForegroundColor(colorError.get())
	warn.SetFontBold(true)
	c.AddWidget(0, 0, warn)
	c.AddWidget(1, 0, ui.NewLabel(b.String()))
	if len(plan.Warnings) > 0 {
		w := ui.NewLabel("⚠ " + strings.Join(plan.Warnings, "\n⚠ "))
		w.SetForegroundColor(colorWait.get())
		c.AddWidget(2, 0, w)
	}
	c.AddLabel(3, 0, fmt.Sprintf(T().TypeBurn, ops.BurnWord))
	word := ui.NewTextBox()
	word.SetName("burn-word")
	c.AddWidget(4, 0, word)
	buttons := ui.NewDialogButtonBox()
	burn := buttons.AddButton(T().Burn, ui.ButtonOther, func() {
		typed := word.Text()
		if typed != ops.BurnWord {
			return
		}
		c.RunInParent(func() { v.burn(plan, typed) })
		c.Form().Close()
	})
	burn.SetName("burn-button")
	burn.SetEnabled(false)
	buttons.AddCancel(func() { c.Form().Close() })
	word.OnTextChanged.Connect(func(text string) { burn.SetEnabled(text == ops.BurnWord) })
	c.AddWidget(5, 0, buttons)
	c.OnDialogShow = func() {
		c.Form().SetTitle(T().Burn)
		word.Focus()
	}
	v.c.Form().Panel().ShowDialog(c)
	v.confirmDlg = c
}

func blocksText(blocks []int) string {
	var s []string
	for _, b := range blocks {
		s = append(s, fmt.Sprintf("BLOCK%d", b))
	}
	return strings.Join(s, ", ")
}

// burn burns the plan, logs it, shows what was done and reads again. A chip
// that is burned under the ROM loader only is offered a reconnection.
func (v *efuseView) burn(plan *ops.EFusePlan, word string) {
	m := v.m
	var res *ops.EFuseResult
	m.runProgress(T().Burn, T().Burning, false, func(ctx context.Context, s *ops.Session, progress func(int, int)) error {
		var err error
		res, err = s.BurnEFuse(ctx, plan, word)
		return err
	}, func(err error) {
		m.logOp(fmt.Sprintf(T().OpBurn, strings.Join(plan.Changes, "; ")), err)
		if err != nil && strings.Contains(err.Error(), "without the stub") {
			ask(m.Form, T().Burn, err.Error()+"\n\n"+T().ReconnectNoStubAsk,
				choice{T().ReconnectNoStub, ui.ButtonOther, func() { m.reconnect(true) }},
				choice{T().Cancel, ui.ButtonReject, nil})
			return
		}
		if res != nil {
			m.showText(T().BurnResult, burnText(res, err), "efuse-burn.txt")
		} else if err != nil {
			m.showError(T().Burn, err)
		}
		v.pending = nil
		v.reread()
	})
}

// burnText tells what a burn did, block by block
func burnText(res *ops.EFuseResult, err error) string {
	var b strings.Builder
	if err != nil {
		b.WriteString(T().OpFailed + ": " + err.Error() + "\n\n")
	} else {
		b.WriteString(T().Burned + "\n\n")
	}
	for _, st := range res.Stages {
		what := fmt.Sprintf("BLOCK%d", st.Block)
		if st.WRDis {
			what += " (WR_DIS)"
		}
		state := "OK"
		switch {
		case st.Skipped:
			state = T().Skipped
		case !st.OK:
			state = T().OpFailed + " " + st.Err
		}
		if st.CodingError {
			state += " · " + T().CodingError
		}
		fmt.Fprintf(&b, "%-18s %s (%d %s)\n", what, state, st.Attempts, T().Attempts)
	}
	for _, n := range res.Notes {
		b.WriteString("\n" + n)
	}
	return b.String()
}

// reread reads the eFuses again into the window
func (v *efuseView) reread() {
	m := v.m
	var d *ops.EFuseDump
	m.run(true, func(ctx context.Context, s *ops.Session) error {
		var err error
		d, err = s.ReadEFuse(ctx)
		return err
	}, func(err error) {
		if err != nil {
			m.showError(T().ToolEfuse, err)
			return
		}
		v.dump = d
		v.fill()
	})
}

// text is the fields shown, for the clipboard
func (v *efuseView) text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s\n", T().Coding, v.dump.CodingScheme)
	for _, r := range v.rows {
		if r.field < 0 {
			fmt.Fprintf(&b, "\n%s:\n", r.header)
			continue
		}
		val := v.dump.Values[r.field]
		fmt.Fprintf(&b, "  %-32s %s\n", val.Field.Name, val.Text)
	}
	return b.String()
}

// chunk breaks a long line (the bits of a key) into lines of n characters
func chunk(s string, n int, indent string) string {
	r := []rune(s)
	var b strings.Builder
	for len(r) > n {
		b.WriteString(string(r[:n]) + "\n" + indent)
		r = r[n:]
	}
	b.WriteString(string(r))
	return b.String()
}

// banner is a line of white text on red: the write mode
type banner struct {
	ui.Widget
	text func() string
}

func newBanner(text func() string) *banner {
	b := &banner{text: text}
	b.InitWidget()
	b.SetMinHeight(ui.ThemeControlHeight() + 8)
	b.SetXExpandable(true)
	b.SetOnPaint(func(cnv *ui.Canvas) {
		cnv.FillRoundedRectAA(0, 0, b.Width(), b.Height(), 4, colorError.get())
		cnv.SetFontFamily(ui.FontFamilySans)
		cnv.SetFontSize(ui.ThemeFontSize())
		cnv.SetFontBold(true)
		cnv.SetColor(hex("#ffffff"))
		cnv.SetVAlign(ui.VAlignCenter)
		cnv.SetHAlign(ui.HAlignLeft)
		cnv.DrawText(10, 0, b.Width()-20, b.Height(), b.text())
	})
	return b
}

// keyDialog takes the bytes of a field longer than 64 bits (a key, a block
// of user data): typed in hex, or the raw content of a file of exactly the
// field's length. The bytes go to the field as they are: the first byte
// is its first 8 bits, so the first byte is the low byte of the block's
// first word.
func (v *efuseView) keyDialog(name, desc string, block, bits int, now string) {
	n := (bits + 7) / 8
	c := &ui.DialogContent{}
	c.InitWidget()
	c.SetCellPadding(6)
	c.AddWidget(0, 0, ui.NewLabel(fmt.Sprintf(T().KeyPrompt, desc, n, bits)))
	box := ui.NewTextBox()
	box.SetName("key-hex")
	box.SetMinWidth(560)
	box.SetFontFamily(ui.FontFamilyMono)
	c.AddWidget(1, 0, box)
	preview := ui.NewLabel("")
	preview.SetFontFamily(ui.FontFamilyMono)
	c.AddWidget(2, 0, preview)
	errLabel := ui.NewLabel("")
	errLabel.SetForegroundColor(colorError.get())
	c.AddWidget(3, 0, errLabel)
	show := func() {
		b, err := parseKey(box.Text(), n)
		if err != nil {
			preview.SetText(fmt.Sprintf(T().KeyNow, now))
			return
		}
		preview.SetText(fmt.Sprintf(T().KeyPreview, block, keyWords(b)))
	}
	box.OnTextChanged.Connect(func(string) {
		errLabel.SetText("")
		show()
	})
	buttons := ui.NewDialogButtonBox()
	buttons.AddButton(T().FromFile, ui.ButtonOther, func() {
		v.m.Form.ShowOpenFileDialog(ui.OpenFileDialogOptions{Title: name}, func(paths []string, err error) {
			if err != nil || len(paths) == 0 {
				return
			}
			data, err := os.ReadFile(paths[0])
			if err == nil && len(data) != n {
				err = fmt.Errorf(T().KeyLength, filepath.Base(paths[0]), len(data), n)
			}
			if err != nil {
				errLabel.SetText(err.Error())
				return
			}
			box.SetText(spacedHex(data))
			errLabel.SetText("")
		})
	})
	buttons.AddOK(func() {
		b, err := parseKey(box.Text(), n)
		if err != nil {
			errLabel.SetText(err.Error())
			return
		}
		c.RunInParent(func() { v.setPendingRaw(name, b) })
		c.Form().Close()
	})
	buttons.AddCancel(func() { c.Form().Close() })
	c.AddWidget(4, 0, buttons)
	c.OnDialogShow = func() {
		c.Form().SetTitle(name)
		box.Focus()
	}
	show()
	v.c.Form().Panel().ShowDialog(c)
	v.keyDlg = c
}

// parseKey reads n bytes of hex, with or without spaces
func parseKey(text string, n int) ([]byte, error) {
	h := strings.NewReplacer(" ", "", "\n", "", "\t", "", ":", "", "0x", "").Replace(text)
	b, err := hexenc.DecodeString(h)
	if err != nil {
		return nil, fmt.Errorf("%s", T().BadBlob)
	}
	if len(b) != n {
		return nil, fmt.Errorf(T().KeyBytes, len(b), n)
	}
	return b, nil
}

// spacedHex writes bytes as "00 11 22"
func spacedHex(b []byte) string {
	var s strings.Builder
	for i, x := range b {
		if i > 0 {
			s.WriteByte(' ')
		}
		fmt.Fprintf(&s, "%02X", x)
	}
	return s.String()
}

// keyWords are the words of the block as its registers will read them
func keyWords(b []byte) string {
	var s []string
	for i := 0; i+4 <= len(b) && i < 32; i += 4 {
		s = append(s, fmt.Sprintf("%08X", uint32(b[i])|uint32(b[i+1])<<8|uint32(b[i+2])<<16|uint32(b[i+3])<<24))
	}
	return strings.Join(s, " ")
}

// setPendingRaw keeps the new bytes of a field
func (v *efuseView) setPendingRaw(name string, b []byte) {
	var kept []ops.EFuseChange
	for _, c := range v.pending {
		if c.Field != name {
			kept = append(kept, c)
		}
	}
	v.pending = append(kept, ops.EFuseChange{Field: name, Value: b})
	v.fill()
}
