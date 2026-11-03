package forms

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ipoluianov/altesp/internal/ops"
	"github.com/ipoluianov/nui/ui"
)

// partRow is a row of the partition table with what is known of its app
type partRow struct {
	P      ops.Partition
	Active bool
	App    *ops.AppLine
}

// PartitionsTab is the page of the Partitions tab: the partition table of
// the board or of the image, with the applications in it
type PartitionsTab struct {
	ui.Panel
	m *MainForm

	rows     []partRow
	imageBar *ui.HBox
	list     *ui.ListView
	model    *ui.FuncModel
	info     *ui.Label
	refresh  *ui.Button
	importB  *ui.Button

	// The table, or the viewer of a partition in its place
	tableBox  *ui.Panel
	viewerBox *ui.Panel
	viewer    partViewer
	source    string // what the table is of: "board" or the image's path
}

// partViewer is the editor of a partition's content shown in the tab
type partViewer interface {
	ui.Widgeter
	// dirty tells whether there are changes not written
	dirty() bool
	// dropped is called when the files are dropped on the window
	dropped(files []string) bool
}

// The columns of the table, from the left
var partCols = []int{0, 130, 190, 280, 380, 460}

func newPartitionsTab(m *MainForm) *PartitionsTab {
	t := &PartitionsTab{m: m}
	t.InitWidget()
	t.SetPanelPadding(6)
	t.SetCellPadding(6)
	t.imageBar = m.newImageBar()
	t.AddWidget(0, 0, t.imageBar)

	bar := ui.NewHBox()
	bar.SetCellPadding(6)
	t.refresh = textButton(func() string { return T().Refresh }, t.reload)
	bar.Add(t.refresh)
	bar.Add(textButton(func() string { return T().ExportCSV }, t.exportCSV))
	t.importB = textButton(func() string { return T().ImportCSV }, t.importCSV)
	bar.Add(t.importB)
	t.info = ui.NewLabel("")
	t.info.SetXExpandable(true)
	t.info.SetForegroundColor(colorSecondary.get())
	ui.OnThemeChanged.Connect(func(bool) { t.info.SetForegroundColor(colorSecondary.get()) })
	bar.Add(t.info)
	t.tableBox = ui.NewPanel()
	t.tableBox.SetPanelPadding(0)
	t.tableBox.SetCellPadding(6)
	t.tableBox.AddWidget(0, 0, bar)

	t.tableBox.AddWidget(1, 0, newPartHeader())
	t.model = ui.NewFuncModel(func() int { return len(t.rows) }, 1, func(row, col int) string { return t.rows[row].P.Name })
	t.list = ui.NewListView()
	t.list.SetName("partitions")
	t.list.SetModel(t.model)
	t.list.SetItemPainter(t.paintRow)
	t.list.SetContextMenu(t.rowMenu())
	t.list.OnMouseDown.Connect(func(e *ui.MouseEvent) {
		if e.Button == ui.MouseButtonRight {
			if row := t.list.RowAt(e.X, e.Y-t.list.ScrollY()); row >= 0 {
				t.list.SetCurrentRow(row)
			}
		}
	})
	t.list.OnActivated.Connect(t.open)
	t.tableBox.AddWidget(2, 0, t.list)
	t.AddWidget(1, 0, t.tableBox)
	t.viewerBox = ui.NewPanel()
	t.viewerBox.SetPanelPadding(0)
	t.viewerBox.SetVisible(false)
	t.AddWidget(2, 0, t.viewerBox)
	t.sync()
	return t
}

