package forms

import (
	"bytes"
	"context"
	hexenc "encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ipoluianov/altesp/internal/ops"
	"github.com/ipoluianov/nui/ui"
)

// pageSize is the unit the flash is read in for the view
const pageSize = 0x1000

// pageBatch is the most pages read at once: the jobs wait for less
const pageBatch = 16

// flashSource is the flash of the connected board, read as it is shown: the
// pages wanted by the view are read in the background through the session
// and kept
type flashSource struct {
	m    *MainForm
	size int

	mu      sync.Mutex
	pages   map[int][]byte
	want    map[int]bool
	loading bool
	failed  bool
}

func newFlashSource(m *MainForm, size int) *flashSource {
	return &flashSource{m: m, size: size, pages: map[int][]byte{}, want: map[int]bool{}}
}

func (f *flashSource) Size() int { return f.size }

func (f *flashSource) At(off int) (byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.pages[off/pageSize]
	if p == nil {
		return 0, false
	}
	return p[off%pageSize], true
}

// Need is called on the UI thread while painting
func (f *flashSource) Need(from, to int) {
	f.mu.Lock()
	for p := from / pageSize; p*pageSize < to; p++ {
		if f.pages[p] == nil {
			f.want[p] = true
		}
	}
	f.mu.Unlock()
	f.load()
}

// loaded tells how many bytes are read
func (f *flashSource) loaded() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pages) * pageSize
}

// put keeps bytes read by a job
func (f *flashSource) put(addr int, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for off := 0; off+pageSize <= len(data); off += pageSize {
		if (addr+off)%pageSize == 0 {
			f.pages[(addr+off)/pageSize] = data[off : off+pageSize]
		}
	}
}

