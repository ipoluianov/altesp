package forms

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"github.com/ipoluianov/altesp/internal/ops"
	"github.com/ipoluianov/nui/ui"
)

// editRow is a row of the partition table editor: as typed, the offset
// may be empty (placed after the row before it)
type editRow struct {
	Name          string
	Type, SubType byte
	Offset, Size  string
	Encrypted     bool
}

// partEditor is the visual editor of a partition table: the rows, the
// fields of the row chosen, the layout of the flash and the problems
type partEditor struct {
	m         *MainForm
	rows      []editRow
	tableAddr uint32
	flashSize int

	list     *ui.ListView
	model    *ui.FuncModel
	name     *ui.TextBox
	typ      *ui.ComboBox
	sub      *ui.ComboBox
	offset   *ui.TextBox
	size     *ui.TextBox
	enc      *ui.Checkbox
	problems *ui.Label
	layout   *layoutBar
	writeBtn *ui.Button
	filling  bool // the fields are being filled from the row
}

// The subtypes offered for each type
var (
	appSubtypes  = []byte{0x00, 0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1A, 0x1B, 0x1C, 0x1D, 0x1E, 0x1F, 0x20}
	dataSubtypes = []byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x80, 0x81, 0x82, 0x83}
)

// The columns of the editor's rows
var editCols = []int{0, 140, 200, 290, 400, 500}

// defaultTable is the "Single factory app, no OTA" table of ESP-IDF
func defaultTable() []editRow {
	return []editRow{
		{Name: "nvs", Type: 0x01, SubType: 0x02, Offset: "0x9000", Size: "0x6000"},
		{Name: "phy_init", Type: 0x01, SubType: 0x01, Offset: "0xF000", Size: "0x1000"},
		{Name: "factory", Type: 0x00, SubType: 0x00, Offset: "0x10000", Size: "1M"},
	}
}

func rowsOf(parts []ops.Partition) []editRow {
	var rows []editRow
	for _, p := range parts {
		rows = append(rows, editRow{Name: p.Name, Type: p.Type, SubType: p.SubType,
			Offset: fmt.Sprintf("0x%X", p.Offset), Size: fmt.Sprintf("0x%X", p.Size), Encrypted: p.Flags&1 != 0})
	}
	return rows
}

func newPartEditor(m *MainForm) *partEditor {
	e := &partEditor{m: m, tableAddr: 0x8000, flashSize: 4 << 20}
	parts, addr := m.shownPartitions()
	if addr != 0 {
		e.tableAddr = addr
	}
	switch {
	case m.image != nil && len(m.image.Data) > 0 && len(parts) > 0:
		e.flashSize = len(m.image.Data)
	case m.hasBoard && m.board.FlashSize > 0:
		e.flashSize = m.board.FlashSize
	}
	if len(parts) > 0 {
		e.rows = rowsOf(parts)
	} else {
		e.rows = defaultTable()
	}
	return e
}

// partitions resolves the rows: the empty offsets placed after the row
// before (aligned: 64 KB for apps, 4 KB for data), the sizes read
func (e *partEditor) partitions() ([]ops.Partition, []string) {
	var parts []ops.Partition
	var problems []string
	next := e.tableAddr + 0x1000
	for i, r := range e.rows {
		p := ops.Partition{Name: r.Name, Type: r.Type, SubType: r.SubType}
		if r.Encrypted {
			p.Flags = 1
		}
		size, err := ops.ParseNumber(r.Size)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%d %s: %v", i+1, r.Name, err))
		}
		p.Size = size
		if strings.TrimSpace(r.Offset) == "" {
			align := uint32(0x1000)
			if r.Type == 0x00 {
				align = 0x10000
			}
			p.Offset = (next + align - 1) / align * align
		} else if p.Offset, err = ops.ParseNumber(r.Offset); err != nil {
			problems = append(problems, fmt.Sprintf("%d %s: %v", i+1, r.Name, err))
		}
		next = p.Offset + p.Size
		parts = append(parts, p)
	}
	return parts, append(problems, ops.CheckPartitions(parts, e.tableAddr, e.flashSize)...)
}