// sync shows the table of the image or of the board
func (t *PartitionsTab) sync() {
	m := t.m
	// Another image or board: the editor of the last one goes
	key := "board"
	if m.image != nil {
		key = m.image.Path
	}
	if key != t.source && t.viewer != nil {
		t.viewer = nil
		t.viewerBox.RemoveAllWidgets()
		t.viewerBox.SetVisible(false)
		t.tableBox.SetVisible(true)
	}
	t.source = key
	m.showImageBar(t.imageBar)
	parts, _ := m.shownPartitions()
	var sum ops.Summary
	switch {
	case m.image != nil:
		sum = m.image.Summary()
	case m.hasBoard:
		sum = m.board.Summary()
	}
	apps := map[string]*ops.AppLine{}
	for i := range sum.Apps {
		apps[sum.Apps[i].Partition] = &sum.Apps[i]
	}
	active := map[string]bool{}
	for _, l := range sum.Partitions {
		active[l.Name] = l.Active
	}
	t.rows = nil
	for _, p := range parts {
		t.rows = append(t.rows, partRow{P: p, Active: active[p.Name], App: apps[p.Name]})
	}
	// An image of one partition (NVS, a file system) is a row of its own
	if m.image != nil && len(parts) == 0 {
		if sub, ok := imageSubtypes[m.image.Kind]; ok {
			t.rows = append(t.rows, partRow{P: ops.Partition{Name: filepath.Base(m.image.Path), Type: 0x01,
				SubType: sub, Size: uint32(len(m.image.Data))}})
		}
	}
	t.model.Changed()
	t.list.Update()
	board := m.image == nil
	t.refresh.SetEnabled(board && m.hasBoard && !m.busy)
	t.importB.SetEnabled(board && m.hasBoard && !m.busy)
	switch {
	case m.image != nil && len(parts) == 0:
		t.info.SetText(T().NoTableImage)
	case m.image != nil:
		t.info.SetText("")
	case !m.hasBoard:
		t.info.SetText(T().PartitionsHint)
	case len(parts) == 0:
		t.info.SetText(T().NoTableBoard)
	default:
		t.info.SetText(fmt.Sprintf(T().TableAt, m.board.PartitionTableAddr))
	}
}

func newPartHeader() ui.Widgeter {
	h := &ui.Widget{}
	h.InitWidget()
	h.SetMinHeight(ui.ThemeControlHeight() - 4)
	h.SetXExpandable(true)
	h.SetOnPaint(func(cnv *ui.Canvas) {
		cnv.SetFontFamily(ui.FontFamilySans)
		cnv.SetFontSize(ui.ThemeFontSize() - 1)
		cnv.SetColor(colorSecondary.get())
		cnv.SetVAlign(ui.VAlignCenter)
		names := []string{T().ColName, T().ColType, T().ColSubtype, T().ColAddress, T().ColSize, T().ColApp}
		for i, n := range names {
			cnv.DrawText(partCols[i]+8, 0, 200, h.Height(), n)
		}
	})
	return h
}

// paintRow draws a partition: its color, the columns, the app in it and
// the mark of the one that boots
func (t *PartitionsTab) paintRow(p *ui.ItemPaint) {
	if p.Row >= len(t.rows) {
		return
	}
	r := t.rows[p.Row]
	pal := ui.CurrentPalette()
	cnv := p.Canvas
	text := pal.Text
	if p.Selected {
		back := pal.Selection
		if p.Focused {
			back, text = pal.Highlight, pal.HighlightedText
		}
		cnv.FillRect(1, 0, p.Width-2, p.Height, back)
	}
	cnv.FillRect(2, 3, 4, p.Height-6, partitionColor(r.P).get())
	cnv.SetFontFamily(ui.FontFamilySans)
	cnv.SetFontSize(ui.ThemeFontSize())
	cnv.SetColor(text)
	cnv.SetVAlign(ui.VAlignCenter)
	cnv.SetHAlign(ui.HAlignLeft)
	h := p.Height
	col := func(i int, s string) {
		end := p.Width
		if i+1 < len(partCols) {
			end = partCols[i+1]
		}
		cnv.DrawText(partCols[i]+8, 0, end-partCols[i]-10, h, s)
	}
	col(0, r.P.Name)
	col(1, r.P.TypeName())
	col(2, r.P.SubTypeName())
	cnv.SetFontFamily(ui.FontFamilyMono)
	col(3, fmt.Sprintf("0x%06X", r.P.Offset))
	cnv.SetFontFamily(ui.FontFamilySans)
	col(4, ops.SizeText(int(r.P.Size)))
	app := ""
	if r.App != nil {
		app = appText(*r.App)
		if r.App.IDFVersion != "" {
			app += " · IDF " + r.App.IDFVersion
		}
		if r.App.Built != "" {
			app += " · " + r.App.Built
		}
	} else if r.P.IsApp() {
		app = T().EmptyApp
	}
	x := partCols[5] + 8
	if r.Active {
		// The badge of the app that boots
		label := T().Boots
		w, _, _ := ui.MeasureText(ui.FontFamilySans, ui.ThemeFontSize()-1, label)
		cnv.FillFrame(x, 4, w+12, h-8, 4, colorOK.get(), colorOK.get())
		cnv.SetFontSize(ui.ThemeFontSize() - 1)
		cnv.SetColor(pal.Base)
		cnv.DrawText(x+6, 0, w+2, h, label)
		cnv.SetFontSize(ui.ThemeFontSize())
		cnv.SetColor(text)
		x += w + 20
	}
	cnv.DrawText(x, 0, p.Width-x-6, h, app)
}

