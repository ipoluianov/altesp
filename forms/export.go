package forms

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ipoluianov/altesp/config"
	"github.com/ipoluianov/altesp/internal/ops"
	"github.com/ipoluianov/nui/ui"
)

// The Read flash dialog: how the firmware is saved — the whole flash, in
// parts into a folder, one file without the erased tail, the application
// alone — and where

// exportDialog is the dialog shown last (for the tests)
type exportDialog struct {
	c        *ui.DialogContent
	kinds    []*ui.RadioButton
	userData *ui.Checkbox
	path     *ui.TextBox
	note     *ui.Label
	kind     ops.ExportKind
}

// exportWidth is the width the notes of the dialog are wrapped to
const exportWidth = 600

// exportKinds are the kinds offered, in their order
var exportKinds = []ops.ExportKind{ops.ExportWhole, ops.ExportParts, ops.ExportTrimmed, ops.ExportApp}

// exportName is the start of the names saved: the chip, its MAC, the date
func (m *MainForm) exportName() string {
	return m.dumpName() + "_" + time.Now().Format("2006-01-02")
}

// exportSuffix ends the name of a file of the kind
func exportSuffix(k ops.ExportKind) string {
	switch k {
	case ops.ExportTrimmed:
		return "_trimmed.bin"
	case ops.ExportApp:
		return "_app.bin"
	}
	return ".bin"
}

// showExport opens the dialog
func (f *FlashTab) showExport() {
	m := f.m
	d := &exportDialog{kind: ops.ExportWhole}
	f.export = d
	c := &ui.DialogContent{}
	c.InitWidget()
	d.c = c

	radios := ui.NewPanel()
	radios.SetPanelPadding(0)
	labels := []string{T().ExportWhole, T().ExportParts, T().ExportTrimmed, T().ExportApp}
	for i, k := range exportKinds {
		k := k
		r := ui.NewRadioButton(labels[i])
		r.OnActivated.Connect(func(bool) { d.choose(m, k) })
		// The option of the parts goes under them: one row more after
		row := i
		if i > 1 {
			row++
		}
		radios.AddWidget(row, 0, r)
		d.kinds = append(d.kinds, r)
	}
	d.userData = ui.NewCheckbox(T().ExportUserData)
	d.userData.SetChecked(true)
	// Under the parts, which it is an option of
	userRow := ui.NewHBox()
	userRow.AddSpacing(26)
	userRow.Add(d.userData)
	userRow.AddStretch()

	d.path = ui.NewTextBox()
	d.path.SetMinWidth(460)
	d.path.SetXExpandable(true)
	browse := textButton(func() string { return T().Browse }, func() { d.browse(m) })
	pathRow := ui.NewHBox()
	pathRow.SetCellPadding(6)
	pathRow.Add(d.path)
	pathRow.Add(browse)

	d.note = secondaryLabel(func() string { return "" })

	buttons := ui.NewDialogButtonBox()
	buttons.AddButton(T().SaveBtn, ui.ButtonAccept, func() {
		kind, user, path := d.kind, d.userData.Checked(), d.path.Text()
		c.Form().Close()
		c.RunInParent(func() { f.saveExport(kind, user, path) })
	})
	buttons.AddButton(T().Cancel, ui.ButtonReject, func() { c.Form().Close() })

	radios.AddWidget(2, 0, userRow)
	c.AddWidget(0, 0, radios)
	c.AddWidget(2, 0, pathRow)
	c.AddWidget(3, 0, d.note)
	c.AddWidget(4, 0, buttons)
	c.OnDialogShow = func() { c.Form().SetTitle(T().ReadFlashTitle) }
	d.kinds[0].SetChecked(true)
	d.choose(m, ops.ExportWhole)
	m.Form.Panel().ShowDialog(c)
}