// load reads the pages wanted, off the UI thread, while no job runs
func (f *flashSource) load() {
	m := f.m
	if f.loading || f.failed || m.busy || m.sess == nil || m.memory.src != hexSource(f) {
		return
	}
	f.mu.Lock()
	var pages []int
	for p := range f.want {
		pages = append(pages, p)
	}
	f.mu.Unlock()
	if len(pages) == 0 {
		return
	}
	sort.Ints(pages)
	// The pages near the view first
	top := m.memory.view.TopAddress() / pageSize
	sort.SliceStable(pages, func(i, j int) bool { return abs(pages[i]-top) < abs(pages[j]-top) })
	if len(pages) > pageBatch {
		pages = pages[:pageBatch]
	}
	sort.Ints(pages)
	f.loading = true
	s := m.sess
	go func() {
		m.sessMu.Lock()
		got := map[int][]byte{}
		var err error
		for i := 0; i < len(pages); {
			// A run of pages in one read
			j := i + 1
			for j < len(pages) && pages[j] == pages[j-1]+1 {
				j++
			}
			var data []byte
			data, err = s.Read(context.Background(), uint32(pages[i]*pageSize), (j-i)*pageSize, nil)
			if err != nil {
				break
			}
			for k := i; k < j; k++ {
				got[pages[k]] = data[(k-i)*pageSize : (k-i+1)*pageSize]
			}
			i = j
		}
		m.sessMu.Unlock()
		ui.Invoke(func() {
			f.mu.Lock()
			for p, d := range got {
				f.pages[p] = d
				delete(f.want, p)
			}
			f.mu.Unlock()
			f.loading = false
			if err != nil {
				f.failed = true
				m.status.ShowMessage(firstLine(err.Error()))
			}
			m.memory.view.update()
			f.load()
		})
	}()
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// imageSource is a file opened without a board: all of it is there
type imageSource struct{ data []byte }

func (s imageSource) Size() int { return len(s.data) }

func (s imageSource) At(off int) (byte, bool) {
	if off < 0 || off >= len(s.data) {
		return 0, false
	}
	return s.data[off], true
}

func (s imageSource) Need(from, to int) {}

// emptySource shows nothing
type emptySource struct{}

func (emptySource) Size() int           { return 0 }
func (emptySource) At(int) (byte, bool) { return 0, false }
func (emptySource) Need(from, to int)   {}

// MemoryTab is the page of the Memory tab: the flash of the board or an
// image file in hex, with its map
type MemoryTab struct {
	ui.Panel
	m *MainForm

	src  hexSource
	mode string // what is shown: see sync

	imageBar *ui.HBox
	empty    *ui.Panel
	info     *ui.Label // why there is nothing to show
	connect  *ui.Button
	body     *ui.Panel
	toolbar  *ui.HBox
	view     *HexView
	fmap     *flashMap
	caretPos *ui.Label
	writeBtn *ui.Button
	dropBtn  *ui.Button

	diffBox  *ui.Panel
	diffList *ui.ListView
	diffs    []ops.Range

	lastFind string
}

func newMemoryTab(m *MainForm) *MemoryTab {
	t := &MemoryTab{m: m, src: emptySource{}}
	t.InitWidget()
	t.SetPanelPadding(6)
	t.SetCellPadding(6)

	t.imageBar = m.newImageBar()
	t.AddWidget(0, 0, t.imageBar)

	// Nothing to show: why, and what to do
	empty := ui.NewPanel()
	t.empty = empty
	empty.AddVSpacer(0, 1)
	empty.AddHSpacer(1, 0)
	t.info = ui.NewLabel("")
	t.info.SetForegroundColor(colorSecondary.get())
	ui.OnThemeChanged.Connect(func(bool) { t.info.SetForegroundColor(colorSecondary.get()) })
	empty.AddWidget(1, 1, t.info)
	buttons := ui.NewHBox()
	buttons.SetCellPadding(6)
	t.connect = textButton(func() string { return T().Connect }, m.connect)
	buttons.Add(t.connect)
	buttons.Add(textButton(func() string { return T().OpenImage }, m.openImageDialog))
	buttons.AddStretch()
	empty.AddWidget(2, 1, buttons)
	empty.AddHSpacer(1, 2)
	empty.AddVSpacer(3, 1)
	t.AddWidget(1, 0, empty)

	t.body = ui.NewPanel()
	t.body.SetPanelPadding(0)
	t.body.SetCellPadding(6)
	t.toolbar = ui.NewHBox()
	t.toolbar.SetCellPadding(6)
	t.toolbar.Add(textButton(func() string { return T().GoTo }, t.goToDialog))
	t.toolbar.Add(textButton(func() string { return T().Find }, t.findDialog))
	t.toolbar.Add(textButton(func() string { return T().SaveRegion }, t.saveRegionDialog))
	t.toolbar.Add(textButton(func() string { return T().CompareFile }, t.compareDialog))
	t.writeBtn = textButton(func() string { return T().WriteChanges }, t.writeEdits)
	t.writeBtn.SetVisible(false)
	t.toolbar.Add(t.writeBtn)
	t.dropBtn = textButton(func() string { return T().DiscardChanges }, func() {
		t.view.DropEdits()
		t.editsChanged()
	})
	t.dropBtn.SetVisible(false)
	t.toolbar.Add(t.dropBtn)
	t.caretPos = ui.NewLabel("")
	t.caretPos.SetXExpandable(true)
	t.caretPos.SetTextAlign(ui.HAlignRight)
	t.toolbar.Add(t.caretPos)
	t.body.AddWidget(0, 0, t.toolbar)

	row := ui.NewPanel()
	row.SetPanelPadding(0)
	row.SetCellPadding(6)
	t.view = newHexView(t.src)
	t.view.SetName("hex-view")
	t.view.OnCaret = t.showCaret
	t.view.OnEdit = t.editsChanged
	t.view.SetContextMenu(t.viewMenu())
	t.fmap = newFlashMap(t)
	row.AddWidget(0, 0, t.fmap)
	row.AddWidget(0, 1, t.view)

	t.diffBox = ui.NewPanel()
	t.diffBox.SetPanelPadding(0)
	t.diffBox.SetMinWidth(200)
	t.diffBox.SetMaxWidth(200)
	diffTitle := ui.NewLabel("")
	diffTitle.SetTextFunc(func() string { return T().Differences })
	diffTitle.SetFontBold(true)
	t.diffBox.AddWidget(0, 0, diffTitle)
	t.diffList = ui.NewListView()
	t.diffList.SetFontFamily(ui.FontFamilyMono)
	t.diffList.OnSelectionChanged.Connect(func(i int) {
		if i >= 0 && i < len(t.diffs) {
			t.view.GoTo(int(t.diffs[i].Addr))
		}
	})
	t.diffBox.AddWidget(1, 0, t.diffList)
	t.diffBox.AddWidget(2, 0, textButton(func() string { return T().CloseDiff }, func() { t.setDiffs(nil) }))
	t.diffBox.SetVisible(false)
	row.AddWidget(0, 2, t.diffBox)
	t.body.AddWidget(1, 0, row)
	t.AddWidget(2, 0, t.body)

	t.sync()
	return t
}

// sync shows what there is: the image opened, the flash of the board
// connected, or why nothing; a change of it starts from the top
func (t *MemoryTab) sync() {
	m := t.m
	mode := "none"
	switch {
	case m.image != nil:
		mode = "image:" + m.image.Path
	case m.sess != nil && m.board.CanRead && !m.board.SecureDownload:
		mode = fmt.Sprintf("board:%p", m.sess)
	case m.sess != nil:
		mode = "noread"
	}
	m.showImageBar(t.imageBar)
	if mode != t.mode {
		t.mode = mode
		switch {
		case m.image != nil:
			t.setSource(imageSource{m.image.Data})
		case strings.HasPrefix(mode, "board:"):
			t.setSource(newFlashSource(m, m.board.FlashSize))
		default:
			t.setSource(emptySource{})
		}
	}
	shown := t.src.Size() > 0
	t.body.SetVisible(shown)
	t.empty.SetVisible(!shown)
	t.connect.SetVisible(mode == "none")
	switch mode {
	case "noread":
		if m.board.SecureDownload {
			t.info.SetText(T().NoReadSecure)
		} else {
			t.info.SetText(T().NoReadStub)
		}
	default:
		t.info.SetText(T().MemoryHint)
	}
}

// invalidate drops the pages read: the flash was written
func (t *MemoryTab) invalidate() {
	if fs, ok := t.src.(*flashSource); ok && t.mode != "" {
		t.setSource(newFlashSource(t.m, fs.size))
	}
}

// setSource shows the source, keeping the place when it is the same flash
func (t *MemoryTab) setSource(src hexSource) {
	top, caret := t.view.TopAddress(), t.view.Caret()
	_, sameKind := src.(*flashSource)
	_, wasFlash := t.src.(*flashSource)
	t.src = src
	t.view.SetSource(src)
	if sameKind && wasFlash {
		t.view.setTop(top / hexPerRow)
		t.view.caret, t.view.anchor = caret, caret
	}
	t.setDiffs(nil)
	t.editsChanged()
	t.fmap.Update()
	t.showCaret()
}

// canWrite tells whether the bytes shown can be written: the board's
func (t *MemoryTab) canWrite() bool {
	_, ok := t.src.(*flashSource)
	return ok && t.m.sess != nil && !t.m.busy
}

// showCaret tells where the caret is: the address, the partition, the
// selection
func (t *MemoryTab) showCaret() {
	if t.src.Size() == 0 {
		t.caretPos.SetText("")
		return
	}
	start, n := t.view.Selection()
	text := fmt.Sprintf("0x%08X", t.view.Caret())
	if name := t.m.partitionAt(uint32(t.view.Caret())); name != "" {
		text += " · " + name
	}
	if n > 1 {
		text += fmt.Sprintf(" · "+T().SelectedFmt, ops.SizeText(n), start, start+n-1)
	}
	t.caretPos.SetText(text)
	t.fmap.Update()
}

func (t *MemoryTab) editsChanged() {
	on := len(t.view.Edits()) > 0
	t.writeBtn.SetVisible(on)
	t.dropBtn.SetVisible(on)
}

// setDiffs shows the blocks found different, in the view, the map and the
// list
func (t *MemoryTab) setDiffs(diffs []ops.Range) {
	t.diffs = diffs
	var marks []hexMark
	var lines []string
	for _, d := range diffs {
		marks = append(marks, hexMark{int(d.Addr), int(d.Size)})
		lines = append(lines, fmt.Sprintf("0x%08X %s", d.Addr, ops.SizeText(int(d.Size))))
	}
	t.view.SetMarks(marks)
	t.diffList.SetItems(lines)
	t.diffBox.SetVisible(len(diffs) > 0)
	t.fmap.Update()
}

// viewMenu is the context menu of a byte or a selection
func (t *MemoryTab) viewMenu() *ui.ContextMenu {
	menu := ui.NewContextMenu(t.view)
	item := func(text func() string, f func()) *ui.ContextMenuItem {
		it := menu.AddItem("", f)
		it.SetTextFunc(text)
		return it
	}
	item(func() string { return T().GoToAddr }, t.goToDialog).SetShortcut("Ctrl+G")
	item(func() string { return T().FindMenu }, t.findDialog).SetShortcut("Ctrl+F")
	menu.AddSeparator()
	save := item(func() string { return T().SaveSelection }, t.saveSelection)
	copyHex := item(func() string { return T().CopyHex }, func() { t.copySelection(false) })
	copyC := item(func() string { return T().CopyC }, func() { t.copySelection(true) })
	menu.AddSeparator()
	writeHere := item(func() string { return T().WriteFileHere }, t.writeFileHere)
	erase := item(func() string { return T().EraseSectors }, t.eraseSectors)
	edit := item(func() string { return T().EnableEditing }, func() {
		t.view.SetEditable(!t.view.Editable())
		if t.view.Editable() {
			t.m.status.ShowMessage(T().EditingHint)
		}
	})
	edit.SetCheckable(true)
	menu.OnShow.Connect(func(*ui.ContextMenu) {
		has := t.src.Size() > 0
		save.SetEnabled(has && !t.m.busy)
		copyHex.SetEnabled(has)
		copyC.SetEnabled(has)
		w := t.canWrite()
		writeHere.SetEnabled(w)
		erase.SetEnabled(w)
		edit.SetEnabled(w)
		edit.SetChecked(t.view.Editable())
	})
	return menu
}

// goToDialog asks for an address and shows it
func (t *MemoryTab) goToDialog() {
	if t.src.Size() == 0 {
		return
	}
	ui.ShowInputDialog(t.m.Form.Panel(), ui.InputDialog{
		Title:  T().GoToAddr,
		Prompt: T().GoToPrompt,
		Value:  fmt.Sprintf("0x%X", t.view.Caret()),
		Validate: func(text string) error {
			v, err := ops.ParseNumber(text)
			if err == nil && int(v) >= t.src.Size() {
				return fmt.Errorf(T().PastEnd, ops.SizeText(t.src.Size()))
			}
			return err
		},
	}, func(text string) {
		if v, err := ops.ParseNumber(text); err == nil {
			t.view.GoTo(int(v))
			t.view.Focus()
		}
	})
}

// show shows the region in the tab
func (t *MemoryTab) show(addr, size uint32) {
	t.m.tabs.SetCurrentIndex(tabMemory)
	t.view.Select(int(addr), int(size))
}

// selectionBytes returns the bytes selected, when all of them are read
func (t *MemoryTab) selectionBytes() ([]byte, bool) {
	start, n := t.view.Selection()
	data := make([]byte, n)
	for i := range data {
		b, ok := t.src.At(start + i)
		if !ok {
			return nil, false
		}
		data[i] = b
	}
	return data, true
}

// maxCopy is the most bytes copied as text
const maxCopy = 1 << 20

// copySelection puts the bytes selected on the clipboard: hex, or a C array
func (t *MemoryTab) copySelection(cArray bool) {
	start, n := t.view.Selection()
	if n > maxCopy {
		t.m.status.ShowMessage(T().TooLargeToCopy)
		return
	}
	data, ok := t.selectionBytes()
	if !ok {
		t.m.status.ShowMessage(T().NotReadYet)
		return
	}
	var b strings.Builder
	if cArray {
		fmt.Fprintf(&b, "// 0x%08X, %d bytes\nconst unsigned char data[%d] = {\n", start, n, n)
		for i, v := range data {
			if i%12 == 0 {
				b.WriteString("    ")
			}
			fmt.Fprintf(&b, "0x%02X,", v)
			if i%12 == 11 || i == len(data)-1 {
				b.WriteString("\n")
			} else {
				b.WriteString(" ")
			}
		}
		b.WriteString("};\n")
	} else {
		for i, v := range data {
			if i > 0 {
				b.WriteString(" ")
			}
			fmt.Fprintf(&b, "%02X", v)
		}
	}
	ui.ClipboardSetText(b.String())
	t.m.status.ShowMessage(T().Copied)
}

// saveSelection saves the bytes selected to a file
func (t *MemoryTab) saveSelection() {
	start, n := t.view.Selection()
	t.m.saveRegion(uint32(start), n, nil)
}

// saveRegionDialog asks for the region to save: the selection, or all
func (t *MemoryTab) saveRegionDialog() {
	if t.src.Size() == 0 {
		return
	}
	start, n := t.view.Selection()
	if n <= 1 {
		start, n = 0, t.src.Size()
	}
	askRegion(t.m.Form, T().SaveRegion, uint32(start), n, t.src.Size(), func(addr uint32, size int) {
		t.m.saveRegion(addr, size, nil)
	})
}

// askRegion asks for an address and a size within the limit
func askRegion(form *ui.Form, title string, addr uint32, size, limit int, onOK func(uint32, int)) {
	c := &ui.DialogContent{}
	c.InitWidget()
	grid := ui.NewPanel()
	grid.SetPanelPadding(0)
	grid.AddLabel(0, 0, T().AddressLabel)
	addrBox := ui.NewTextBox()
	addrBox.SetText(fmt.Sprintf("0x%X", addr))
	addrBox.SetMinWidth(200)
	grid.AddWidget(0, 1, addrBox)
	grid.AddLabel(1, 0, T().SizeLabel)
	sizeBox := ui.NewTextBox()
	sizeBox.SetText(fmt.Sprintf("0x%X", size))
	grid.AddWidget(1, 1, sizeBox)
	c.AddWidget(0, 0, grid)
	errLabel := ui.NewLabel("")
	errLabel.SetForegroundColor(colorError.get())
	c.AddWidget(1, 0, errLabel)
	buttons := ui.NewDialogButtonBox()
	buttons.AddOK(func() {
		a, err := ops.ParseNumber(addrBox.Text())
		var s uint32
		if err == nil {
			s, err = ops.ParseNumber(sizeBox.Text())
		}
		if err == nil && (s == 0 || int(a)+int(s) > limit) {
			err = fmt.Errorf(T().PastEnd, ops.SizeText(limit))
		}
		if err != nil {
			errLabel.SetText(err.Error())
			return
		}
		c.RunInParent(func() { onOK(a, int(s)) })
		c.Form().Close()
	})
	buttons.AddCancel(func() { c.Form().Close() })
	c.AddWidget(2, 0, buttons)
	c.OnDialogShow = func() { c.Form().SetTitle(title) }
	form.Panel().ShowDialog(c)
}

// saveRegion reads the region of the board or the image into a file; then
// runs after it is saved
func (m *MainForm) saveRegion(addr uint32, size int, then func()) {
	name := m.dumpName() + fmt.Sprintf("_0x%X_%s", addr, strings.ReplaceAll(ops.SizeText(size), " ", ""))
	m.Form.ShowSaveFileDialog(ui.SaveFileDialogOptions{
		Title:           T().SaveRegion,
		DefaultFileName: name + ".bin",
		Filters:         []ui.FileDialogFilter{{DisplayName: T().Firmware, Patterns: []string{"*.bin"}}},
	}, func(path string, err error) {
		if err != nil || path == "" {
			return
		}
		m.saveRegionTo(path, addr, size, then)
	})
}

// saveRegionTo reads the region into the file
func (m *MainForm) saveRegionTo(path string, addr uint32, size int, then func()) {
	if m.image != nil {
		end := min(int(addr)+size, len(m.image.Data))
		if err := os.WriteFile(path, m.image.Data[addr:end], 0o644); err != nil {
			ui.ShowMessageBox(m.Form.Panel(), T().SaveRegion, err.Error())
			return
		}
		m.status.ShowMessage(fmt.Sprintf(T().ReadDone, ops.SizeText(end-int(addr)), path))
		if then != nil {
			then()
		}
		return
	}
	m.runProgress(T().SaveRegion, T().Reading, false, func(ctx context.Context, s *ops.Session, progress func(int, int)) error {
		data, err := s.Read(ctx, addr, size, progress)
		if err != nil {
			return err
		}
		return os.WriteFile(path, data, 0o644)
	}, func(err error) {
		if err != nil {
			m.showError(T().SaveRegion, err)
			return
		}
		m.status.ShowMessage(fmt.Sprintf(T().ReadDone, ops.SizeText(size), path))
		if then != nil {
			then()
		}
	})
}

// dumpName is the start of the names of the files saved from the board
func (m *MainForm) dumpName() string {
	if m.image != nil {
		return strings.TrimSuffix(filepath.Base(m.image.Path), filepath.Ext(m.image.Path))
	}
	if !m.hasBoard {
		return "flash"
	}
	s := m.board.Summary()
	name := strings.ToLower(strings.ReplaceAll(s.Chip, " ", "-"))
	if s.MAC != "" {
		name += "_" + strings.ReplaceAll(s.MAC, ":", "")
	}
	return name
}

// compareDialog compares the flash with a file: a file and its address
func (t *MemoryTab) compareDialog() {
	if t.src.Size() == 0 {
		return
	}
	t.m.Form.ShowOpenFileDialog(ui.OpenFileDialogOptions{
		Title:   T().CompareFile,
		Filters: []ui.FileDialogFilter{{DisplayName: T().Firmware, Patterns: []string{"*.bin"}}, {DisplayName: T().AllFiles, Patterns: []string{"*"}}},
	}, func(paths []string, err error) {
		if err != nil || len(paths) == 0 {
			return
		}
		path := paths[0]
		data, err := os.ReadFile(path)
		if err != nil {
			ui.ShowMessageBox(t.m.Form.Panel(), T().CompareFile, err.Error())
			return
		}
		// A file of the size of the flash is a dump of it, at 0
		addr := uint32(0)
		if len(data) != t.src.Size() {
			if f, ok, err := ops.FileFor(path, t.m.knownBoard()); err == nil && ok {
				addr = f.Addr
			}
		}
		askAddress(t.m.Form, filepath.Base(path), addr, func(addr uint32) { t.compare(addr, data) })
	})
}

// compare finds the 4 KB blocks that differ from the data at addr
func (t *MemoryTab) compare(addr uint32, data []byte) {
	if int(addr)+len(data) > t.src.Size() {
		ui.ShowMessageBox(t.m.Form.Panel(), T().CompareFile, fmt.Sprintf(T().PastEnd, ops.SizeText(t.src.Size())))
		return
	}
	shown := func(diffs []ops.Range) {
		t.setDiffs(diffs)
		if len(diffs) == 0 {
			t.m.status.ShowMessage(T().Identical)
			ui.ShowMessageBox(t.m.Form.Panel(), T().CompareFile, T().Identical)
			return
		}
		total := 0
		for _, d := range diffs {
			total += int(d.Size)
		}
		t.m.status.ShowMessage(fmt.Sprintf(T().DiffFound, len(diffs), ops.SizeText(total)))
		t.view.GoTo(int(diffs[0].Addr))
	}
	if im, ok := t.src.(imageSource); ok {
		shown(compareBytes(im.data, addr, data))
		return
	}
	var diffs []ops.Range
	t.m.runProgress(T().CompareFile, T().Comparing, false, func(ctx context.Context, s *ops.Session, progress func(int, int)) error {
		var err error
		diffs, err = s.Compare(ctx, addr, data, progress)
		return err
	}, func(err error) {
		if err != nil {
			t.m.showError(T().CompareFile, err)
			return
		}
		shown(diffs)
	})
}

// compareBytes compares by 4 KB blocks, as Session.Compare does on the board
func compareBytes(flash []byte, addr uint32, data []byte) []ops.Range {
	var diff []ops.Range
	for off := 0; off < len(data); off += pageSize {
		n := min(pageSize, len(data)-off)
		a := int(addr) + off
		if !bytes.Equal(flash[a:a+n], data[off:off+n]) {
			if k := len(diff); k > 0 && diff[k-1].Addr+diff[k-1].Size == uint32(a) {
				diff[k-1].Size += uint32(n)
			} else {
				diff = append(diff, ops.Range{Addr: uint32(a), Size: uint32(n)})
			}
		}
	}
	return diff
}

// findDialog asks what to find: hex bytes, or a text in quotes
func (t *MemoryTab) findDialog() {
	if t.src.Size() == 0 {
		return
	}
	ui.ShowInputDialog(t.m.Form.Panel(), ui.InputDialog{
		Title:  T().FindMenu,
		Prompt: T().FindPrompt,
		Value:  t.lastFind,
		Validate: func(text string) error {
			_, err := parseFind(text)
			return err
		},
	}, func(text string) {
		t.lastFind = text
		needle, _ := parseFind(text)
		t.find(needle)
	})
}

// parseFind reads what to find: "text" in quotes, or hex bytes with or
// without spaces; anything else is a text
func parseFind(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("%s", T().FindEmpty)
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return []byte(s[1 : len(s)-1]), nil
	}
	h := strings.ReplaceAll(strings.ReplaceAll(s, " ", ""), "0x", "")
	if b, err := hexenc.DecodeString(h); err == nil {
		return b, nil
	}
	return []byte(s), nil
}