// show shows the editor in a dialog
func (e *partEditor) show() {
	m := e.m
	c := &ui.DialogContent{}
	c.InitWidget()
	c.SetCellPadding(6)
	info := secondaryLabel(func() string {
		return fmt.Sprintf(T().EditorInfo, e.tableAddr, ops.SizeText(e.flashSize))
	})
	c.AddWidget(0, 0, info)

	tools := ui.NewHBox()
	tools.SetCellPadding(6)
	tools.Add(textButton(func() string { return T().AddRow }, e.add))
	tools.Add(textButton(func() string { return T().RemoveRow }, e.remove))
	tools.Add(textButton(func() string { return "↑" }, func() { e.move(-1) }))
	tools.Add(textButton(func() string { return "↓" }, func() { e.move(1) }))
	tools.Add(textButton(func() string { return T().DefaultTable }, func() {
		e.rows = defaultTable()
		e.changed(0)
	}))
	tools.Add(textButton(func() string { return T().ImportCSV }, e.importCSV))
	tools.AddStretch()
	c.AddWidget(1, 0, tools)

	c.AddWidget(2, 0, newColumns(editCols, func() []string {
		return []string{T().ColName, T().ColType, T().ColSubtype, T().ColAddress, T().ColSize, T().ColFlags}
	}))
	e.model = ui.NewFuncModel(func() int { return len(e.rows) }, 1, func(row, col int) string { return e.rows[row].Name })
	e.list = ui.NewListView()
	e.list.SetName("table-rows")
	e.list.SetModel(e.model)
	e.list.SetMinSize(700, 220)
	e.list.SetItemPainter(e.paintRow)
	e.list.OnSelectionChanged.Connect(func(int) { e.fill() })
	c.AddWidget(3, 0, e.list)

	// The fields of the row chosen
	fields := ui.NewPanel()
	fields.SetPanelPadding(0)
	e.name = ui.NewTextBox()
	e.typ = ui.NewComboBox()
	e.typ.AddItem("app", byte(0x00))
	e.typ.AddItem("data", byte(0x01))
	e.sub = ui.NewComboBox()
	e.offset = ui.NewTextBox()
	e.offset.SetHintFunc(func() string { return T().OffsetAuto })
	e.size = ui.NewTextBox()
	e.enc = ui.NewCheckbox("")
	e.enc.SetTextFunc(func() string { return T().Encrypted })
	labels := []string{T().ColName, T().ColType, T().ColSubtype, T().ColAddress, T().ColSize}
	widgets := []ui.Widgeter{e.name, e.typ, e.sub, e.offset, e.size}
	for i := range labels {
		fields.AddLabel(0, i, labels[i])
		fields.AddWidget(1, i, widgets[i])
	}
	fields.AddWidget(1, len(labels), e.enc)
	c.AddWidget(4, 0, fields)
	for _, tb := range []*ui.TextBox{e.name, e.offset, e.size} {
		tb.OnTextChanged.Connect(func(string) { e.take() })
	}
	e.typ.OnActivated.Connect(func(int) {
		e.fillSubtypes(e.selType(), 0xFF)
		e.take()
	})
	e.sub.OnActivated.Connect(func(int) { e.take() })
	e.enc.OnCheckedChanged.Connect(func(bool) { e.take() })

	e.layout = newLayoutBar(e)
	c.AddWidget(5, 0, e.layout)
	e.problems = ui.NewLabel("")
	c.AddWidget(6, 0, e.problems)

	buttons := ui.NewDialogButtonBox()
	buttons.AddButton(T().SaveCSV, ui.ButtonOther, e.saveCSV)
	buttons.AddButton(T().SaveBinary, ui.ButtonOther, e.saveBinary)
	e.writeBtn = buttons.AddButton(T().WriteToBoard, ui.ButtonOther, func() { e.write(c) })
	buttons.AddButton(T().CloseDiff, ui.ButtonReject, func() { c.Form().Close() })
	c.AddWidget(7, 0, buttons)
	c.OnDialogShow = func() { c.Form().SetTitle(T().ToolPartitionEditor) }
	e.changed(0)
	m.Form.Panel().ShowDialog(c)
}

func (m *MainForm) partitionEditor() {
	newPartEditor(m).show()
}

func (e *partEditor) paintRow(p *ui.ItemPaint) {
	if p.Row >= len(e.rows) {
		return
	}
	r := e.rows[p.Row]
	parts, _ := e.partitions()
	off := r.Offset
	if strings.TrimSpace(off) == "" && p.Row < len(parts) {
		off = fmt.Sprintf("(0x%X)", parts[p.Row].Offset)
	}
	flags := ""
	if r.Encrypted {
		flags = "encrypted"
	}
	pp := ops.Partition{Type: r.Type, SubType: r.SubType}
	paintColumns(p, editCols, []string{r.Name, pp.TypeName(), pp.SubTypeName(), off, r.Size, flags}, 3)
}

