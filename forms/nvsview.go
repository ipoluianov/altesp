package forms

import (
	"bytes"
	"context"
	hexenc "encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ipoluianov/altesp/internal/ops"
	"github.com/ipoluianov/nui/ui"
)

// nvsView is the NVS editor: the namespaces at the left, their keys at the
// right; the changes are kept until written to the board
type nvsView struct {
	ui.Panel
	t   *PartitionsTab
	p   ops.Partition
	raw []byte // the partition as read
	im  *ops.NVS

	changed bool
	ns      string // the namespace shown, "" for all
	spaces  []string
	rows    []ops.NVSEntry

	title    *ui.Label
	problems *ui.Label
	encBar   *ui.HBox
	nsList   *ui.ListView
	list     *ui.ListView
	model    *ui.FuncModel
	writeBtn *ui.Button
	addBtn   *ui.Button
	importB  *ui.Button
}

func newNVSView(t *PartitionsTab, p ops.Partition, raw []byte) *nvsView {
	v := &nvsView{t: t, p: p, raw: raw}
	v.InitWidget()
	v.SetPanelPadding(0)
	v.SetCellPadding(6)

	v.title = ui.NewLabel("")
	v.AddWidget(0, 0, t.viewerHeader(v.title))

	bar := ui.NewHBox()
	bar.SetCellPadding(6)
	v.addBtn = textButton(func() string { return T().AddKey }, func() { v.editDialog(nil) })
	bar.Add(v.addBtn)
	v.importB = textButton(func() string { return T().ImportCSV }, v.importCSV)
	bar.Add(v.importB)
	bar.Add(textButton(func() string { return T().ExportCSV }, func() { v.exportCSV(nil) }))
	bar.Add(textButton(func() string { return T().SaveNVSImage }, v.saveImage))
	bar.AddStretch()
	v.writeBtn = textButton(func() string { return T().WriteToBoard }, v.write)
	v.writeBtn.SetFontBold(true)
	bar.Add(v.writeBtn)
	v.AddWidget(1, 0, bar)

	v.problems = ui.NewLabel("")
	v.problems.SetForegroundColor(colorWait.get())
	v.problems.SetXExpandable(true)
	v.AddWidget(2, 0, v.problems)

	// An encrypted NVS is decrypted with the keys of nvs_keys or of a file
	v.encBar = ui.NewHBox()
	v.encBar.SetCellPadding(6)
	encLabel := ui.NewLabel("")
	encLabel.SetTextFunc(func() string { return T().NVSEncrypted })
	encLabel.SetForegroundColor(colorWait.get())
	v.encBar.Add(encLabel)
	if kp, ok := v.keysPartition(); ok {
		v.encBar.Add(textButton(func() string { return fmt.Sprintf(T().DecryptWith, kp.Name) }, func() {
			t.partData(kp, v.decrypt)
		}))
	}
	v.encBar.Add(textButton(func() string { return T().KeyFile }, v.keyFile))
	v.encBar.AddStretch()
	v.AddWidget(3, 0, v.encBar)

	body := ui.NewPanel()
	body.SetPanelPadding(0)
	body.SetCellPadding(6)
	v.nsList = ui.NewListView()
	v.nsList.SetName("nvs-namespaces")
	v.nsList.SetMinWidth(170)
	v.nsList.SetMaxWidth(170)
	v.nsList.OnSelectionChanged.Connect(func(i int) {
		if i <= 0 || i > len(v.spaces) {
			v.ns = ""
		} else {
			v.ns = v.spaces[i-1]
		}
		v.showKeys()
	})
	v.nsList.SetContextMenu(v.nsMenu())
	body.AddWidget(0, 0, v.nsList)

	right := ui.NewPanel()
	right.SetPanelPadding(0)
	right.AddWidget(0, 0, newColumns(nvsCols, func() []string {
		return []string{T().ColNamespace, T().ColKey, T().ColType, T().ColValue, T().ColSize}
	}))
	v.model = ui.NewFuncModel(func() int { return len(v.rows) }, 1, func(row, col int) string { return v.rows[row].Key })
	v.list = ui.NewListView()
	v.list.SetName("nvs-keys")
	v.list.SetModel(v.model)
	v.list.SetItemPainter(v.paintRow)
	v.list.SetContextMenu(v.keyMenu())
	v.list.OnMouseDown.Connect(func(e *ui.MouseEvent) {
		if e.Button == ui.MouseButtonRight {
			if row := v.list.RowAt(e.X, e.Y-v.list.ScrollY()); row >= 0 {
				v.list.SetCurrentRow(row)
			}
		}
	})
	v.list.OnActivated.Connect(func(row int) {
		if row >= 0 && row < len(v.rows) {
			e := v.rows[row]
			v.editDialog(&e)
		}
	})
	right.AddWidget(1, 0, v.list)
	body.AddWidget(0, 1, right)
	v.AddWidget(4, 0, body)

	im, err := ops.ParseNVS(raw)
	if err != nil {
		v.problems.SetText(err.Error())
		im = nil
	}
	v.setImage(im)
	return v
}

