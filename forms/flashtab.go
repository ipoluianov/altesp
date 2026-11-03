package forms

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ipoluianov/altesp/config"
	"github.com/ipoluianov/altesp/internal/ops"
	"github.com/ipoluianov/nui/ui"
)

// listFile is a file of the flash list
type listFile struct {
	ops.File
	Enabled bool
}

// FlashTab is the page of the Flash tab: the files to write, the Flash
// button with its options, erasing and reading the flash
type FlashTab struct {
	ui.Panel
	m *MainForm
	// export is the Read flash dialog shown last (for the tests)
	export *exportDialog
	// folderBtn chooses a build folder; emptyBox is shown in place of the
	// empty list
	folderBtn *ui.Button
	emptyBox  *ui.Panel

	files   []listFile
	project string // the folder the files are remembered for
	elf     string

	list     *ui.ListView
	model    *ui.FuncModel
	noPorts  *ui.Label
	verify   *ui.Checkbox
	reset    *ui.Checkbox
	flashBtn *ui.Button
	moreBtn  *ui.Button
	addBtn   *ui.Button
	eraseBtn *ui.Button
	readBtn  *ui.Button
	stopBtn  *ui.Button
	bar      *ui.ProgressBar
	progress *ui.Label

	// The options of the arrow next to Flash
	onlyChanged, noCompress, noStub bool
	// encrypt has the chip encrypt what it writes (flash encryption on)
	encrypt bool
	// params override the flash mode, frequency and size of the header
	params       ops.FlashParams
	overrideRow  *ui.HBox
	overrideText *ui.Label
}

// The columns of the list: the check box, the name, then these from the
// right edge
const (
	colCheck   = 26
	colAddress = 330
	colSize    = 230
	colKind    = 150
)

func newFlashTab(m *MainForm) *FlashTab {
	f := &FlashTab{m: m}
	f.InitWidget()
	f.SetPanelPadding(6)
	f.SetCellPadding(6)

	f.noPorts = ui.NewLabel("")
	f.noPorts.SetForegroundColor(colorWait.get())
	ui.OnThemeChanged.Connect(func(bool) { f.noPorts.SetForegroundColor(colorWait.get()) })
	f.noPorts.SetVisible(false)
	f.AddWidget(0, 0, f.noPorts)

	f.AddWidget(1, 0, newColumnHeader())
	f.model = ui.NewFuncModel(func() int { return len(f.files) }, 1, func(row, col int) string {
		return filepath.Base(f.files[row].Path)
	})
	f.list = ui.NewListView()
	f.list.SetName("flash-files")
	f.list.SetModel(f.model)
	f.list.SetEmptyText(T().DropHint)
	f.list.SetItemPainter(f.paintRow)
	f.list.SetContextMenu(f.rowMenu())
	f.list.OnMouseDown.Connect(f.listMouseDown)
	f.list.OnActivated.Connect(func(row int) { f.changeAddress(row) })
	// While the list is empty: the hint, and the two ways to fill it
	f.emptyBox = f.emptyPanel()
	listBox := ui.NewPanel()
	listBox.SetPanelPadding(0)
	listBox.AddWidget(0, 0, f.list)
	listBox.AddWidget(1, 0, f.emptyBox)
	f.AddWidget(2, 0, listBox)
	f.showEmpty()

	buttons := ui.NewHBox()
	buttons.SetCellPadding(6)
	f.addBtn = textButton(func() string { return T().Add }, f.addDialog)
	buttons.Add(f.addBtn)
	f.folderBtn = textButton(func() string { return T().BuildFolderBtn }, f.folderDialog)
	f.folderBtn.SetTooltipFunc(func() string { return T().BuildFolderTip })
	buttons.Add(f.folderBtn)
	f.eraseBtn = textButton(func() string { return T().EraseAll }, f.eraseAll)
	buttons.Add(f.eraseBtn)
	f.readBtn = textButton(func() string { return T().ReadFlash }, f.showExport)
	buttons.Add(f.readBtn)
	buttons.AddStretch()
	f.verify = ui.NewCheckbox("")
	f.verify.SetTextFunc(func() string { return T().Verify })
	f.verify.SetChecked(true)
	buttons.Add(f.verify)
	f.reset = ui.NewCheckbox("")
	f.reset.SetTextFunc(func() string { return T().ResetAfter })
	f.reset.SetChecked(true)
	buttons.Add(f.reset)
	f.flashBtn = textButton(func() string { return T().FlashBtn }, f.flash)
	f.flashBtn.SetMinWidth(140)
	f.flashBtn.SetFontBold(true)
	buttons.Add(f.flashBtn)
	f.moreBtn = textButton(func() string { return "▾" }, nil)
	f.moreBtn.SetTooltipFunc(func() string { return T().FlashOptions })
	f.moreBtn.OnClick.Connect(func(ui.Widgeter) {
		x, y := f.moreBtn.RectClientAreaOnWindow()
		m.Form.ShowContextMenu(f.optionsMenu(), x, y+f.moreBtn.Height())
	})
	buttons.Add(f.moreBtn)
	f.AddWidget(3, 0, buttons)

	// The override of the flash parameters in effect, with the way to drop it
	f.overrideRow = ui.NewHBox()
	f.overrideRow.SetCellPadding(6)
	f.overrideRow.AddStretch()
	f.overrideText = ui.NewLabel("")
	f.overrideText.SetName("flash-override")
	f.overrideText.SetForegroundColor(colorWait.get())
	f.overrideRow.Add(f.overrideText)
	f.overrideRow.Add(textButton(func() string { return "✕" }, func() { f.setParams(ops.FlashParams{}) }))
	f.overrideRow.SetVisible(false)
	f.AddWidget(6, 0, f.overrideRow)

	progress := ui.NewHBox()
	progress.SetCellPadding(6)
	f.bar = ui.NewProgressBar(0, 1, 0)
	f.bar.SetXExpandable(true)
	progress.Add(f.bar)
	f.stopBtn = textButton(func() string { return T().Stop }, m.stopJob)
	f.stopBtn.SetVisible(false)
	progress.Add(f.stopBtn)
	f.AddWidget(4, 0, progress)
	f.progress = ui.NewLabel("")
	f.progress.SetXExpandable(true)
	f.AddWidget(5, 0, f.progress)

	m.Form.OnLanguageChanged.Connect(func(string) { f.list.SetEmptyText(T().DropHint) })
	return f
}