// find looks for the bytes after the caret, around to the start, in what
// is read. A flash not read whole is read first, when the user wants.
func (t *MemoryTab) find(needle []byte) {
	if fs, ok := t.src.(*flashSource); ok && fs.loaded() < fs.size {
		ask(t.m.Form, T().FindMenu, fmt.Sprintf(T().FindReadAll, ops.SizeText(fs.loaded()), ops.SizeText(fs.size)),
			choice{T().ReadWhole, ui.ButtonAccept, func() { t.readAll(func() { t.findIn(needle) }) }},
			choice{T().FindInRead, ui.ButtonOther, func() { t.findIn(needle) }},
			choice{T().Cancel, ui.ButtonReject, nil})
		return
	}
	t.findIn(needle)
}

// readAll reads the whole flash into the view
func (t *MemoryTab) readAll(then func()) {
	fs, ok := t.src.(*flashSource)
	if !ok {
		return
	}
	var data []byte
	t.m.runProgress(T().FindMenu, T().Reading, false, func(ctx context.Context, s *ops.Session, progress func(int, int)) error {
		var err error
		data, err = s.Read(ctx, 0, fs.size, progress)
		return err
	}, func(err error) {
		if err != nil {
			t.m.showError(T().FindMenu, err)
			return
		}
		// The job ended with the pages dropped: the new source takes them
		if nfs, ok := t.src.(*flashSource); ok {
			nfs.put(0, data)
		}
		t.view.update()
		if then != nil {
			then()
		}
	})
}

