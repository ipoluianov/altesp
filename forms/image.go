package forms

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/ipoluianov/altesp/internal/ops"
	"github.com/ipoluianov/nui/ui"
)

// Without a board the Memory and Partitions tabs show an image file: a
// flash dump, a merged image, a partition image. They show it exactly as
// the flash of a board, and only what reads it works.

// openImageDialog asks for the image file to open
func (m *MainForm) openImageDialog() {
	m.Form.ShowOpenFileDialog(ui.OpenFileDialogOptions{
		Title:   T().OpenImage,
		Filters: []ui.FileDialogFilter{{DisplayName: T().Firmware, Patterns: []string{"*.bin"}}, {DisplayName: T().AllFiles, Patterns: []string{"*"}}},
	}, func(paths []string, err error) {
		if err == nil && len(paths) > 0 {
			m.openImage(paths[0])
		}
	})
}

// openImage shows the file in the Memory and Partitions tabs
func (m *MainForm) openImage(path string) {
	im, err := ops.OpenImage(path)
	if err != nil {
		ui.ShowMessageBox(m.Form.Panel(), T().OpenImage, err.Error())
		return
	}
	if len(im.Data) == 0 {
		ui.ShowMessageBox(m.Form.Panel(), T().OpenImage, T().EmptyFile)
		return
	}
	m.image = im
	m.syncViews()
	if i := m.tabs.CurrentIndex(); i != tabMemory && i != tabPartitions {
		if len(im.Partitions) > 0 {
			m.tabs.SetCurrentIndex(tabPartitions)
		} else {
			m.tabs.SetCurrentIndex(tabMemory)
		}
	}
	m.status.ShowMessage(fmt.Sprintf(T().ImageOpened, filepath.Base(path), ops.SizeText(len(im.Data))))
}

// closeImage goes back to the board
func (m *MainForm) closeImage() {
	m.image = nil
	m.syncViews()
}

// newImageBar is the line over a tab that tells an image is shown, with
// the way back to the board
func (m *MainForm) newImageBar() *ui.HBox {
	bar := ui.NewHBox()
	bar.SetCellPadding(6)
	l := ui.NewLabel("")
	l.SetName("image-label")
	l.SetFontBold(true)
	l.SetForegroundColor(colorWait.get())
	ui.OnThemeChanged.Connect(func(bool) { l.SetForegroundColor(colorWait.get()) })
	bar.Add(l)
	bar.Add(textButton(func() string { return T().BackToBoard }, m.closeImage))
	bar.AddStretch()
	bar.SetVisible(false)
	return bar
}

// showImageBar shows the bar when an image is open
func (m *MainForm) showImageBar(bar *ui.HBox) {
	if m.image == nil {
		bar.SetVisible(false)
		return
	}
	for _, w := range bar.Widgets() {
		if l, ok := w.(*ui.Label); ok && l.Name() == "image-label" {
			l.SetText(fmt.Sprintf(T().ImageShown, filepath.Base(m.image.Path), ops.SizeText(len(m.image.Data))))
			l.SetTooltip(m.image.Path)
		}
	}
	bar.SetVisible(true)
}

// shownPartitions are the partitions of the image shown, or of the board,
// and the address of their table (0 when there is none)
func (m *MainForm) shownPartitions() ([]ops.Partition, uint32) {
	if m.image != nil {
		return m.image.Partitions, m.image.PartitionTableAddr
	}
	if m.hasBoard {
		return m.board.Partitions, m.board.PartitionTableAddr
	}
	return nil, 0
}

// boardChanged shows the board as it is now: its card and its partitions.
// The jobs that write the flash also drop what the Memory tab read.
func (m *MainForm) boardChanged() {
	m.showBoard()
	m.syncViews()
}

// runWrite runs a job that writes the flash
func (m *MainForm) runWrite(work func(ctx context.Context, s *ops.Session) error, done func(err error)) bool {
	return m.run(true, work, func(err error) {
		m.memory.invalidate()
		if done != nil {
			done(err)
		}
	})
}

// syncViews shows in the Memory and Partitions tabs what there is
func (m *MainForm) syncViews() {
	m.memory.sync()
	m.parts.sync()
}