// current is the partition of the row selected
func (t *PartitionsTab) current() (partRow, bool) {
	i := t.list.CurrentRow()
	if i < 0 || i >= len(t.rows) {
		return partRow{}, false
	}
	return t.rows[i], true
}

// open shows a partition: the details of an app, a viewer, or its bytes
func (t *PartitionsTab) open(row int) {
	if row < 0 || row >= len(t.rows) {
		return
	}
	r := t.rows[row]
	switch {
	case r.App != nil:
		t.details(r)
	case t.openViewer(r.P):
	default:
		t.m.memory.show(r.P.Offset, r.P.Size)
	}
}

// details shows all that is known of the application in the partition
func (t *PartitionsTab) details(r partRow) {
	a := r.App
	var b strings.Builder
	line := func(name, value string) {
		if value != "" {
			fmt.Fprintf(&b, "%s: %s\n", name, value)
		}
	}
	line(T().ColName, r.P.Name+fmt.Sprintf(" (0x%06X, %s)", r.P.Offset, ops.SizeText(int(r.P.Size))))
	line(T().AppProject, a.Project)
	line(T().AppVersion, a.Version)
	line("ESP-IDF", a.IDFVersion)
	line(T().AppBuilt, a.Built)
	if a.SecureVersion != 0 {
		line(T().AppSecureVersion, fmt.Sprint(a.SecureVersion))
	}
	line(T().AppELF, a.ELFSHA256)
	if r.Active {
		b.WriteString("\n" + T().AppBoots + "\n")
	}
	ui.ShowMessageBox(t.m.Form.Panel(), a.Project, b.String())
}

// rowMenu is the context menu of a partition
func (t *PartitionsTab) rowMenu() *ui.ContextMenu {
	menu := ui.NewContextMenu(t.list)
	item := func(text func() string, f func()) *ui.ContextMenuItem {
		it := menu.AddItem("", f)
		it.SetTextFunc(text)
		return it
	}
	save := item(func() string { return T().SavePartition }, t.savePartition)
	write := item(func() string { return T().WritePartition }, t.writePartition)
	erase := item(func() string { return T().ErasePartition }, t.erasePartition)
	activate := item(func() string { return T().MakeActive }, t.makeActive)
	hexItem := item(func() string { return T().OpenInHex }, func() {
		if r, ok := t.current(); ok {
			t.m.memory.show(r.P.Offset, r.P.Size)
		}
	})
	menu.AddSeparator()
	item(func() string { return T().ExportCSV }, t.exportCSV)
	imp := item(func() string { return T().ImportCSV }, t.importCSV)
	refresh := item(func() string { return T().Refresh }, t.reload)
	menu.OnShow.Connect(func(*ui.ContextMenu) {
		r, ok := t.current()
		board := t.m.image == nil && t.m.hasBoard && !t.m.busy
		save.SetEnabled(ok && (t.m.image != nil || board))
		write.SetEnabled(ok && board)
		erase.SetEnabled(ok && board)
		activate.SetEnabled(ok && board && r.P.IsOTA() && !r.Active)
		hexItem.SetEnabled(ok)
		imp.SetEnabled(board)
		refresh.SetEnabled(board)
	})
	return menu
}