// textButton is a button out of the focus chain with a text in the
// language of the interface
func textButton(text func() string, onClick func()) *ui.Button {
	b := ui.NewButton("")
	b.SetTextFunc(text)
	b.SetCanBeFocused(false)
	if onClick != nil {
		b.OnClick.Connect(func(ui.Widgeter) { onClick() })
	}
	return b
}

// columnHeader shows the names of the columns of the list
type columnHeader struct {
	ui.Widget
}

func newColumnHeader() *columnHeader {
	h := &columnHeader{}
	h.InitWidget()
	h.SetMinHeight(ui.ThemeControlHeight() - 4)
	h.SetXExpandable(true)
	h.SetOnPaint(func(cnv *ui.Canvas) {
		cnv.SetFontFamily(ui.FontFamilySans)
		cnv.SetFontSize(ui.ThemeFontSize() - 1)
		cnv.SetColor(colorSecondary.get())
		cnv.SetVAlign(ui.VAlignCenter)
		cnv.SetHAlign(ui.HAlignLeft)
		w, ht := h.Width(), h.Height()
		cnv.DrawText(colCheck+4, 0, w-colAddress-colCheck, ht, T().ColFile)
		cnv.DrawText(w-colAddress, 0, colAddress-colSize, ht, T().ColAddress)
		cnv.DrawText(w-colSize, 0, colSize-colKind, ht, T().ColSize)
		cnv.DrawText(w-colKind, 0, colKind, ht, T().ColKind)
	})
	return h
}

// paintRow draws a file of the list: the check box and the columns
func (f *FlashTab) paintRow(p *ui.ItemPaint) {
	if p.Row >= len(f.files) {
		return
	}
	file := f.files[p.Row]
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
	if !file.Enabled {
		text = colorSecondary.get()
	}
	// The check box
	side := 14
	bx, by := 7, (p.Height-side)/2
	cnv.FillFrame(bx, by, side, side, 3, pal.Base, pal.Border)
	if file.Enabled {
		mark, _ := ui.ParseSVGPath(fmt.Sprintf("M%d %dl3 3 6-7 1.4 1.3L%d %dz", bx+2, by+7, bx+5, by+12))
		cnv.FillPath(mark, colorOK.get())
	}
	cnv.SetFontFamily(ui.FontFamilySans)
	cnv.SetFontSize(ui.ThemeFontSize())
	cnv.SetColor(text)
	cnv.SetVAlign(ui.VAlignCenter)
	cnv.SetHAlign(ui.HAlignLeft)
	w, h := p.Width, p.Height
	cnv.DrawText(colCheck+4, 0, w-colAddress-colCheck-8, h, filepath.Base(file.Path))
	cnv.SetFontFamily(ui.FontFamilyMono)
	cnv.DrawText(w-colAddress, 0, colAddress-colSize, h, fmt.Sprintf("0x%X", file.Addr))
	cnv.SetFontFamily(ui.FontFamilySans)
	cnv.DrawText(w-colSize, 0, colSize-colKind, h, ops.SizeText(file.Size))
	cnv.DrawText(w-colKind, 0, colKind, h, kindText(file.Kind))
}