// choose takes the kind: the path is a file or, for the parts, the folder
// the new folder is made in
func (d *exportDialog) choose(m *MainForm, k ops.ExportKind) {
	old := d.kind
	d.kind = k
	for i, r := range d.kinds {
		r.SetChecked(exportKinds[i] == k)
	}
	d.userData.SetEnabled(k == ops.ExportParts)
	dir := config.Get().ExportDir
	if dir == "" {
		dir, _ = os.UserHomeDir()
	}
	if cur := d.path.Text(); cur != "" {
		if old == ops.ExportParts {
			dir = cur
		} else {
			dir = filepath.Dir(cur)
		}
	}
	if k == ops.ExportParts {
		d.path.SetText(dir)
	} else {
		d.path.SetText(filepath.Join(dir, m.exportName()+exportSuffix(k)))
	}
	d.showNote(m)
}

// showNote tells what is made: the folder for the parts; what the kind
// leaves out
func (d *exportDialog) showNote(m *MainForm) {
	text := ""
	switch d.kind {
	case ops.ExportParts:
		text = fmt.Sprintf(T().ExportFolderNote, m.exportName())
	case ops.ExportTrimmed:
		text = T().ExportTrimmedNote
	case ops.ExportApp:
		text = T().ExportAppNote
	}
	if m.hasBoard && m.board.Security.FlashEncryption {
		text += "\n" + T().ExportEncrypted
	}
	wrapped := wrap(text, exportWidth)
	d.note.SetTextFunc(func() string { return wrapped })
}

// browse chooses the file or the folder
func (d *exportDialog) browse(m *MainForm) {
	form := d.c.Form()
	if d.kind == ops.ExportParts {
		form.ShowSelectDirectoryDialog(ui.SelectDirectoryDialogOptions{Title: T().ReadFlashTitle, DefaultDirectory: d.path.Text()}, func(path string, err error) {
			if err == nil && path != "" {
				d.path.SetText(path)
			}
		})
		return
	}
	form.ShowSaveFileDialog(ui.SaveFileDialogOptions{
		Title:            T().ReadFlashTitle,
		DefaultDirectory: filepath.Dir(d.path.Text()),
		DefaultFileName:  filepath.Base(d.path.Text()),
		Filters:          []ui.FileDialogFilter{{DisplayName: T().Firmware, Patterns: []string{"*.bin"}}},
	}, func(path string, err error) {
		if err == nil && path != "" {
			d.path.SetText(path)
		}
	})
}

// saveExport reads and saves; then offers to open the folder
func (f *FlashTab) saveExport(kind ops.ExportKind, userData bool, path string) {
	m := f.m
	if path == "" {
		return
	}
	if kind == ops.ExportParts {
		config.Update(func(s *config.Settings) { s.ExportDir = path })
	} else {
		config.Update(func(s *config.Settings) { s.ExportDir = filepath.Dir(path) })
	}
	start := time.Now()
	var last time.Time
	var written string
	var warnings []string
	var total int
	name := m.exportName()
	f.showProgress(0, T().Reading)
	m.run(true, func(ctx context.Context, s *ops.Session) error {
		files, warn, err := s.Export(ctx, ops.ExportOptions{Kind: kind, UserData: userData}, func(done, all int) {
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
		warnings = warn
		for _, fl := range files {
			total += len(fl.Data)
		}
		written, err = s.WriteExport(files, kind, path, name)
		return err
	}, func(err error) {
		if err != nil {
			f.showProgress(0, "")
			m.showError(T().ReadFlashTitle, err)
			return
		}
		f.showProgress(1, fmt.Sprintf(T().ReadDone, ops.SizeText(total), written))
		folder := written
		if kind != ops.ExportParts {
			folder = filepath.Dir(written)
		}
		text := fmt.Sprintf(T().ExportDone, ops.SizeText(total), written)
		for _, w := range warnings {
			text += "\n\n" + w
		}
		ask(m.Form, T().ReadFlashTitle, text,
			choice{T().OpenFolder, ui.ButtonAccept, func() { ui.OpenURL(folder) }},
			choice{T().CloseDiff, ui.ButtonReject, nil})
	})
}