// reload reads the partition table of the board again
func (t *PartitionsTab) reload() {
	t.m.run(true, func(ctx context.Context, s *ops.Session) error {
		s.RefreshPartitions(ctx)
		return nil
	}, func(err error) { t.m.showError(T().Refresh, err) })
}

func (t *PartitionsTab) savePartition() {
	if r, ok := t.current(); ok {
		t.m.saveRegion(r.P.Offset, int(r.P.Size), nil)
	}
}

// writePartition writes a file at the start of the partition, after the
// user saw what is lost; the rest of it is erased when ticked
func (t *PartitionsTab) writePartition() {
	r, ok := t.current()
	if !ok {
		return
	}
	t.m.Form.ShowOpenFileDialog(ui.OpenFileDialogOptions{Title: T().WritePartition}, func(paths []string, err error) {
		if err != nil || len(paths) == 0 {
			return
		}
		data, err := os.ReadFile(paths[0])
		if err != nil {
			ui.ShowMessageBox(t.m.Form.Panel(), T().WritePartition, err.Error())
			return
		}
		if len(data) > int(r.P.Size) {
			ui.ShowMessageBox(t.m.Form.Panel(), T().WritePartition,
				fmt.Sprintf(T().TooLargeForPart, filepath.Base(paths[0]), ops.SizeText(len(data)), r.P.Name, ops.SizeText(int(r.P.Size))))
			return
		}
		eraseRest := ui.NewCheckbox("")
		eraseRest.SetTextFunc(func() string { return T().EraseRest })
		eraseRest.SetChecked(true)
		text := fmt.Sprintf(T().WritePartAsk, filepath.Base(paths[0]), ops.SizeText(len(data)), r.P.Name, r.P.Offset) + t.lost(r)
		write := func() {
			erase := eraseRest.Checked()
			t.m.runProgress(T().WritePartition, T().Writing, true, func(ctx context.Context, s *ops.Session, progress func(int, int)) error {
				return s.WritePartition(ctx, r.P.Name, data, erase, progress)
			}, func(err error) {
				t.m.logOp(fmt.Sprintf(T().OpWritePart, filepath.Base(paths[0]), r.P.Name), err)
				if err != nil {
					t.m.showError(T().WritePartition, err)
					return
				}
				t.m.status.ShowMessage(fmt.Sprintf(T().PartWritten, r.P.Name))
			})
		}
		askWith(t.m.Form, T().WritePartition, text, eraseRest,
			choice{T().BackupFirst, ui.ButtonOther, func() { t.m.saveRegion(r.P.Offset, int(r.P.Size), write) }},
			choice{T().Write, ui.ButtonOther, write},
			choice{T().Cancel, ui.ButtonReject, nil})
	})
}

// lost tells what is in the partition now
func (t *PartitionsTab) lost(r partRow) string {
	if r.App == nil {
		return ""
	}
	s := "\n\n" + T().EraseApp + " " + appText(*r.App)
	if r.Active {
		s += " (" + T().Boots + ")"
	}
	return s
}

// erasePartition erases the partition after the user saw what is lost
func (t *PartitionsTab) erasePartition() {
	r, ok := t.current()
	if !ok {
		return
	}
	text := fmt.Sprintf(T().ErasePartAsk, r.P.Name, r.P.Offset, ops.SizeText(int(r.P.Size))) + t.lost(r)
	t.m.confirmDanger(T().ErasePartition, text, T().Erase, r.P.Offset, int(r.P.Size), func() {
		t.m.runProgress(T().ErasePartition, T().Erasing, true, func(ctx context.Context, s *ops.Session, progress func(int, int)) error {
			return s.ErasePartition(ctx, r.P.Name)
		}, func(err error) {
			t.m.logOp(fmt.Sprintf(T().OpErasePart, r.P.Name), err)
			if err != nil {
				t.m.showError(T().ErasePartition, err)
				return
			}
			t.m.status.ShowMessage(fmt.Sprintf(T().PartErased, r.P.Name))
		})
	})
}