// findIn looks in the bytes read
func (t *MemoryTab) findIn(needle []byte) {
	size := t.src.Size()
	start := t.view.Caret() + 1
	for k := 0; k < size; k++ {
		off := (start + k) % size
		if off+len(needle) > size {
			continue
		}
		match := true
		for i, b := range needle {
			got, ok := t.src.At(off + i)
			if !ok || got != b {
				match = false
				break
			}
		}
		if match {
			t.view.Select(off, len(needle))
			t.m.status.ShowMessage(fmt.Sprintf(T().FoundAt, off))
			return
		}
	}
	t.m.status.ShowMessage(T().NotFound)
}

// writeFileHere writes a file at the caret: the sectors under it are read,
// patched and written, so the bytes around it stay
func (t *MemoryTab) writeFileHere() {
	addr := uint32(t.view.Caret())
	t.m.Form.ShowOpenFileDialog(ui.OpenFileDialogOptions{Title: T().WriteFileHere}, func(paths []string, err error) {
		if err != nil || len(paths) == 0 {
			return
		}
		data, err := os.ReadFile(paths[0])
		if err != nil {
			ui.ShowMessageBox(t.m.Form.Panel(), T().WriteFileHere, err.Error())
			return
		}
		if int(addr)+len(data) > t.src.Size() {
			ui.ShowMessageBox(t.m.Form.Panel(), T().WriteFileHere, fmt.Sprintf(T().PastEnd, ops.SizeText(t.src.Size())))
			return
		}
		text := fmt.Sprintf(T().WriteHereAsk, filepath.Base(paths[0]), ops.SizeText(len(data)), addr, int(addr)+len(data)-1) +
			t.m.affected(addr, len(data))
		t.m.confirmDanger(T().WriteFileHere, text, T().Write, addr, len(data), func() {
			t.writeBytes(addr, data)
		})
	})
}