// kindText is the kind of a file in the language of the interface; an
// unknown kind is shown as it is
func kindText(kind string) string {
	if kind == "" {
		return T().KindUnknown
	}
	return T().Kind(kind)
}

// listMouseDown toggles a file by its check box; a right click selects the
// row first, for the context menu
func (f *FlashTab) listMouseDown(e *ui.MouseEvent) {
	row := f.list.RowAt(e.X, e.Y-f.list.ScrollY())
	if row < 0 {
		return
	}
	switch {
	case e.Button == ui.MouseButtonRight:
		f.list.SetCurrentRow(row)
	case e.Button == ui.MouseButtonLeft && e.X < colCheck:
		f.list.SetCurrentRow(row)
		f.toggle(row)
		e.Handled = true
	}
}

// rowMenu is the context menu of a file
func (f *FlashTab) rowMenu() *ui.ContextMenu {
	menu := ui.NewContextMenu(f.list)
	addr := menu.AddItem("", func() { f.changeAddress(f.list.CurrentRow()) })
	addr.SetTextFunc(func() string { return T().ChangeAddress })
	skip := menu.AddItem("", func() { f.toggle(f.list.CurrentRow()) })
	skip.SetTextFunc(func() string { return T().SkipFile })
	skip.SetCheckable(true)
	remove := menu.AddItem("", func() { f.remove(f.list.CurrentRow()) })
	remove.SetTextFunc(func() string { return T().RemoveFile })
	menu.OnShow.Connect(func(*ui.ContextMenu) {
		row := f.list.CurrentRow()
		ok := row >= 0 && row < len(f.files) && !f.m.busy
		addr.SetEnabled(ok)
		skip.SetEnabled(ok)
		remove.SetEnabled(ok)
		skip.SetChecked(ok && !f.files[row].Enabled)
	})
	return menu
}

// optionsMenu is the menu of the arrow next to Flash: the ways of writing
func (f *FlashTab) optionsMenu() *ui.ContextMenu {
	menu := ui.NewContextMenu(nil)
	check(menu, T().OnlyChanged, func() bool { return f.onlyChanged }, func(on bool) { f.onlyChanged = on })
	check(menu, T().NoCompress, func() bool { return f.noCompress }, func(on bool) { f.noCompress = on })
	check(menu, T().NoStub, func() bool { return f.noStub }, func(on bool) {
		f.noStub = on
		if f.m.sess != nil {
			f.m.status.ShowMessage(T().NoStubNext)
		}
	})
	check(menu, T().NoReset, func() bool { return !f.reset.Checked() }, func(on bool) { f.reset.SetChecked(!on) })
	menu.AddSeparator()
	menu.AddItem(T().PortBaud, f.baudDialog)
	menu.AddItem(T().ParamsOverride, f.paramsDialog)
	enc := check(menu, T().EncryptWrite, func() bool { return f.encrypt }, func(on bool) {
		f.encrypt = on
		if on {
			f.m.status.ShowMessage(T().EncryptNote)
		}
	})
	enc.SetEnabled(f.m.hasBoard && f.m.board.Security.FlashEncryption)
	menu.AddSeparator()
	check(menu, T().MonitorAfter, func() bool { return config.Get().MonitorAfterFlash }, func(on bool) {
		config.Update(func(s *config.Settings) { s.MonitorAfterFlash = on })
	})
	return menu
}

// check adds an item with a check mark that follows a value
func check(menu *ui.ContextMenu, text string, cur func() bool, set func(bool)) *ui.ContextMenuItem {
	var it *ui.ContextMenuItem
	it = menu.AddItem(text, func() { set(it.Checked()) })
	it.SetCheckable(true)
	menu.OnShow.Connect(func(*ui.ContextMenu) { it.SetChecked(cur()) })
	return it
}

// showNoPorts tells, above the list, that there are no ports and what may
// be in the way
func (f *FlashTab) showNoPorts(none bool) {
	if none {
		lines := append([]string{T().NoPortsHint}, ops.LinuxObstacles()...)
		f.noPorts.SetText(strings.Join(lines, "\n"))
	}
	f.noPorts.SetVisible(none)
}