// makeActive makes the bootloader start the OTA slot next
func (t *PartitionsTab) makeActive() {
	r, ok := t.current()
	if !ok {
		return
	}
	parts, _ := t.m.shownPartitions()
	slot := -1
	for i, p := range ops.OTASlots(parts) {
		if p.Name == r.P.Name {
			slot = i
		}
	}
	if slot < 0 {
		return
	}
	t.m.runWrite(func(ctx context.Context, s *ops.Session) error {
		return s.SetActiveOTA(ctx, slot)
	}, func(err error) {
		t.m.logOp(fmt.Sprintf(T().OpOTA, r.P.Name), err)
		if err != nil {
			t.m.showError(T().MakeActive, err)
			return
		}
		t.m.status.ShowMessage(fmt.Sprintf(T().NowBoots, r.P.Name))
	})
}

// exportCSV saves the table in the CSV of ESP-IDF
func (t *PartitionsTab) exportCSV() {
	parts, _ := t.m.shownPartitions()
	if len(parts) == 0 {
		t.m.status.ShowMessage(T().NoTableBoard)
		return
	}
	t.m.Form.ShowSaveFileDialog(ui.SaveFileDialogOptions{
		Title:           T().ExportCSV,
		DefaultFileName: t.m.dumpName() + "_partitions.csv",
		Filters:         []ui.FileDialogFilter{{DisplayName: "CSV", Patterns: []string{"*.csv"}}},
	}, func(path string, err error) {
		if err != nil || path == "" {
			return
		}
		if err := t.writeCSV(path, parts); err != nil {
			ui.ShowMessageBox(t.m.Form.Panel(), T().ExportCSV, err.Error())
			return
		}
		t.m.status.ShowMessage(fmt.Sprintf(T().Saved, path))
	})
}

func (t *PartitionsTab) writeCSV(path string, parts []ops.Partition) error {
	var b bytes.Buffer
	if err := ops.WritePartitionsCSV(&b, parts); err != nil {
		return err
	}
	return os.WriteFile(path, b.Bytes(), 0o644)
}

// importCSV reads a table from a CSV, shows it with its problems, and
// writes it to the board after the user agreed
func (t *PartitionsTab) importCSV() {
	t.m.Form.ShowOpenFileDialog(ui.OpenFileDialogOptions{
		Title:   T().ImportCSV,
		Filters: []ui.FileDialogFilter{{DisplayName: "CSV", Patterns: []string{"*.csv"}}, {DisplayName: T().AllFiles, Patterns: []string{"*"}}},
	}, func(paths []string, err error) {
		if err == nil && len(paths) > 0 {
			t.importFrom(paths[0])
		}
	})
}

func (t *PartitionsTab) importFrom(path string) {
	addr := t.m.board.PartitionTableAddr
	if addr == 0 {
		addr = 0x8000
	}
	f, err := os.Open(path)
	if err != nil {
		ui.ShowMessageBox(t.m.Form.Panel(), T().ImportCSV, err.Error())
		return
	}
	parts, err := ops.ReadPartitionsCSV(f, addr)
	f.Close()
	if err != nil {
		ui.ShowMessageBox(t.m.Form.Panel(), T().ImportCSV, err.Error())
		return
	}
	var b strings.Builder
	for _, p := range parts {
		fmt.Fprintf(&b, "%s — %s/%s, 0x%X, %s\n", p.Name, p.TypeName(), p.SubTypeName(), p.Offset, ops.SizeText(int(p.Size)))
	}
	problems := ops.CheckPartitions(parts, addr, t.m.board.FlashSize)
	table := b.String()
	if len(problems) > 0 {
		ask(t.m.Form, T().ImportCSV, T().TableProblems+"\n\n"+table+"\n• "+strings.Join(problems, "\n• "),
			choice{T().Cancel, ui.ButtonReject, nil})
		return
	}
	ask(t.m.Form, T().ImportCSV, fmt.Sprintf(T().WriteTableAsk, addr)+"\n\n"+table,
		choice{T().BackupFirst, ui.ButtonOther, func() { t.m.saveRegion(0, t.m.board.FlashSize, func() { t.writeTable(parts) }) }},
		choice{T().WriteTable, ui.ButtonOther, func() { t.writeTable(parts) }},
		choice{T().Cancel, ui.ButtonReject, nil})
}

