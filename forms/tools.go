package forms

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ipoluianov/altesp/internal/ops"
	"github.com/ipoluianov/nui/ui"
)

// toolsMenu is the menu of the Tools button: what is not tied to an object
// on the screen
func (m *MainForm) toolsMenu() *ui.ContextMenu {
	menu := ui.NewContextMenu(nil)
	menu.AddItem(T().ToolEfuse, m.showEFuse).SetEnabled(m.hasSel && m.image == nil)
	menu.AddItem(T().ToolPartitionEditor, m.partitionEditor)
	menu.AddItem(T().ToolImages, m.imageGenerator)
	menu.AddItem(T().ToolCoredump, m.coredump)
	return menu
}

// Coredump analysis

// coredump reads a coredump from the board's partition or from a file and
// shows its tasks with the .elf of the project
func (m *MainForm) coredump() {
	elf := m.monitor.elfPath()
	text := T().CoredumpAsk
	if elf != "" {
		text += "\n\n" + fmt.Sprintf(T().ELFShown, filepath.Base(elf))
	} else {
		text += "\n\n" + T().CoredumpNoELF
	}
	choices := []choice{{T().FromFile, ui.ButtonOther, func() { m.coredumpFile(elf) }}}
	if m.hasSel && m.image == nil {
		choices = append([]choice{{T().FromBoard, ui.ButtonAccept, func() { m.coredumpBoard(elf) }}}, choices...)
	}
	choices = append(choices, choice{T().Cancel, ui.ButtonReject, nil})
	ask(m.Form, T().ToolCoredump, text, choices...)
}

func (m *MainForm) coredumpBoard(elf string) {
	var r *ops.CoredumpReport
	m.run(true, func(ctx context.Context, s *ops.Session) error {
		var err error
		r, err = s.ReadCoredump(ctx, elf)
		return err
	}, func(err error) {
		if err != nil {
			m.showError(T().ToolCoredump, err)
			return
		}
		m.showText(T().ToolCoredump, coredumpText(r), "coredump.txt")
	})
}

func (m *MainForm) coredumpFile(elf string) {
	m.Form.ShowOpenFileDialog(ui.OpenFileDialogOptions{Title: T().ToolCoredump}, func(paths []string, err error) {
		if err != nil || len(paths) == 0 {
			return
		}
		r, err := ops.ParseCoredumpFile(paths[0], elf)
		if err != nil {
			ui.ShowMessageBox(m.Form.Panel(), T().ToolCoredump, err.Error())
			return
		}
		m.showText(T().ToolCoredump, coredumpText(r), strings.TrimSuffix(filepath.Base(paths[0]), filepath.Ext(paths[0]))+".txt")
	})
}

// coredumpText is the report: the exception, then the tasks, the crashed
// one first, with their frames
func coredumpText(r *ops.CoredumpReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s, %s\n", r.Format, r.Version, r.Checksum)
	if r.ELFMismatch {
		b.WriteString("⚠ " + T().CoredumpMismatch + "\n")
	}
	if r.Exception != "" {
		fmt.Fprintf(&b, "\n%s: %s\n", T().Exception, r.Exception)
	}
	tasks := append([]ops.CoredumpTask(nil), r.Tasks...)
	for i := range tasks {
		if tasks[i].Crashed && i > 0 {
			t := tasks[i]
			copy(tasks[1:i+1], tasks[:i])
			tasks[0] = t
		}
	}
	for _, t := range tasks {
		mark := ""
		if t.Crashed {
			mark = "  ← " + T().Crashed
		}
		fmt.Fprintf(&b, "\n%s (%s, %s %d)%s\n", t.Name, t.State, T().Priority, t.Priority, mark)
		if len(t.Frames) == 0 {
			b.WriteString("    " + T().NoFrames + "\n")
		}
		for _, f := range t.Frames {
			b.WriteString("    " + f + "\n")
		}
	}
	return b.String()
}

// Image generator

// The kinds of image the generator makes
const (
	genNVS = iota
	genLittleFS
	genSPIFFS
	genFAT
	genMerged
)

// generateImage makes an image: NVS from a CSV, a file system from a
// folder, a merged image of the files
func generateImage(kind int, src string, size, version int, files []ops.File) ([]byte, error) {
	switch kind {
	case genNVS:
		return ops.BuildNVS(src, size, version)
	case genLittleFS:
		return ops.BuildFS(ops.FSLittleFS, src, size)
	case genSPIFFS:
		return ops.BuildFS(ops.FSSPIFFS, src, size)
	case genFAT:
		return ops.BuildFS(ops.FSFAT, src, size)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%s", T().NothingToFlash)
	}
	return ops.BuildMerged(files)
}

// genSubtypes are the data subtypes of the partitions the images are for
var genSubtypes = map[int]byte{genNVS: 0x02, genLittleFS: 0x83, genSPIFFS: 0x82, genFAT: 0x81}