// changed shows the rows after a change and selects the row
func (e *partEditor) changed(sel int) {
	e.model.Changed()
	e.list.Update()
	if sel >= 0 && sel < len(e.rows) {
		e.list.SetCurrentRow(sel)
	}
	e.check()
	e.fill()
}

// check shows the problems of the table and the layout
func (e *partEditor) check() {
	_, problems := e.partitions()
	if len(problems) == 0 {
		e.problems.SetForegroundColor(colorOK.get())
		e.problems.SetText(T().TableOK)
	} else {
		e.problems.SetForegroundColor(colorError.get())
		e.problems.SetText("• " + strings.Join(problems, "\n• "))
	}
	if e.writeBtn != nil {
		e.writeBtn.SetEnabled(len(problems) == 0 && e.m.image == nil && e.m.hasSel && !e.m.busy)
	}
	e.layout.Update()
	e.list.Update()
}

// fill puts the row chosen into the fields
func (e *partEditor) fill() {
	i := e.list.CurrentRow()
	if i < 0 || i >= len(e.rows) {
		return
	}
	r := e.rows[i]
	e.filling = true
	e.name.SetText(r.Name)
	e.typ.SetSelectedIndex(int(r.Type))
	e.fillSubtypes(r.Type, r.SubType)
	e.offset.SetText(r.Offset)
	e.size.SetText(r.Size)
	e.enc.SetChecked(r.Encrypted)
	e.filling = false
}

func (e *partEditor) selType() byte {
	t, _ := e.typ.ItemData(e.typ.SelectedIndex()).(byte)
	return t
}

// fillSubtypes offers the subtypes of the type, sub chosen (0xFF: the first)
func (e *partEditor) fillSubtypes(typ, sub byte) {
	list := dataSubtypes
	if typ == 0x00 {
		list = appSubtypes
	}
	e.sub.Clear()
	sel := 0
	for i, s := range list {
		e.sub.AddItem(ops.Partition{Type: typ, SubType: s}.SubTypeName(), s)
		if s == sub {
			sel = i
		}
	}
	e.sub.SetSelectedIndex(sel)
}

// take puts the fields into the row chosen
func (e *partEditor) take() {
	i := e.list.CurrentRow()
	if e.filling || i < 0 || i >= len(e.rows) {
		return
	}
	sub, _ := e.sub.ItemData(e.sub.SelectedIndex()).(byte)
	e.rows[i] = editRow{Name: strings.TrimSpace(e.name.Text()), Type: e.selType(), SubType: sub,
		Offset: strings.TrimSpace(e.offset.Text()), Size: strings.TrimSpace(e.size.Text()), Encrypted: e.enc.Checked()}
	e.model.Changed()
	e.check()
}

func (e *partEditor) add() {
	i := e.list.CurrentRow() + 1
	if i <= 0 || i > len(e.rows) {
		i = len(e.rows)
	}
	r := editRow{Name: fmt.Sprintf("part%d", len(e.rows)), Type: 0x01, SubType: 0x06, Size: "0x1000"}
	e.rows = append(e.rows[:i], append([]editRow{r}, e.rows[i:]...)...)
	e.changed(i)
}

func (e *partEditor) remove() {
	i := e.list.CurrentRow()
	if i < 0 || i >= len(e.rows) {
		return
	}
	e.rows = append(e.rows[:i], e.rows[i+1:]...)
	e.changed(min(i, len(e.rows)-1))
}

func (e *partEditor) move(d int) {
	i := e.list.CurrentRow()
	j := i + d
	if i < 0 || j < 0 || j >= len(e.rows) {
		return
	}
	e.rows[i], e.rows[j] = e.rows[j], e.rows[i]
	e.changed(j)
}

// table returns the table when it has no problems
func (e *partEditor) table() ([]ops.Partition, bool) {
	parts, problems := e.partitions()
	if len(problems) > 0 {
		ui.ShowMessageBox(e.m.Form.Panel(), T().ToolPartitionEditor, T().TableProblems+"\n\n• "+strings.Join(problems, "\n• "))
		return nil, false
	}
	return parts, true
}