// writeBytes writes bytes in place through the session
func (t *MemoryTab) writeBytes(addr uint32, data []byte) {
	t.m.runProgress(T().WriteFileHere, T().Writing, true, func(ctx context.Context, s *ops.Session, progress func(int, int)) error {
		return s.WriteBytes(ctx, addr, data)
	}, func(err error) {
		t.m.logOp(fmt.Sprintf(T().OpWriteBytes, len(data), addr), err)
		if err != nil {
			t.m.showError(T().WriteFileHere, err)
			return
		}
		t.m.status.ShowMessage(fmt.Sprintf(T().WrittenAt, ops.SizeText(len(data)), addr))
	})
}

// eraseSectors erases the sectors under the selection
func (t *MemoryTab) eraseSectors() {
	start, n := t.view.Selection()
	a := uint32(start) / pageSize * pageSize
	end := (uint32(start+n) + pageSize - 1) / pageSize * pageSize
	size := int(end - a)
	text := fmt.Sprintf(T().EraseSectorsAsk, size/pageSize, a, end-1) + t.m.affected(a, size)
	t.m.confirmDanger(T().EraseSectors, text, T().Erase, a, size, func() {
		t.m.runProgress(T().EraseSectors, T().Erasing, true, func(ctx context.Context, s *ops.Session, progress func(int, int)) error {
			return s.Erase(ctx, a, size)
		}, func(err error) {
			t.m.logOp(fmt.Sprintf(T().OpEraseSectors, size/pageSize, a), err)
			if err != nil {
				t.m.showError(T().EraseSectors, err)
				return
			}
			t.m.status.ShowMessage(fmt.Sprintf(T().ErasedAt, size/pageSize, a))
		})
	})
}