// The columns of the keys, from the left
var nvsCols = []int{0, 130, 280, 350, -80}

// keysPartition is the nvs_keys partition, when there is one
func (v *nvsView) keysPartition() (ops.Partition, bool) {
	parts, _ := v.t.m.shownPartitions()
	for _, p := range parts {
		if p.Type == 0x01 && p.SubTypeName() == "nvs_keys" {
			return p, true
		}
	}
	return ops.Partition{}, false
}

// setImage shows the NVS parsed
func (v *nvsView) setImage(im *ops.NVS) {
	v.im = im
	v.changed = false
	v.refresh()
}

// locked tells whether the NVS can't be changed: encrypted without keys
func (v *nvsView) locked() bool {
	return v.im == nil || v.im.Encrypted && len(v.im.Entries) == 0 && len(v.im.Namespaces()) == 0
}

func (v *nvsView) dirty() bool { return v.changed }

func (v *nvsView) dropped([]string) bool { return false }

// refresh shows the namespaces, the keys and the state
func (v *nvsView) refresh() {
	title := v.p.Name
	if v.im != nil {
		ver := T().NVSv2
		if v.im.Version == 1 {
			ver = T().NVSv1
		}
		title += fmt.Sprintf(" · %s · "+T().NVSUsage, ver, v.im.UsedEntries, v.im.FreeEntries)
	}
	if v.changed {
		title += " · " + T().Changed
	}
	v.title.SetText(title)
	if v.im != nil && len(v.im.Problems) > 0 {
		v.problems.SetText("⚠ " + strings.Join(v.im.Problems, "\n⚠ "))
	} else if v.im != nil {
		v.problems.SetText("")
	}
	v.problems.SetVisible(v.problems.Text() != "")
	v.encBar.SetVisible(v.im != nil && v.im.Encrypted && v.locked())
	if v.im != nil {
		v.spaces = v.im.Namespaces()
	} else {
		v.spaces = nil
	}
	items := []string{T().AllNamespaces}
	cur := 0
	for i, ns := range v.spaces {
		items = append(items, ns)
		if ns == v.ns {
			cur = i + 1
		}
	}
	if cur == 0 {
		v.ns = ""
	}
	v.nsList.SetItems(items)
	v.nsList.SetCurrentRow(cur)
	v.showKeys()
	ok := !v.locked()
	v.addBtn.SetEnabled(ok)
	v.importB.SetEnabled(ok)
	v.writeBtn.SetEnabled(ok && v.changed && v.t.canWriteBoard())
}

// showKeys shows the keys of the namespace
func (v *nvsView) showKeys() {
	v.rows = nil
	if v.im != nil {
		for _, e := range v.im.Entries {
			if v.ns == "" || e.Namespace == v.ns {
				v.rows = append(v.rows, e)
			}
		}
	}
	v.model.Changed()
	v.list.Update()
}

func (v *nvsView) paintRow(p *ui.ItemPaint) {
	if p.Row >= len(v.rows) {
		return
	}
	e := v.rows[p.Row]
	paintColumns(p, nvsCols, []string{e.Namespace, e.Key, e.Type.String(), nvsValueText(e, 80), ops.SizeText(ops.NVSValueSize(e))}, 3)
}