// setBusy shows the buttons for a job running or not
func (f *FlashTab) setBusy(busy bool) {
	for _, b := range []*ui.Button{f.addBtn, f.eraseBtn, f.readBtn, f.flashBtn, f.moreBtn} {
		b.SetEnabled(!busy)
	}
	f.stopBtn.SetVisible(busy)
}

// Files

func (f *FlashTab) changed() {
	f.model.Changed()
	f.showEmpty()
	f.list.Update()
	f.saveProject()
}

func (f *FlashTab) toggle(row int) {
	if row < 0 || row >= len(f.files) || f.m.busy {
		return
	}
	f.files[row].Enabled = !f.files[row].Enabled
	f.changed()
}

func (f *FlashTab) remove(row int) {
	if row < 0 || row >= len(f.files) {
		return
	}
	f.files = append(f.files[:row], f.files[row+1:]...)
	f.changed()
}

// changeAddress asks for the address of the file
func (f *FlashTab) changeAddress(row int) {
	if row < 0 || row >= len(f.files) || f.m.busy {
		return
	}
	file := f.files[row]
	askAddress(f.m.Form, filepath.Base(file.Path), file.Addr, func(addr uint32) {
		if row < len(f.files) && f.files[row].Path == file.Path {
			f.files[row].Addr = addr
			f.changed()
		}
	})
}

// askAddress asks for an address: hex with 0x, or decimal
func askAddress(form *ui.Form, name string, addr uint32, onOK func(uint32)) {
	ui.ShowInputDialog(form.Panel(), ui.InputDialog{
		Title:  T().AddressTitle,
		Prompt: fmt.Sprintf(T().AddressPrompt, name),
		Value:  fmt.Sprintf("0x%X", addr),
		Validate: func(text string) error {
			_, err := ops.ParseNumber(text)
			return err
		},
	}, func(text string) {
		if v, err := ops.ParseNumber(text); err == nil {
			onOK(v)
		}
	})
}

// addDialog adds files chosen in the dialog
func (f *FlashTab) addDialog() {
	f.m.Form.ShowOpenFileDialog(ui.OpenFileDialogOptions{
		Title:         T().Add,
		AllowMultiple: true,
		Filters: []ui.FileDialogFilter{
			{DisplayName: T().Firmware, Patterns: []string{"*.bin"}},
			{DisplayName: T().AllFiles, Patterns: []string{"*"}},
		},
	}, func(paths []string, err error) {
		if err == nil {
			f.addPaths(paths)
		}
	})
}

// folderDialog takes the build folder chosen in the dialog, as one dropped
func (f *FlashTab) folderDialog() {
	dir := ""
	if f.project != "" {
		dir = filepath.Dir(f.project)
	}
	f.m.Form.ShowSelectDirectoryDialog(ui.SelectDirectoryDialogOptions{Title: T().BuildFolderTitle, DefaultDirectory: dir}, func(path string, err error) {
		if err == nil && path != "" {
			f.addPaths([]string{path})
		}
	})
}

// emptyPanel is shown in place of the empty list: the hint, and links to
// add files or choose a build folder
func (f *FlashTab) emptyPanel() *ui.Panel {
	p := ui.NewPanel()
	p.SetYExpandable(true)
	p.SetXExpandable(true)
	// The frame and the background of the list it stands for
	p.SetOnPaint(func(cnv *ui.Canvas) {
		pal := ui.CurrentPalette()
		cnv.FillFrame(0, 0, p.Width(), p.Height(), 3, pal.Base, pal.Border)
	})
	p.AddVSpacer(0, 1)
	p.AddHSpacer(1, 0)
	box := ui.NewVBox()
	box.SetCellPadding(8)
	hint := secondaryLabel(func() string { return T().DropHint })
	hint.SetTextAlign(ui.HAlignCenter)
	box.Add(hint)
	links := ui.NewHBox()
	links.SetCellPadding(16)
	links.AddStretch()
	files := ui.NewLink("", f.addDialog)
	files.SetTextFunc(func() string { return T().AddFilesLink })
	links.Add(files)
	folder := ui.NewLink("", f.folderDialog)
	folder.SetTextFunc(func() string { return T().BuildFolderLink })
	links.Add(folder)
	links.AddStretch()
	box.Add(links)
	p.AddWidget(1, 1, box)
	p.AddHSpacer(1, 2)
	p.AddVSpacer(2, 1)
	return p
}

