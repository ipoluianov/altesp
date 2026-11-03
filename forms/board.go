package forms

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ipoluianov/altesp/internal/ops"
	"github.com/ipoluianov/nui/ui"
)

// boardCard is the card of the board, shown on all the tabs; its content is
// made again by showBoard
func (m *MainForm) boardCard() ui.Widgeter {
	m.card = ui.NewGroupBox("")
	m.card.SetTitleFunc(func() string { return T().Board })
	m.card.SetContextMenu(m.cardMenu())
	m.showBoard()
	return m.card
}

// showBoard fills the card: the board connected last, or a hint
func (m *MainForm) showBoard() {
	box := ui.NewVBox()
	if !m.hasBoard {
		title := ui.NewLabel("")
		title.SetTextFunc(func() string { return T().NoBoard })
		title.SetFontBold(true)
		box.Add(title)
		box.Add(secondaryLabel(func() string { return T().NoBoardHint }))
	} else {
		s := m.board.Summary()
		chip := s.Chip
		if s.Revision != "" {
			chip += " (" + s.Revision + ")"
		}
		title := ui.NewLabel(chip)
		title.SetFontBold(true)
		box.Add(title)
		for _, line := range cardLines(s) {
			box.Add(cardLabel(line))
		}
		if len(s.Apps) > 0 {
			box.Add(fixedSpace(4))
			box.Add(cardLabel(T().OnBoard))
			for _, a := range s.Apps {
				box.Add(cardLabel("  " + appText(a)))
			}
		}
		if head, text := firmwareNotice(m.board); head != "" {
			box.Add(fixedSpace(4))
			// Blue: information to notice, not an error
			h := ui.NewLabel(head)
			h.SetFontBold(true)
			l := ui.NewLabel(wrap(text, cardTextWidth))
			l.SetTooltip(text)
			for _, w := range []*ui.Label{h, l} {
				w.SetForegroundColor(colorInfo.get())
				ui.OnThemeChanged.Connect(func(bool) { w.SetForegroundColor(colorInfo.get()) })
				box.Add(w)
			}
		}
		for _, n := range s.Notes {
			l := cardLabel(wrap(n, cardTextWidth))
			l.SetForegroundColor(colorWait.get())
			l.SetTooltip(n)
			box.Add(l)
		}
	}
	m.card.RemoveAllWidgets()
	m.card.AddWidget(0, 0, box)
}

// firmwareNotice tells in the card that the board has no firmware to boot,
// and what it does then: "" when there is firmware or it is unknown
func firmwareNotice(b ops.Board) (head, text string) {
	switch b.Firmware {
	case ops.FirmwareEmpty:
		loop := T().EmptyLoopUART
		if ops.BuiltinUSB(b.Port) {
			loop = T().EmptyLoopUSB
		}
		return T().NoFirmware, T().EmptyFlash + " " + loop + " " + T().DropToFlash
	case ops.FirmwareNoTable:
		return T().IncompleteFirmware, T().NoPartitionTable + " " + T().DropToFlash
	case ops.FirmwareNoApp:
		return T().IncompleteFirmware, T().NoApplication + " " + T().DropToFlash
	}
	return "", ""
}

// cardLabel is a line of the card: a long one is cut by the width of the
// column, the whole of it in the tooltip
func cardLabel(text string) *ui.Label {
	l := ui.NewLabel(text)
	l.SetXExpandable(true)
	l.SetTooltip(text)
	return l
}

// cardLines are the lines of the card under the chip
func cardLines(s ops.Summary) []string {
	var lines []string
	if s.MAC != "" {
		lines = append(lines, "MAC "+s.MAC)
	}
	flash := T().FlashLabel + " " + s.Flash
	if s.PSRAM != "" {
		flash += " · PSRAM " + s.PSRAM
	}
	lines = append(lines, flash)
	if s.Crystal != "" {
		lines = append(lines, T().Crystal+" "+s.Crystal)
	}
	return append(lines, s.Security...)
}

// cardTextWidth is the room for the text in the card
const cardTextWidth = cardWidth - 30