// nvsValueText is the value as the table and the dialog show it: numbers,
// the text of a string, the bytes of a blob in hex (cut to max bytes, 0
// for all)
func nvsValueText(e ops.NVSEntry, max int) string {
	switch v := e.Value.(type) {
	case []byte:
		b := v
		cut := max > 0 && len(b) > max
		if cut {
			b = b[:max]
		}
		var s strings.Builder
		for i, x := range b {
			if i > 0 {
				s.WriteByte(' ')
			}
			fmt.Fprintf(&s, "%02X", x)
		}
		if cut {
			s.WriteString(" …")
		}
		return s.String()
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64)
	}
	return fmt.Sprint(e.Value)
}

// parseNVSValue reads a value typed for the type; the range is checked when
// the value is set
func parseNVSValue(t ops.NVSType, text string) (any, error) {
	name := t.String()
	switch {
	case t == ops.NVSString:
		return text, nil
	case t == ops.NVSBlob:
		h := strings.NewReplacer(" ", "", "\n", "", "0x", "", ",", "").Replace(text)
		b, err := hexenc.DecodeString(h)
		if err != nil {
			return nil, fmt.Errorf("%s", T().BadBlob)
		}
		return b, nil
	case t == ops.NVSFloat || t == ops.NVSDouble:
		return strconv.ParseFloat(strings.TrimSpace(text), 64)
	case strings.HasPrefix(name, "u"):
		v, err := strconv.ParseUint(strings.TrimSpace(text), 0, 64)
		if err != nil {
			return nil, fmt.Errorf(T().BadNumber, name)
		}
		return v, nil
	default:
		v, err := strconv.ParseInt(strings.TrimSpace(text), 0, 64)
		if err != nil {
			return nil, fmt.Errorf(T().BadNumber, name)
		}
		return v, nil
	}
}

// editDialog changes a value, or adds a key when e is nil
func (v *nvsView) editDialog(e *ops.NVSEntry) {
	if v.locked() {
		return
	}
	c := &ui.DialogContent{}
	c.InitWidget()
	grid := ui.NewPanel()
	grid.SetPanelPadding(0)
	grid.AddLabel(0, 0, T().ColNamespace)
	nsBox := ui.NewEditableComboBox()
	nsBox.SetItems(v.spaces)
	nsBox.SetMinWidth(320)
	grid.AddWidget(0, 1, nsBox)
	grid.AddLabel(1, 0, T().ColKey)
	keyBox := ui.NewTextBox()
	grid.AddWidget(1, 1, keyBox)
	grid.AddLabel(2, 0, T().ColType)
	typeBox := ui.NewComboBox()
	for i, t := range ops.NVSTypes {
		typeBox.AddItem(t.String(), i)
	}
	typeBox.SetSelectedIndex(0)
	grid.AddWidget(2, 1, typeBox)
	grid.AddLabel(3, 0, T().ColValue)
	valueBox := ui.NewTextBox()
	grid.AddWidget(3, 1, valueBox)
	c.AddWidget(0, 0, grid)
	hint := secondaryLabel(func() string { return T().NVSValueHint })
	c.AddWidget(1, 0, hint)
	errLabel := ui.NewLabel("")
	errLabel.SetForegroundColor(colorError.get())
	c.AddWidget(2, 0, errLabel)

	title := T().AddKey
	if e != nil {
		title = T().ChangeValue
		nsBox.SetText(e.Namespace)
		nsBox.SetEnabled(false)
		keyBox.SetText(e.Key)
		keyBox.SetReadOnly(true)
		for i, t := range ops.NVSTypes {
			if t == e.Type {
				typeBox.SetSelectedIndex(i)
			}
		}
		typeBox.SetEnabled(false)
		valueBox.SetText(nvsValueText(*e, 0))
	} else if v.ns != "" {
		nsBox.SetText(v.ns)
	}
	buttons := ui.NewDialogButtonBox()
	buttons.AddOK(func() {
		i, _ := typeBox.ItemData(typeBox.SelectedIndex()).(int)
		t := ops.NVSTypes[i]
		value, err := parseNVSValue(t, valueBox.Text())
		if err == nil {
			err = v.im.Set(ops.NVSEntry{Namespace: strings.TrimSpace(nsBox.Text()), Key: strings.TrimSpace(keyBox.Text()), Type: t, Value: value})
		}
		if err != nil {
			errLabel.SetText(err.Error())
			return
		}
		c.RunInParent(func() {
			v.ns = strings.TrimSpace(nsBox.Text())
			v.edited()
		})
		c.Form().Close()
	})
	buttons.AddCancel(func() { c.Form().Close() })
	c.AddWidget(3, 0, buttons)
	c.OnDialogShow = func() {
		c.Form().SetTitle(title)
		if e != nil {
			valueBox.Focus()
		}
	}
	v.t.m.Form.Panel().ShowDialog(c)
}