// showEmpty shows the hint in place of the list while it is empty
func (f *FlashTab) showEmpty() {
	empty := len(f.files) == 0
	f.list.SetVisible(!empty)
	f.emptyBox.SetVisible(empty)
}

// addPaths adds dropped or chosen files; a folder is a build, which
// replaces the list
func (f *FlashTab) addPaths(paths []string) {
	if f.m.busy {
		return
	}
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			f.loadFolder(p)
			return
		}
	}
	f.addFiles(paths)
}

// loadFolder takes the files of a build folder at their addresses; the
// files skipped last time stay skipped
func (f *FlashTab) loadFolder(dir string) {
	p, err := ops.LoadProject(dir)
	if err != nil {
		ui.ShowMessageBox(f.m.Form.Panel(), T().NotABuild, err.Error())
		return
	}
	skipped := map[string]bool{}
	if old, ok := config.ProjectFor(dir); ok {
		for _, of := range old.Files {
			skipped[of.Path] = !of.Enabled
		}
	}
	f.files = nil
	for _, file := range p.Files {
		f.files = append(f.files, listFile{File: file, Enabled: !skipped[file.Path]})
	}
	f.project, f.elf = dir, p.ELF
	f.changed()
	f.m.status.ShowMessage(fmt.Sprintf(T().BuildLoaded, p.Tool, len(p.Files)))
}

// addFiles adds single files: each goes where its kind says, or the user
// gives the address
func (f *FlashTab) addFiles(paths []string) {
	if len(paths) == 0 {
		return
	}
	path := paths[0]
	rest := paths[1:]
	file, ok, err := ops.FileFor(path, f.m.knownBoard())
	if err != nil {
		ui.ShowMessageBox(f.m.Form.Panel(), T().Add, err.Error())
		f.addFiles(rest)
		return
	}
	add := func() {
		f.put(file)
		f.addFiles(rest)
	}
	if ok {
		add()
		return
	}
	askAddress(f.m.Form, filepath.Base(path), file.Addr, func(addr uint32) {
		file.Addr = addr
		add()
	})
}

// put adds the file to the list, in place of the same file
func (f *FlashTab) put(file ops.File) {
	if f.project == "" || len(f.files) == 0 {
		f.project = filepath.Dir(file.Path)
	}
	for i := range f.files {
		if f.files[i].Path == file.Path {
			f.files[i] = listFile{File: file, Enabled: true}
			f.changed()
			return
		}
	}
	f.files = append(f.files, listFile{File: file, Enabled: true})
	f.changed()
}

// loadRemembered shows the files remembered for the project folder
func (f *FlashTab) loadRemembered(folder string) {
	if f.m.busy {
		return
	}
	pr, ok := config.ProjectFor(folder)
	if !ok {
		return
	}
	board := f.m.knownBoard()
	f.files = nil
	for _, rf := range pr.Files {
		file := ops.File{Path: rf.Path, Addr: rf.Address}
		if found, _, err := ops.FileFor(rf.Path, board); err == nil {
			file.Kind, file.Size = found.Kind, found.Size
		} else {
			file.Kind = T().Missing
		}
		f.files = append(f.files, listFile{File: file, Enabled: rf.Enabled})
	}
	f.project, f.elf = folder, pr.ELF
	f.changed()
}

// saveProject remembers the list for its folder
func (f *FlashTab) saveProject() {
	if f.project == "" || len(f.files) == 0 {
		return
	}
	p := config.Project{ELF: f.elf}
	for _, file := range f.files {
		p.Files = append(p.Files, config.FlashFile{Path: file.Path, Address: file.Addr, Enabled: file.Enabled})
	}
	config.SetProject(f.project, p)
	config.Save()
	f.m.showRecent()
}

// Flashing

// items reads the files to write
func (f *FlashTab) items() ([]ops.Item, bool) {
	var files []ops.File
	for _, file := range f.files {
		if file.Enabled {
			files = append(files, file.File)
		}
	}
	if len(files) == 0 {
		f.m.status.ShowMessage(T().NothingToFlash)
		return nil, false
	}
	items, err := ops.ItemsOf(files)
	if err != nil {
		ui.ShowMessageBox(f.m.Form.Panel(), T().FlashBtn, err.Error())
		return nil, false
	}
	return items, true
}