func (e *partEditor) saveCSV() {
	parts, ok := e.table()
	if !ok {
		return
	}
	var b bytes.Buffer
	if err := ops.WritePartitionsCSV(&b, parts); err != nil {
		ui.ShowMessageBox(e.m.Form.Panel(), T().SaveCSV, err.Error())
		return
	}
	e.m.saveText(T().SaveCSV, "partitions.csv", b.String())
}

func (e *partEditor) saveBinary() {
	parts, ok := e.table()
	if !ok {
		return
	}
	bin, err := ops.BuildPartitionTable(parts)
	if err != nil {
		ui.ShowMessageBox(e.m.Form.Panel(), T().SaveBinary, err.Error())
		return
	}
	e.m.saveBytes(T().SaveBinary, "partition-table.bin", bin)
}

// importCSV loads the rows from a CSV
func (e *partEditor) importCSV() {
	e.m.Form.ShowOpenFileDialog(ui.OpenFileDialogOptions{
		Title:   T().ImportCSV,
		Filters: []ui.FileDialogFilter{{DisplayName: "CSV", Patterns: []string{"*.csv"}}},
	}, func(paths []string, err error) {
		if err != nil || len(paths) == 0 {
			return
		}
		data, err := os.ReadFile(paths[0])
		var parts []ops.Partition
		if err == nil {
			parts, err = ops.ReadPartitionsCSV(bytes.NewReader(data), e.tableAddr)
		}
		if err != nil {
			ui.ShowMessageBox(e.m.Form.Panel(), T().ImportCSV, err.Error())
			return
		}
		e.rows = rowsOf(parts)
		e.changed(0)
	})
}

// write writes the table to the board after the user saw the warning
func (e *partEditor) write(c *ui.DialogContent) {
	parts, ok := e.table()
	if !ok {
		return
	}
	m := e.m
	ask(m.Form, T().WriteTable, fmt.Sprintf(T().WriteTableAsk, e.tableAddr),
		choice{T().BackupFirst, ui.ButtonOther, func() {
			m.saveRegion(0, e.flashSize, func() { m.parts.writeTable(parts) })
			c.Form().Close()
		}},
		choice{T().WriteTable, ui.ButtonOther, func() {
			m.parts.writeTable(parts)
			c.Form().Close()
		}},
		choice{T().Cancel, ui.ButtonReject, nil})
}

// layoutBar draws the layout of the flash: the table and the partitions,
// in their colors; overlaps and parts past the end in red
type layoutBar struct {
	ui.Widget
	e *partEditor
}

func newLayoutBar(e *partEditor) *layoutBar {
	b := &layoutBar{e: e}
	b.InitWidget()
	b.SetMinHeight(34)
	b.SetXExpandable(true)
	b.SetOnPaint(b.paint)
	return b
}

func (b *layoutBar) paint(cnv *ui.Canvas) {
	pal := ui.CurrentPalette()
	w, h := b.Width(), b.Height()
	cnv.FillFrame(0, 0, w, h, 3, pal.Base, pal.Border)
	total := int64(max(b.e.flashSize, 1))
	x := func(addr uint32) int { return 1 + int(int64(addr)*int64(w-2)/total) }
	cnv.FillRect(x(0), 3, x(b.e.tableAddr)-x(0), h-6, colorMapBoot.get())
	cnv.FillRect(x(b.e.tableAddr), 3, max(2, x(b.e.tableAddr+0x1000)-x(b.e.tableAddr)), h-6, colorMapTable.get())
	parts, _ := b.e.partitions()
	cnv.SetFontFamily(ui.FontFamilySans)
	cnv.SetFontSize(ui.ThemeFontSize() - 2)
	cnv.SetVAlign(ui.VAlignCenter)
	for i, p := range parts {
		x0, x1 := x(p.Offset), x(p.Offset+p.Size)
		col := partitionColor(p).get()
		end := uint64(p.Offset) + uint64(p.Size)
		bad := end > uint64(b.e.flashSize)
		for j, q := range parts {
			if j != i && p.Offset < q.Offset+q.Size && q.Offset < uint32(end) {
				bad = true
			}
		}
		if bad {
			col = colorError.get()
		}
		cnv.FillRect(x0, 3, max(2, x1-x0-1), h-6, col)
		if x1-x0 > 40 {
			cnv.SetColor(pal.Base)
			cnv.DrawText(x0+4, 0, x1-x0-6, h, p.Name)
		}
	}
}