// edited shows a change made
func (v *nvsView) edited() {
	v.changed = true
	v.refresh()
}

// keyMenu is the context menu of a key
func (v *nvsView) keyMenu() *ui.ContextMenu {
	menu := ui.NewContextMenu(v.list)
	item := func(text func() string, f func()) *ui.ContextMenuItem {
		it := menu.AddItem("", f)
		it.SetTextFunc(text)
		return it
	}
	change := item(func() string { return T().ChangeValue }, func() {
		if e, ok := v.current(); ok {
			v.editDialog(&e)
		}
	})
	add := item(func() string { return T().AddKey }, func() { v.editDialog(nil) })
	addNS := item(func() string { return T().AddNamespace }, v.addNamespace)
	del := item(func() string { return T().Delete }, v.deleteKey)
	menu.AddSeparator()
	item(func() string { return T().ExportNamespace }, func() {
		if e, ok := v.current(); ok {
			v.exportCSV([]string{e.Namespace})
		}
	})
	item(func() string { return T().SaveNVSImage }, v.saveImage)
	menu.OnShow.Connect(func(*ui.ContextMenu) {
		_, ok := v.current()
		w := !v.locked()
		change.SetEnabled(ok && w)
		add.SetEnabled(w)
		addNS.SetEnabled(w)
		del.SetEnabled(ok && w)
	})
	return menu
}

// nsMenu is the context menu of a namespace
func (v *nvsView) nsMenu() *ui.ContextMenu {
	menu := ui.NewContextMenu(v.nsList)
	add := menu.AddItem("", v.addNamespace)
	add.SetTextFunc(func() string { return T().AddNamespace })
	del := menu.AddItem("", v.deleteNamespace)
	del.SetTextFunc(func() string { return T().DeleteNamespace })
	export := menu.AddItem("", func() {
		if v.ns != "" {
			v.exportCSV([]string{v.ns})
		} else {
			v.exportCSV(nil)
		}
	})
	export.SetTextFunc(func() string { return T().ExportNamespace })
	menu.OnShow.Connect(func(*ui.ContextMenu) {
		add.SetEnabled(!v.locked())
		del.SetEnabled(!v.locked() && v.ns != "")
	})
	return menu
}

func (v *nvsView) current() (ops.NVSEntry, bool) {
	i := v.list.CurrentRow()
	if i < 0 || i >= len(v.rows) {
		return ops.NVSEntry{}, false
	}
	return v.rows[i], true
}

func (v *nvsView) addNamespace() {
	ui.ShowInputDialog(v.t.m.Form.Panel(), ui.InputDialog{
		Title:  T().AddNamespace,
		Prompt: T().NamespacePrompt,
		Validate: func(text string) error {
			// Checked on a copy: a bad name leaves the NVS as it is
			cp := *v.im
			return cp.AddNamespace(strings.TrimSpace(text))
		},
	}, func(text string) {
		name := strings.TrimSpace(text)
		if err := v.im.AddNamespace(name); err == nil {
			v.ns = name
			v.edited()
		}
	})
}