// flash checks the files against the board, asks about the problems found,
// then writes
func (f *FlashTab) flash() {
	items, ok := f.items()
	if !ok {
		return
	}
	var problems []ops.Problem
	f.showProgress(0, T().Connecting)
	params := f.params
	encrypt := f.encrypt
	f.m.run(true, func(ctx context.Context, s *ops.Session) error {
		// The override of the flash parameters is written into the
		// bootloader's header first: the checks see what is written
		if !params.Empty() {
			patched, err := ops.PatchFlashParams(s.Board(), items, params)
			if err != nil {
				return err
			}
			items = patched
		}
		b := s.Board()
		problems = ops.CheckWrite(b, items, ops.FlashOptions{Encrypt: encrypt && b.Security.FlashEncryption})
		return nil
	}, func(err error) {
		if err != nil {
			f.showProgress(0, "")
			f.m.showError(T().FlashBtn, err)
			return
		}
		f.confirm(problems, func() { f.write(items) }, func() { f.showProgress(0, "") })
	})
}

// confirm shows the problems before writing: warnings may be passed, a
// fatal one is passed only by a secondary button
func (f *FlashTab) confirm(problems []ops.Problem, write, cancel func()) {
	if len(problems) == 0 {
		write()
		return
	}
	fatal := false
	var lines []string
	for _, p := range problems {
		if p.Severity == ops.Fatal {
			fatal = true
		}
		lines = append(lines, "• "+p.String())
	}
	list := strings.Join(lines, "\n")
	if fatal {
		ask(f.m.Form, T().FlashBtn, T().FatalProblems+"\n\n"+list,
			choice{T().FlashAnyway, ui.ButtonOther, write},
			choice{T().Cancel, ui.ButtonReject, cancel})
		return
	}
	ask(f.m.Form, T().FlashBtn, T().WarnProblems+"\n\n"+list,
		choice{T().Continue, ui.ButtonAccept, write},
		choice{T().Cancel, ui.ButtonReject, cancel})
}

// write flashes the items, with the progress of each file and the speed;
// then resets the board and switches to the monitor, as set
func (f *FlashTab) write(items []ops.Item) {
	opt := f.flashOptions()
	resetAfter := f.reset.Checked()
	total := 0
	base := make([]int, len(items))
	for i, it := range items {
		base[i] = total
		total += len(it.Data)
	}
	start := time.Now()
	var last time.Time
	closed := false
	// The card after the write: the session may be closed by the reset
	var after ops.Board
	f.showProgress(0, T().Writing)
	started := f.m.runWrite(func(ctx context.Context, s *ops.Session) error {
		err := s.Flash(ctx, items, opt, func(i, done, all int) {
			now := time.Now()
			if now.Sub(last) < 100*time.Millisecond && done < all {
				return
			}
			last = now
			cur := len(items[i].Data)
			if all > 0 {
				cur = int(int64(done) * int64(len(items[i].Data)) / int64(all))
			}
			overall := base[i] + cur
			text := fmt.Sprintf(T().WritingFile, items[i].Name, i+1, len(items), percent(done, all), speed(overall, start))
			ui.Invoke(func() { f.showProgress(fraction(overall, total), text) })
		})
		if err != nil {
			return err
		}
		after = s.Board()
		if resetAfter {
			closed = true
			return s.Close(ctx, true)
		}
		return nil
	}, func(err error) {
		if closed {
			f.m.setSession(nil)
			f.m.resetAt = time.Now()
			f.m.board = after
			f.m.boardChanged()
		}
		var names []string
		for _, it := range items {
			names = append(names, fmt.Sprintf("%s @0x%X", it.Name, it.Addr))
		}
		f.m.logOp(fmt.Sprintf(T().OpFlash, strings.Join(names, ", ")), err)
		switch {
		case err != nil && ctxErr(err):
			f.showProgress(f.bar.Value(), T().Stopped)
			ui.ShowMessageBox(f.m.Form.Panel(), T().FlashBtn, T().PartialWrite)
		case err != nil:
			f.showProgress(f.bar.Value(), firstLine(err.Error()))
			f.m.showError(T().FlashBtn, err)
		default:
			f.showProgress(1, fmt.Sprintf(T().Flashed, len(items), ops.SizeText(total), time.Since(start).Seconds()))
			if closed && config.Get().MonitorAfterFlash {
				f.m.openMonitor()
			}
		}
	})
	if !started {
		f.showProgress(0, "")
	}
}