// imageGenerator shows the dialog of the generator
func (m *MainForm) imageGenerator() {
	c := &ui.DialogContent{}
	c.InitWidget()
	grid := ui.NewPanel()
	grid.SetPanelPadding(0)
	grid.AddLabel(0, 0, T().GenKind)
	kind := ui.NewComboBox()
	for i, k := range []string{T().GenNVS, T().GenLittleFS, T().GenSPIFFS, T().GenFAT, T().GenMerged} {
		kind.AddItem(k, i)
	}
	kind.SetSelectedIndex(0)
	kind.SetMinWidth(360)
	grid.AddWidget(0, 1, kind)
	srcLabel := grid.AddLabel(1, 0, T().GenSource)
	srcRow := ui.NewHBox()
	src := ui.NewTextBox()
	src.SetXExpandable(true)
	srcRow.Add(src)
	browse := textButton(func() string { return T().Browse }, nil)
	srcRow.Add(browse)
	grid.AddWidget(1, 1, srcRow)
	sizeLabel := grid.AddLabel(2, 0, T().SizeLabel)
	size := ui.NewTextBox()
	grid.AddWidget(2, 1, size)
	verLabel := grid.AddLabel(3, 0, T().GenVersion)
	version := ui.NewComboBox()
	version.AddItem(T().NVSv2, 2)
	version.AddItem(T().NVSv1, 1)
	version.SetSelectedIndex(0)
	grid.AddWidget(3, 1, version)
	c.AddWidget(0, 0, grid)
	info := ui.NewLabel("")
	c.AddWidget(1, 0, info)

	cur := func() int { k, _ := kind.ItemData(kind.SelectedIndex()).(int); return k }
	// The size of the partition the image is for, when the board has one
	partSize := func(k int) (ops.Partition, bool) {
		parts, _ := m.shownPartitions()
		for _, p := range parts {
			if p.Type == 0x01 && p.SubType == genSubtypes[k] {
				return p, true
			}
		}
		return ops.Partition{}, false
	}
	update := func() {
		k := cur()
		merged := k == genMerged
		srcLabel.SetVisible(!merged)
		srcRow.SetVisible(!merged)
		sizeLabel.SetVisible(!merged)
		size.SetVisible(!merged)
		verLabel.SetVisible(k == genNVS)
		version.SetVisible(k == genNVS)
		def := 0x100000
		if k == genNVS {
			def = 0x6000
		}
		if p, ok := partSize(k); ok {
			def = int(p.Size)
		}
		size.SetText(fmt.Sprintf("0x%X", def))
		info.SetForegroundColor(colorSecondary.get())
		switch {
		case merged:
			info.SetText(fmt.Sprintf(T().GenMergedInfo, len(m.flash.enabledFiles())))
		case k == genNVS:
			info.SetText(T().GenNVSInfo)
		default:
			info.SetText(T().GenFSInfo)
		}
	}
	kind.OnActivated.Connect(func(int) { update() })
	browse.OnClick.Connect(func(ui.Widgeter) {
		if cur() == genNVS {
			m.Form.ShowOpenFileDialog(ui.OpenFileDialogOptions{Title: T().GenSource,
				Filters: []ui.FileDialogFilter{{DisplayName: "CSV", Patterns: []string{"*.csv"}}}}, func(paths []string, err error) {
				if err == nil && len(paths) > 0 {
					src.SetText(paths[0])
				}
			})
			return
		}
		m.Form.ShowSelectDirectoryDialog(ui.SelectDirectoryDialogOptions{Title: T().GenSource}, func(dir string, err error) {
			if err == nil && dir != "" {
				src.SetText(dir)
			}
		})
	})
	update()

	// build generates the image, or tells why not
	build := func() ([]byte, bool) {
		n, err := ops.ParseNumber(size.Text())
		var data []byte
		if err == nil {
			v, _ := version.ItemData(version.SelectedIndex()).(int)
			data, err = generateImage(cur(), strings.TrimSpace(src.Text()), int(n), v, m.flash.enabledFiles())
		}
		if err != nil {
			info.SetForegroundColor(colorError.get())
			info.SetText(err.Error())
			return nil, false
		}
		return data, true
	}
	name := func() string {
		return [...]string{"nvs", "littlefs", "spiffs", "fat", "merged"}[cur()] + ".bin"
	}
	buttons := ui.NewDialogButtonBox()
	buttons.AddButton(T().GenSave, ui.ButtonOther, func() {
		if data, ok := build(); ok {
			m.saveBytes(T().ToolImages, name(), data)
		}
	})
	buttons.AddButton(T().GenAddToList, ui.ButtonAccept, func() {
		data, ok := build()
		if !ok {
			return
		}
		k := cur()
		addr := uint32(0)
		if p, ok := partSize(k); ok && k != genMerged {
			addr = p.Offset
		}
		m.saveGenerated(name(), data, addr, func() { c.Form().Close() })
	})
	buttons.AddButton(T().CloseDiff, ui.ButtonReject, func() { c.Form().Close() })
	c.AddWidget(2, 0, buttons)
	c.OnDialogShow = func() { c.Form().SetTitle(T().ToolImages) }
	m.Form.Panel().ShowDialog(c)
}

// saveGenerated saves an image and adds it to the flash list at an address
// the user confirms
func (m *MainForm) saveGenerated(name string, data []byte, addr uint32, done func()) {
	m.Form.ShowSaveFileDialog(ui.SaveFileDialogOptions{
		Title:           T().GenAddToList,
		DefaultFileName: name,
		Filters:         []ui.FileDialogFilter{{DisplayName: T().Firmware, Patterns: []string{"*.bin"}}},
	}, func(path string, err error) {
		if err != nil || path == "" {
			return
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			ui.ShowMessageBox(m.Form.Panel(), T().ToolImages, err.Error())
			return
		}
		askAddress(m.Form, filepath.Base(path), addr, func(a uint32) {
			file, _, err := ops.FileFor(path, m.knownBoard())
			if err != nil {
				ui.ShowMessageBox(m.Form.Panel(), T().ToolImages, err.Error())
				return
			}
			file.Addr = a
			m.flash.put(file)
			m.tabs.SetCurrentIndex(tabFlash)
			if done != nil {
				done()
			}
		})
	})
}