func (v *nvsView) deleteKey() {
	e, ok := v.current()
	if !ok {
		return
	}
	ask(v.t.m.Form, T().Delete, fmt.Sprintf(T().DeleteKeyAsk, e.Namespace, e.Key),
		choice{T().Delete, ui.ButtonOther, func() {
			v.im.Delete(e.Namespace, e.Key)
			v.edited()
		}},
		choice{T().Cancel, ui.ButtonReject, nil})
}

func (v *nvsView) deleteNamespace() {
	ns := v.ns
	if ns == "" {
		return
	}
	n := 0
	for _, e := range v.im.Entries {
		if e.Namespace == ns {
			n++
		}
	}
	ask(v.t.m.Form, T().DeleteNamespace, fmt.Sprintf(T().DeleteNSAsk, ns, n),
		choice{T().Delete, ui.ButtonOther, func() {
			v.im.DeleteNamespace(ns)
			v.ns = ""
			v.edited()
		}},
		choice{T().Cancel, ui.ButtonReject, nil})
}

// exportCSV saves the namespaces (all when none) in the CSV of
// nvs_partition_gen
func (v *nvsView) exportCSV(namespaces []string) {
	if v.im == nil {
		return
	}
	var b bytes.Buffer
	if err := v.im.WriteCSV(&b, namespaces...); err != nil {
		ui.ShowMessageBox(v.t.m.Form.Panel(), T().ExportCSV, err.Error())
		return
	}
	name := v.p.Name
	if len(namespaces) == 1 {
		name += "_" + namespaces[0]
	}
	v.t.m.saveText(T().ExportCSV, name+".csv", b.String())
}

// importCSV merges the values of a CSV into the NVS
func (v *nvsView) importCSV() {
	v.t.m.Form.ShowOpenFileDialog(ui.OpenFileDialogOptions{
		Title:   T().ImportCSV,
		Filters: []ui.FileDialogFilter{{DisplayName: "CSV", Patterns: []string{"*.csv"}}, {DisplayName: T().AllFiles, Patterns: []string{"*"}}},
	}, func(paths []string, err error) {
		if err == nil && len(paths) > 0 {
			v.importFrom(paths[0])
		}
	})
}

func (v *nvsView) importFrom(path string) {
	f, err := os.Open(path)
	if err != nil {
		ui.ShowMessageBox(v.t.m.Form.Panel(), T().ImportCSV, err.Error())
		return
	}
	entries, err := ops.ReadNVSCSV(f, filepath.Dir(path))
	f.Close()
	if err != nil {
		ui.ShowMessageBox(v.t.m.Form.Panel(), T().ImportCSV, err.Error())
		return
	}
	var bad []string
	for _, e := range entries {
		if err := v.im.Set(e); err != nil {
			bad = append(bad, err.Error())
		}
	}
	v.edited()
	if len(bad) > 0 {
		ui.ShowMessageBox(v.t.m.Form.Panel(), T().ImportCSV, strings.Join(bad, "\n"))
		return
	}
	v.t.m.status.ShowMessage(fmt.Sprintf(T().NVSImported, len(entries)))
}

// saveImage saves the NVS, with the changes, as a partition image
func (v *nvsView) saveImage() {
	if v.im == nil {
		return
	}
	data := v.raw
	if v.changed {
		var err error
		if data, err = v.im.Bytes(); err != nil {
			ui.ShowMessageBox(v.t.m.Form.Panel(), T().SaveNVSImage, err.Error())
			return
		}
	}
	v.t.m.saveBytes(T().SaveNVSImage, v.p.Name+".bin", data)
}

// write writes the changes to the partition of the board
func (v *nvsView) write() {
	if !v.changed || !v.t.canWriteBoard() {
		return
	}
	if _, err := v.im.Bytes(); err != nil {
		ui.ShowMessageBox(v.t.m.Form.Panel(), T().WriteToBoard, err.Error())
		return
	}
	m := v.t.m
	text := fmt.Sprintf(T().WriteNVSAsk, v.p.Name, v.p.Offset, len(v.im.Entries))
	m.confirmDanger(T().WriteToBoard, text, T().Write, v.p.Offset, int(v.p.Size), func() {
		m.runProgress(T().WriteToBoard, T().Writing, true, func(ctx context.Context, s *ops.Session, progress func(int, int)) error {
			return s.WriteNVS(ctx, v.p.Name, v.im)
		}, func(err error) {
			m.logOp(fmt.Sprintf(T().OpWriteNVS, v.p.Name, len(v.im.Entries)), err)
			if err != nil {
				m.showError(T().WriteToBoard, err)
				return
			}
			v.raw, _ = v.im.Bytes()
			v.changed = false
			v.refresh()
			m.status.ShowMessage(fmt.Sprintf(T().PartWritten, v.p.Name))
		})
	})
}