// eraseAll erases the whole flash after the user saw what will be lost
func (f *FlashTab) eraseAll() {
	lost := T().EraseUnknown
	if f.m.hasBoard {
		s := f.m.board.Summary()
		var parts []string
		for _, p := range s.Partitions {
			parts = append(parts, p.Name)
		}
		var lines []string
		if len(parts) > 0 {
			lines = append(lines, T().ErasePartitions+" "+strings.Join(parts, ", "))
		}
		for _, a := range s.Apps {
			lines = append(lines, T().EraseApp+" "+appText(a))
		}
		if len(lines) > 0 {
			lost = T().EraseBootloader + "\n" + strings.Join(lines, "\n")
		}
	}
	ask(f.m.Form, T().EraseAll, T().EraseAsk+"\n\n"+lost,
		choice{T().ReadFirst, ui.ButtonOther, func() { f.readFlash(f.erase) }},
		choice{T().Erase, ui.ButtonOther, f.erase},
		choice{T().Cancel, ui.ButtonReject, nil})
}

func (f *FlashTab) erase() {
	f.showProgress(0, T().Erasing)
	start := time.Now()
	f.m.runWrite(func(ctx context.Context, s *ops.Session) error {
		return s.EraseAll(ctx)
	}, func(err error) {
		f.m.logOp(T().OpEraseAll, err)
		if err != nil {
			f.showProgress(0, "")
			f.m.showError(T().EraseAll, err)
			return
		}
		f.showProgress(1, fmt.Sprintf(T().Erased, time.Since(start).Seconds()))
	})
}

// readFlash saves the whole flash to a file; then runs after it is saved
func (f *FlashTab) readFlash(then func()) {
	name := "flash"
	if f.m.hasBoard {
		s := f.m.board.Summary()
		name = strings.ToLower(strings.ReplaceAll(s.Chip, " ", "-"))
		if s.MAC != "" {
			name += "_" + strings.ReplaceAll(s.MAC, ":", "")
		}
	}
	f.m.Form.ShowSaveFileDialog(ui.SaveFileDialogOptions{
		Title:           T().ReadFlash,
		DefaultFileName: name + "_" + time.Now().Format("2006-01-02") + ".bin",
		Filters:         []ui.FileDialogFilter{{DisplayName: T().Firmware, Patterns: []string{"*.bin"}}},
	}, func(path string, err error) {
		if err != nil || path == "" {
			return
		}
		f.readTo(path, then)
	})
}

// readTo reads the whole flash into the file
func (f *FlashTab) readTo(path string, then func()) {
	start := time.Now()
	var last time.Time
	size := 0
	f.showProgress(0, T().Reading)
	f.m.run(true, func(ctx context.Context, s *ops.Session) error {
		size = s.Board().FlashSize
		data, err := s.Read(ctx, 0, size, func(done, all int) {
			now := time.Now()
			if now.Sub(last) < 100*time.Millisecond && done < all {
				return
			}
			last = now
			text := fmt.Sprintf(T().ReadingProgress, ops.SizeText(done), ops.SizeText(all), speed(done, start))
			ui.Invoke(func() { f.showProgress(fraction(done, all), text) })
		})
		if err != nil {
			return err
		}
		return os.WriteFile(path, data, 0o644)
	}, func(err error) {
		if err != nil {
			f.showProgress(0, "")
			f.m.showError(T().ReadFlash, err)
			return
		}
		f.showProgress(1, fmt.Sprintf(T().ReadDone, ops.SizeText(size), path))
		if then != nil {
			then()
		}
	})
}

// showProgress shows the bar and the line under it
func (f *FlashTab) showProgress(v float64, text string) {
	f.bar.SetValue(v)
	f.progress.SetText(text)
}

func fraction(done, total int) float64 {
	if total <= 0 {
		return 0
	}
	return float64(done) / float64(total)
}

func percent(done, total int) int {
	return int(fraction(done, total) * 100)
}

// speed is the rate since the start, in KB/s
func speed(done int, start time.Time) string {
	sec := time.Since(start).Seconds()
	if sec < 0.05 {
		return "—"
	}
	return fmt.Sprintf("%.0f KB/s", float64(done)/1024/sec)
}

// enabledFiles are the files ticked in the list
func (f *FlashTab) enabledFiles() []ops.File {
	var files []ops.File
	for _, file := range f.files {
		if file.Enabled {
			files = append(files, file.File)
		}
	}
	return files
}

// flashOptions are the options of the write: encryption only where the
// chip encrypts the flash, and then nothing can be verified
func (f *FlashTab) flashOptions() ops.FlashOptions {
	enc := f.encrypt && f.m.hasBoard && f.m.board.Security.FlashEncryption
	return ops.FlashOptions{Verify: f.verify.Checked() && !enc, OnlyChanged: f.onlyChanged && !enc,
		NoCompress: f.noCompress, Encrypt: enc}
}