// wrap breaks the text into lines that fit the width, at the spaces
func wrap(text string, width int) string {
	fits := func(s string) bool {
		w, _, err := ui.MeasureText(ui.FontFamilySans, ui.ThemeFontSize(), s)
		return err != nil || w <= width
	}
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		next := word
		if line != "" {
			next = line + " " + word
		}
		if line != "" && !fits(next) {
			lines = append(lines, line)
			next = word
		}
		line = next
	}
	if line != "" {
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// appText is an application on the board: "sensor-node 1.4.2"
func appText(a ops.AppLine) string {
	return strings.TrimSpace(a.Project + " " + a.Version)
}

// summaryText is the whole card as text: for the clipboard and the report
func summaryText(s ops.Summary) string {
	var b strings.Builder
	line := func(name, value string) {
		if value != "" {
			fmt.Fprintf(&b, "%-12s %s\n", name+":", value)
		}
	}
	line("Port", s.Port)
	line("Adapter", s.Adapter)
	line("Chip", s.Chip)
	line("Revision", s.Revision)
	line("MAC", s.MAC)
	line("Flash", s.Flash)
	line("Flash ID", s.FlashID)
	line("PSRAM", s.PSRAM)
	line("Crystal", s.Crystal)
	line("Features", strings.Join(s.Features, ", "))
	line("Security", strings.Join(s.Security, ", "))
	line("Loader", s.Loader)
	line("Firmware", s.Firmware)
	if len(s.Partitions) > 0 {
		b.WriteString("\nPartitions:\n")
		for _, p := range s.Partitions {
			fmt.Fprintf(&b, "  %-16s %-5s %-9s 0x%06X %s\n", p.Name, p.Type, p.SubType, p.Offset, ops.SizeText(int(p.Size)))
		}
	}
	if len(s.Apps) > 0 {
		b.WriteString("\nApplications:\n")
		for _, a := range s.Apps {
			fmt.Fprintf(&b, "  %-16s %s", a.Partition, appText(a))
			if a.IDFVersion != "" {
				fmt.Fprintf(&b, ", ESP-IDF %s", a.IDFVersion)
			}
			if a.Built != "" {
				fmt.Fprintf(&b, ", built %s", a.Built)
			}
			b.WriteString("\n")
		}
	}
	if len(s.Notes) > 0 {
		b.WriteString("\nNotes:\n")
		for _, n := range s.Notes {
			b.WriteString("  " + n + "\n")
		}
	}
	return b.String()
}

// cardMenu is the context menu of the card
func (m *MainForm) cardMenu() *ui.ContextMenu {
	menu := ui.NewContextMenu(m.card)
	copyMAC := menu.AddItem("", func() {
		ui.ClipboardSetText(m.board.Summary().MAC)
		m.status.ShowMessage(T().Copied)
	})
	copyMAC.SetTextFunc(func() string { return T().CopyMAC })
	copyAll := menu.AddItem("", func() {
		ui.ClipboardSetText(summaryText(m.board.Summary()))
		m.status.ShowMessage(T().Copied)
	})
	copyAll.SetTextFunc(func() string { return T().CopyAll })
	report := menu.AddItem("", m.saveReport)
	report.SetTextFunc(func() string { return T().SaveReport })
	menu.AddSeparator()
	reset := menu.AddItem("", m.resetBoard)
	reset.SetTextFunc(func() string { return T().ResetBoard })
	boot := menu.AddItem("", m.enterBootloader)
	boot.SetTextFunc(func() string { return T().EnterBootloader })
	menu.AddSeparator()
	efuse := menu.AddItem("", m.showEFuse)
	efuse.SetTextFunc(func() string { return T().ShowEFuse })
	menu.OnShow.Connect(func(*ui.ContextMenu) {
		copyMAC.SetEnabled(m.hasBoard && m.board.Summary().MAC != "")
		copyAll.SetEnabled(m.hasBoard)
		report.SetEnabled(m.hasBoard)
		reset.SetEnabled(m.hasSel && !m.busy)
		boot.SetEnabled(m.hasSel && !m.busy)
		efuse.SetEnabled(m.hasSel && !m.busy && m.image == nil)
	})
	return menu
}

// saveReport writes the card to a text file
func (m *MainForm) saveReport() {
	s := m.board.Summary()
	name := strings.ToLower(strings.ReplaceAll(s.Chip, " ", "-"))
	if s.MAC != "" {
		name += "_" + strings.ReplaceAll(s.MAC, ":", "")
	}
	m.Form.ShowSaveFileDialog(ui.SaveFileDialogOptions{
		Title:           T().SaveReport,
		DefaultFileName: name + "_" + time.Now().Format("2006-01-02") + ".txt",
		Filters:         []ui.FileDialogFilter{{DisplayName: T().TextFiles, Patterns: []string{"*.txt"}}},
	}, func(path string, err error) {
		if err != nil || path == "" {
			return
		}
		if err := os.WriteFile(path, []byte(summaryText(s)), 0o644); err != nil {
			ui.ShowMessageBox(m.Form.Panel(), T().SaveReport, err.Error())
			return
		}
		m.status.ShowMessage(fmt.Sprintf(T().Saved, path))
	})
}

// resetBoard restarts the application of the board: from download mode by
// ending the session, else through the monitor's port
func (m *MainForm) resetBoard() {
	switch {
	case m.sess != nil:
		m.disconnect(nil)
	case m.monitor.active():
		m.monitor.reset()
	case isJTAG(m.sel):
		m.status.ShowMessage(T().ConnectFirst)
	default:
		m.onPort(func(ctx context.Context, mon *ops.Monitor) error { return mon.Reset(ctx) })
	}
}

// enterBootloader puts the board into download mode and leaves it there
func (m *MainForm) enterBootloader() {
	switch {
	case m.sess != nil:
		m.status.ShowMessage(T().AlreadyDownload)
	case isJTAG(m.sel):
		m.status.ShowMessage(T().ConnectFirst)
	default:
		m.onPort(func(ctx context.Context, mon *ops.Monitor) error { return mon.Bootloader(ctx) })
	}
}

// onPort opens the port for a moment to do f: a reset with nothing open
func (m *MainForm) onPort(f func(ctx context.Context, mon *ops.Monitor) error) {
	p, ok := m.selectedPort()
	if !ok {
		m.status.ShowMessage(T().NoPortSelected)
		return
	}
	baud := m.monitor.baud()
	m.run(false, func(ctx context.Context, _ *ops.Session) error {
		mon, err := ops.OpenMonitor(p, baud)
		if err != nil {
			return err
		}
		defer mon.Close()
		return f(ctx, mon)
	}, func(err error) { m.showError(T().Board, err) })
}