// writeEdits writes the bytes typed, after the user saw where
func (t *MemoryTab) writeEdits() {
	edits := t.view.Edits()
	if len(edits) == 0 {
		return
	}
	offs := make([]int, 0, len(edits))
	for off := range edits {
		offs = append(offs, off)
	}
	sort.Ints(offs)
	// The runs of bytes in a row
	type run struct {
		addr uint32
		data []byte
	}
	var runs []run
	for _, off := range offs {
		if k := len(runs); k > 0 && int(runs[k-1].addr)+len(runs[k-1].data) == off {
			runs[k-1].data = append(runs[k-1].data, edits[off])
		} else {
			runs = append(runs, run{uint32(off), []byte{edits[off]}})
		}
	}
	text := fmt.Sprintf(T().WriteEditsAsk, len(edits), offs[0], offs[len(offs)-1]) + t.m.affected(uint32(offs[0]), offs[len(offs)-1]-offs[0]+1)
	ask(t.m.Form, T().WriteChanges, text,
		choice{T().Write, ui.ButtonOther, func() {
			t.m.runProgress(T().WriteChanges, T().Writing, true, func(ctx context.Context, s *ops.Session, progress func(int, int)) error {
				for i, r := range runs {
					if err := s.WriteBytes(ctx, r.addr, r.data); err != nil {
						return err
					}
					progress(i+1, len(runs))
				}
				return nil
			}, func(err error) {
				t.m.logOp(fmt.Sprintf(T().OpWriteBytes, len(edits), offs[0]), err)
				if err != nil {
					t.m.showError(T().WriteChanges, err)
					return
				}
				t.view.DropEdits()
				t.view.SetEditable(false)
				t.editsChanged()
				t.m.status.ShowMessage(fmt.Sprintf(T().EditsWritten, len(edits)))
			})
		}},
		choice{T().Cancel, ui.ButtonReject, nil})
}