// flashBauds are the rates offered for flashing
var flashBauds = []int{115200, 230400, 460800, 921600, 1500000, 2000000}

// baudDialog chooses the rate of flashing, used from the next connection
func (f *FlashTab) baudDialog() {
	c := &ui.DialogContent{}
	c.InitWidget()
	c.AddLabel(0, 0, T().PortBaudPrompt)
	combo := ui.NewComboBox()
	cur := config.Get().FlashBaud
	for i, b := range flashBauds {
		combo.AddItem(fmt.Sprintf(T().BaudFmt, b), b)
		if b == cur {
			combo.SetSelectedIndex(i)
		}
	}
	combo.SetName("flash-baud")
	c.AddWidget(1, 0, combo)
	c.AddWidget(2, 0, secondaryLabel(func() string { return T().PortBaudNote }))
	buttons := ui.NewDialogButtonBox()
	buttons.AddOK(func() {
		b, _ := combo.ItemData(combo.SelectedIndex()).(int)
		c.RunInParent(func() { f.setBaud(b) })
		c.Form().Close()
	})
	buttons.AddCancel(func() { c.Form().Close() })
	c.AddWidget(3, 0, buttons)
	c.OnDialogShow = func() { c.Form().SetTitle(T().PortBaud) }
	f.m.Form.Panel().ShowDialog(c)
}

// setBaud keeps the rate of flashing
func (f *FlashTab) setBaud(b int) {
	if b <= 0 {
		return
	}
	config.Update(func(s *config.Settings) { s.FlashBaud = b })
	if f.m.sess != nil {
		f.m.status.ShowMessage(T().NoStubNext)
	}
}

// paramsDialog overrides the flash mode, frequency and size the
// bootloader's header tells
func (f *FlashTab) paramsDialog() {
	key := ""
	if f.m.hasBoard && f.m.board.Chip != nil {
		key = f.m.board.Chip.Key
	}
	c := &ui.DialogContent{}
	c.InitWidget()
	grid := ui.NewPanel()
	grid.SetPanelPadding(0)
	combo := func(row int, label string, values []string, cur string) *ui.ComboBox {
		grid.AddLabel(row, 0, label)
		cb := ui.NewComboBox()
		cb.AddItem(T().AsInFile, "")
		for i, v := range values {
			cb.AddItem(v, v)
			if v == cur {
				cb.SetSelectedIndex(i + 1)
			}
		}
		if cur == "" {
			cb.SetSelectedIndex(0)
		}
		cb.SetMinWidth(200)
		grid.AddWidget(row, 1, cb)
		return cb
	}
	freqs, sizes := ops.FlashFreqs(key), ops.FlashSizes(key)
	if key == "" {
		freqs, sizes = ops.FlashFreqs("esp32c3"), ops.FlashSizes("esp32c3")
	}
	mode := combo(0, T().FlashMode, ops.FlashModes(), f.params.Mode)
	freq := combo(1, T().FlashFreq, freqs, f.params.Freq)
	size := combo(2, T().FlashSizeLabel, sizes, f.params.Size)
	mode.SetName("flash-mode")
	freq.SetName("flash-freq")
	size.SetName("flash-size")
	c.AddWidget(0, 0, grid)
	c.AddWidget(1, 0, secondaryLabel(func() string { return T().ParamsNote }))
	buttons := ui.NewDialogButtonBox()
	buttons.AddOK(func() {
		val := func(cb *ui.ComboBox) string { s, _ := cb.ItemData(cb.SelectedIndex()).(string); return s }
		p := ops.FlashParams{Mode: val(mode), Freq: val(freq), Size: val(size)}
		c.RunInParent(func() { f.setParams(p) })
		c.Form().Close()
	})
	buttons.AddCancel(func() { c.Form().Close() })
	c.AddWidget(2, 0, buttons)
	c.OnDialogShow = func() { c.Form().SetTitle(T().ParamsOverride) }
	f.m.Form.Panel().ShowDialog(c)
}

// setParams keeps the override and shows it
func (f *FlashTab) setParams(p ops.FlashParams) {
	f.params = p
	if p.Empty() {
		f.overrideRow.SetVisible(false)
		return
	}
	var parts []string
	for _, v := range []string{p.Mode, p.Freq, p.Size} {
		if v != "" {
			parts = append(parts, v)
		}
	}
	f.overrideText.SetText(T().OverrideShown + " " + strings.Join(parts, " "))
	f.overrideRow.SetVisible(true)
}
