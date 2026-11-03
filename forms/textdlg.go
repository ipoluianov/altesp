package forms

import (
	"fmt"
	"os"
	"strings"

	"github.com/ipoluianov/altesp/config"
	"github.com/ipoluianov/nui/ui"
)

// showText shows a long text in a dialog: a report, the log of operations.
// It can be copied and saved; saveName is the file name offered.
func (m *MainForm) showText(title, text, saveName string) {
	c := &ui.DialogContent{}
	c.InitWidget()
	box := ui.NewTextBox()
	box.SetMultiline(true)
	box.SetReadOnly(true)
	box.SetFontFamily(ui.FontFamilyMono)
	box.SetText(text)
	box.SetMinSize(720, 420)
	box.SetXExpandable(true)
	box.SetYExpandable(true)
	c.AddWidget(0, 0, box)
	buttons := ui.NewDialogButtonBox()
	buttons.AddButton(T().CopyText, ui.ButtonOther, func() {
		ui.ClipboardSetText(text)
		m.status.ShowMessage(T().Copied)
	})
	buttons.AddButton(T().SaveText, ui.ButtonOther, func() {
		m.saveText(title, saveName, text)
	})
	buttons.AddButton(T().CloseDiff, ui.ButtonReject, func() { c.Form().Close() })
	c.AddWidget(1, 0, buttons)
	c.OnDialogShow = func() { c.Form().SetTitle(title) }
	m.Form.Panel().ShowDialog(c)
}

// saveText saves a text to a file the user chooses
func (m *MainForm) saveText(title, name, text string) {
	m.Form.ShowSaveFileDialog(ui.SaveFileDialogOptions{
		Title:           title,
		DefaultFileName: name,
		Filters:         []ui.FileDialogFilter{{DisplayName: T().TextFiles, Patterns: []string{"*.txt", "*.log", "*.csv"}}},
	}, func(path string, err error) {
		if err != nil || path == "" {
			return
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			ui.ShowMessageBox(m.Form.Panel(), title, err.Error())
			return
		}
		m.status.ShowMessage(fmt.Sprintf(T().Saved, path))
	})
}

// logOp appends a dangerous operation to the log of operations, with the
// board it was done on
func (m *MainForm) logOp(what string, err error) {
	if err != nil {
		what += " — " + T().OpFailed + ": " + firstLine(err.Error())
	}
	mac, port := "", ""
	if m.hasBoard {
		mac, port = m.board.Summary().MAC, m.board.Port.Short
	}
	if lerr := config.LogOperation(mac, port, what); lerr != nil {
		m.status.ShowMessage(lerr.Error())
	}
}

// showOperations shows the log of the dangerous operations
func (m *MainForm) showOperations() {
	lines, err := config.Operations()
	if err != nil {
		ui.ShowMessageBox(m.Form.Panel(), T().OperationsLog, err.Error())
		return
	}
	var b strings.Builder
	if len(lines) == 0 {
		b.WriteString(T().NoOperations + "\n")
	}
	for _, l := range lines {
		fmt.Fprintf(&b, "%s  %-17s  %-10s  %s\n", l.Time, l.MAC, l.Port, l.What)
	}
	fmt.Fprintf(&b, "\n%s\n", config.OperationsLogPath())
	m.showText(T().OperationsLog, b.String(), "operations.log")
}