func (t *PartitionsTab) writeTable(parts []ops.Partition) {
	t.m.runWrite(func(ctx context.Context, s *ops.Session) error {
		return s.WritePartitionTable(ctx, parts)
	}, func(err error) {
		t.m.logOp(fmt.Sprintf(T().OpTable, len(parts)), err)
		if err != nil {
			t.m.showError(T().ImportCSV, err)
			return
		}
		t.m.status.ShowMessage(T().TableWritten)
	})
}

// imageSubtypes are the data subtypes of the image kinds that are one
// partition
var imageSubtypes = map[string]byte{"NVS": 0x02, "LittleFS": 0x83, "SPIFFS": 0x82, "FAT": 0x81}

// openViewer opens the editor of the partition's content: NVS, a file
// system; false when there is none for it
func (t *PartitionsTab) openViewer(p ops.Partition) bool {
	if p.Type != 0x01 {
		return false
	}
	switch p.SubTypeName() {
	case "nvs":
		t.partData(p, func(data []byte) { t.showViewer(newNVSView(t, p, data)) })
	case "fat", "spiffs", "littlefs":
		t.partData(p, func(data []byte) {
			fs, err := ops.OpenFS(data)
			if err != nil {
				ui.ShowMessageBox(t.m.Form.Panel(), p.Name, err.Error())
				return
			}
			t.showViewer(newFSView(t, p, fs))
		})
	default:
		return false
	}
	return true
}

// partData reads the content of the partition: from the image, or from the
// board
func (t *PartitionsTab) partData(p ops.Partition, then func(data []byte)) {
	if im := t.m.image; im != nil {
		end := min(int(p.Offset)+int(p.Size), len(im.Data))
		then(append([]byte(nil), im.Data[p.Offset:end]...))
		return
	}
	var data []byte
	t.m.runProgress(p.Name, T().Reading, false, func(ctx context.Context, s *ops.Session, progress func(int, int)) error {
		var err error
		data, err = s.ReadPartition(ctx, p.Name, progress)
		return err
	}, func(err error) {
		if err != nil {
			t.m.showError(p.Name, err)
			return
		}
		then(data)
	})
}

// showViewer shows the editor in place of the table
func (t *PartitionsTab) showViewer(v partViewer) {
	t.viewer = v
	t.viewerBox.RemoveAllWidgets()
	t.viewerBox.AddWidget(0, 0, v)
	t.tableBox.SetVisible(false)
	t.viewerBox.SetVisible(true)
}

// closeViewer goes back to the table; changes not written are asked about
func (t *PartitionsTab) closeViewer() {
	if t.viewer == nil {
		return
	}
	back := func() {
		t.viewer = nil
		t.viewerBox.RemoveAllWidgets()
		t.viewerBox.SetVisible(false)
		t.tableBox.SetVisible(true)
	}
	if !t.viewer.dirty() {
		back()
		return
	}
	ask(t.m.Form, T().TabPartitions, T().DropChangesAsk,
		choice{T().DropChanges, ui.ButtonOther, back},
		choice{T().Cancel, ui.ButtonReject, nil})
}

// viewerHeader is the line over an editor: the way back, the title
func (t *PartitionsTab) viewerHeader(title *ui.Label) *ui.HBox {
	row := ui.NewHBox()
	row.SetCellPadding(6)
	row.Add(textButton(func() string { return "← " + T().TabPartitions }, t.closeViewer))
	title.SetFontBold(true)
	title.SetXExpandable(true)
	row.Add(title)
	return row
}

// canWriteBoard tells whether the editors can write to the board: the
// partition is the board's, not an image's
func (t *PartitionsTab) canWriteBoard() bool {
	return t.m.image == nil && t.m.hasBoard && !t.m.busy
}