// affected names the partitions a region touches, for the confirmations
func (m *MainForm) affected(addr uint32, size int) string {
	parts, tableAddr := m.shownPartitions()
	var names []string
	end := uint64(addr) + uint64(size)
	if tableAddr > 0 && uint64(addr) < uint64(tableAddr) {
		names = append(names, T().Bootloader)
	}
	if tableAddr > 0 && uint64(addr) < uint64(tableAddr)+0xC00 && end > uint64(tableAddr) {
		names = append(names, T().PartitionTable)
	}
	for _, p := range parts {
		if uint64(addr) < uint64(p.Offset)+uint64(p.Size) && end > uint64(p.Offset) {
			names = append(names, p.Name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return "\n\n" + T().Affected + " " + strings.Join(names, ", ")
}

// confirmDanger asks before an operation that loses data, offering to save
// the region first
func (m *MainForm) confirmDanger(title, text, action string, addr uint32, size int, do func()) {
	ask(m.Form, title, text,
		choice{T().BackupFirst, ui.ButtonOther, func() { m.saveRegion(addr, size, do) }},
		choice{action, ui.ButtonOther, do},
		choice{T().Cancel, ui.ButtonReject, nil})
}

// runProgress runs a job that needs the board with a progress dialog; the
// dialog's Cancel stops it
// writes: the job changes the flash, which is read again for the view.
func (m *MainForm) runProgress(title, text string, writes bool, work func(ctx context.Context, s *ops.Session, progress func(done, total int)) error, done func(err error)) {
	var dlg *ui.ProgressDialog
	var last time.Time
	start := time.Now()
	started := m.run(true, func(ctx context.Context, s *ops.Session) error {
		return work(ctx, s, func(d, total int) {
			now := time.Now()
			if now.Sub(last) < 100*time.Millisecond && d < total {
				return
			}
			last = now
			line := fmt.Sprintf("%s %d%% · %s", text, percent(d, total), speed(d, start))
			ui.Invoke(func() {
				if dlg != nil {
					dlg.SetProgress(fraction(d, total))
					dlg.SetText(line)
				}
			})
		})
	}, func(err error) {
		if dlg != nil {
			dlg.Close()
			dlg = nil
		}
		if writes {
			m.memory.invalidate()
		}
		done(err)
	})
	if started {
		dlg = ui.ShowProgressDialog(m.Form.Panel(), title, text, m.stopJob)
	}
}

// flashMap is the strip at the left of the hex view: the whole flash from
// the top down, colored by partition; the view and the differences on it.
// A click shows the place.
type flashMap struct {
	ui.Widget
	t      *MemoryTab
	mouseY int
}

// mapWidth is the width of the map
const mapWidth = 34

func newFlashMap(t *MemoryTab) *flashMap {
	f := &flashMap{t: t}
	f.InitWidget()
	f.SetMinWidth(mapWidth)
	f.SetMaxWidth(mapWidth)
	f.SetYExpandable(true)
	f.SetMouseCursor(ui.MouseCursorPointer)
	f.SetOnPaint(f.paint)
	f.SetOnMouseDown(func(b ui.MouseButton, x, y int, mods ui.KeyModifiers) bool {
		if b == ui.MouseButtonLeft {
			f.jump(y)
		}
		return true
	})
	f.SetOnMouseMove(func(x, y int, mods ui.KeyModifiers) bool {
		f.mouseY = y
		f.SetTooltip(f.tooltipAt(y))
		return false
	})
	return f
}

// addrAt is the address at the height
func (f *flashMap) addrAt(y int) uint32 {
	size := f.t.src.Size()
	h := max(1, f.Height())
	return uint32(int64(max(0, min(y, h-1))) * int64(size) / int64(h))
}

func (f *flashMap) jump(y int) {
	if f.t.src.Size() == 0 {
		return
	}
	addr := f.addrAt(y)
	f.t.view.GoTo(int(addr))
	f.t.view.Focus()
}

func (f *flashMap) tooltipAt(y int) string {
	if f.t.src.Size() == 0 {
		return ""
	}
	addr := f.addrAt(y)
	text := fmt.Sprintf("0x%08X", addr)
	if name := f.t.m.partitionAt(addr); name != "" {
		text += " · " + name
	}
	return text
}

// region is a colored part of the map
type region struct {
	addr, size uint32
	color      themeColor
}

func (f *flashMap) paint(cnv *ui.Canvas) {
	pal := ui.CurrentPalette()
	w, h := f.Width(), f.Height()
	cnv.FillFrame(0, 0, w, h, 3, pal.Base, pal.Border)
	size := f.t.src.Size()
	if size == 0 {
		return
	}
	y := func(addr uint32) int { return int(int64(addr) * int64(h) / int64(size)) }
	for _, r := range f.t.m.mapRegions() {
		y0, y1 := y(r.addr), y(r.addr+r.size)
		cnv.FillRect(3, y0, w-6, max(1, y1-y0-1), r.color.get())
	}
	for _, d := range f.t.diffs {
		y0 := y(d.Addr)
		cnv.FillRect(0, y0, w, max(2, y(d.Addr+d.Size)-y0), colorError.get())
	}
	// The part shown in the view
	top := f.t.view.TopAddress()
	shown := f.t.view.rowsShown() * hexPerRow
	y0, y1 := y(uint32(top)), y(uint32(min(size, top+shown)))
	cnv.DrawRect(1, y0, w-2, max(3, y1-y0))
	cnv.FillRect(0, y0, w, 2, pal.Highlight)
	cnv.FillRect(0, max(y0+2, y1-2), w, 2, pal.Highlight)
}

// The colors of the partitions on the map
var (
	colorMapBoot  = themeColor{hex("#6e7781"), hex("#8c959f")}
	colorMapTable = themeColor{hex("#d29922"), hex("#bf8700")}
	colorMapApp   = themeColor{hex("#2f81f7"), hex("#0969da")}
	colorMapNVS   = themeColor{hex("#3fb950"), hex("#1a7f37")}
	colorMapOTA   = themeColor{hex("#a371f7"), hex("#8250df")}
	colorMapFS    = themeColor{hex("#db6d28"), hex("#bc4c00")}
	colorMapData  = themeColor{hex("#39c5cf"), hex("#1b7c83")}
)

// mapRegions are the parts of the flash shown on the map: the bootloader,
// the partition table and the partitions
func (m *MainForm) mapRegions() []region {
	parts, tableAddr := m.shownPartitions()
	var rs []region
	if tableAddr > 0 {
		rs = append(rs, region{0, tableAddr, colorMapBoot}, region{tableAddr, 0x1000, colorMapTable})
	}
	for _, p := range parts {
		rs = append(rs, region{p.Offset, p.Size, partitionColor(p)})
	}
	return rs
}

// partitionColor is the color of a partition by what it holds
func partitionColor(p ops.Partition) themeColor {
	switch {
	case p.IsApp():
		return colorMapApp
	}
	switch p.SubTypeName() {
	case "nvs", "nvs_keys":
		return colorMapNVS
	case "ota":
		return colorMapOTA
	case "spiffs", "fat", "littlefs":
		return colorMapFS
	}
	return colorMapData
}

// partitionAt names the partition at the address, or the bootloader or
// the table
func (m *MainForm) partitionAt(addr uint32) string {
	parts, tableAddr := m.shownPartitions()
	if tableAddr > 0 {
		if addr < tableAddr {
			return T().Bootloader
		}
		if addr < tableAddr+0x1000 {
			return T().PartitionTable
		}
	}
	for _, p := range parts {
		if addr >= p.Offset && addr < p.Offset+p.Size {
			return p.Name
		}
	}
	return ""
}