// decrypt parses the NVS with the keys of a partition or a file
func (v *nvsView) decrypt(keys []byte) {
	im, err := ops.ParseNVSWithKeys(v.raw, keys)
	if err != nil {
		ui.ShowMessageBox(v.t.m.Form.Panel(), v.p.Name, err.Error())
		return
	}
	v.setImage(im)
}

// keyFile decrypts with the keys of a file (an nvs_keys partition image)
func (v *nvsView) keyFile() {
	v.t.m.Form.ShowOpenFileDialog(ui.OpenFileDialogOptions{Title: T().KeyFile}, func(paths []string, err error) {
		if err != nil || len(paths) == 0 {
			return
		}
		data, err := os.ReadFile(paths[0])
		if err != nil {
			ui.ShowMessageBox(v.t.m.Form.Panel(), T().KeyFile, err.Error())
			return
		}
		v.decrypt(data)
	})
}

// saveBytes saves data to a file the user chooses
func (m *MainForm) saveBytes(title, name string, data []byte) {
	m.Form.ShowSaveFileDialog(ui.SaveFileDialogOptions{
		Title:           title,
		DefaultFileName: name,
		Filters:         []ui.FileDialogFilter{{DisplayName: T().Firmware, Patterns: []string{"*.bin"}}, {DisplayName: T().AllFiles, Patterns: []string{"*"}}},
	}, func(path string, err error) {
		if err != nil || path == "" {
			return
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			ui.ShowMessageBox(m.Form.Panel(), title, err.Error())
			return
		}
		m.status.ShowMessage(fmt.Sprintf(T().Saved, path))
	})
}

// columns is the header of a table painted by paintColumns: the x of each
// column, a negative one from the right edge
type columns struct {
	ui.Widget
	xs    []int
	names func() []string
}

func newColumns(xs []int, names func() []string) *columns {
	h := &columns{xs: xs, names: names}
	h.InitWidget()
	h.SetMinHeight(ui.ThemeControlHeight() - 4)
	h.SetXExpandable(true)
	h.SetOnPaint(func(cnv *ui.Canvas) {
		cnv.SetFontFamily(ui.FontFamilySans)
		cnv.SetFontSize(ui.ThemeFontSize() - 1)
		cnv.SetColor(colorSecondary.get())
		cnv.SetVAlign(ui.VAlignCenter)
		for i, n := range h.names() {
			x, end := columnSpan(h.xs, i, h.Width())
			cnv.DrawText(x+8, 0, end-x-10, h.Height(), n)
		}
	})
	return h
}

// columnSpan is where the column i is in the width
func columnSpan(xs []int, i, width int) (x, end int) {
	at := func(v int) int {
		if v < 0 {
			return width + v
		}
		return v
	}
	x, end = at(xs[i]), width
	if i+1 < len(xs) {
		end = at(xs[i+1])
	}
	return x, end
}

// paintColumns draws a row of texts in the columns; mono is the column
// drawn in the monospace font (-1 for none)
func paintColumns(p *ui.ItemPaint, xs []int, texts []string, mono int) {
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
	cnv.SetFontSize(ui.ThemeFontSize())
	cnv.SetColor(text)
	cnv.SetVAlign(ui.VAlignCenter)
	cnv.SetHAlign(ui.HAlignLeft)
	for i, s := range texts {
		cnv.SetFontFamily(ui.FontFamilySans)
		if i == mono {
			cnv.SetFontFamily(ui.FontFamilyMono)
		}
		x, end := columnSpan(xs, i, p.Width)
		cnv.DrawText(x+8, 0, end-x-12, p.Height, s)
	}
}
