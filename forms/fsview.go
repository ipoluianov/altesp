package forms

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ipoluianov/altesp/internal/ops"
	"github.com/ipoluianov/nui/ui"
)

// fsView browses the file system of a partition: a folder at a time. The
// changes are made in memory and written to the board at once.
type fsView struct {
	ui.Panel
	t  *PartitionsTab
	p  ops.Partition
	fs ops.FileSystem

	changed bool
	dir     string // the folder shown: "/", "/www"
	entries []ops.FileInfo

	title    *ui.Label
	pathBox  *ui.Label
	list     *ui.ListView
	model    *ui.FuncModel
	upBtn    *ui.Button
	writeBtn *ui.Button
}

// The columns of the files
var fsCols = []int{0, -230, -130}

func newFSView(t *PartitionsTab, p ops.Partition, fs ops.FileSystem) *fsView {
	v := &fsView{t: t, p: p, fs: fs, dir: "/"}
	v.InitWidget()
	v.SetPanelPadding(0)
	v.SetCellPadding(6)
	v.title = ui.NewLabel("")
	v.AddWidget(0, 0, t.viewerHeader(v.title))

	bar := ui.NewHBox()
	bar.SetCellPadding(6)
	v.upBtn = textButton(func() string { return "↑ " + T().Up }, v.up)
	bar.Add(v.upBtn)
	bar.Add(textButton(func() string { return T().AddFiles }, v.addFilesDialog))
	bar.Add(textButton(func() string { return T().AddFolder }, v.addFolderDialog))
	bar.Add(textButton(func() string { return T().NewFolder }, v.newFolder))
	bar.Add(textButton(func() string { return T().SaveFSImage }, v.saveImage))
	bar.AddStretch()
	v.writeBtn = textButton(func() string { return T().WriteToBoard }, v.write)
	v.writeBtn.SetFontBold(true)
	bar.Add(v.writeBtn)
	v.AddWidget(1, 0, bar)

	v.pathBox = ui.NewLabel("")
	v.pathBox.SetFontFamily(ui.FontFamilyMono)
	v.AddWidget(2, 0, v.pathBox)
	v.AddWidget(3, 0, newColumns(fsCols, func() []string { return []string{T().ColName, T().ColSize, T().ColType} }))
	v.model = ui.NewFuncModel(func() int { return len(v.entries) }, 1, func(row, col int) string { return v.entries[row].Name })
	v.list = ui.NewListView()
	v.list.SetName("fs-files")
	v.list.SetModel(v.model)
	v.list.SetEmptyText(T().EmptyFolder)
	v.list.SetItemPainter(v.paintRow)
	v.list.SetContextMenu(v.menu())
	v.list.OnMouseDown.Connect(func(e *ui.MouseEvent) {
		if e.Button == ui.MouseButtonRight {
			if row := v.list.RowAt(e.X, e.Y-v.list.ScrollY()); row >= 0 {
				v.list.SetCurrentRow(row)
			}
		}
	})
	v.list.OnActivated.Connect(v.open)
	v.AddWidget(4, 0, v.list)
	v.refresh()
	return v
}

func (v *fsView) dirty() bool { return v.changed }

// dropped adds the files and folders dropped on the window to the folder
func (v *fsView) dropped(files []string) bool {
	v.addPaths(files)
	return true
}

// refresh shows the folder and the state
func (v *fsView) refresh() {
	list, err := v.fs.ReadDir(v.dir)
	if err != nil {
		v.t.m.status.ShowMessage(err.Error())
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Dir != list[j].Dir {
			return list[i].Dir
		}
		return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name)
	})
	v.entries = list
	v.model.Changed()
	v.list.Update()
	title := fmt.Sprintf("%s · %s · %s", v.p.Name, v.fs.Kind(), ops.SizeText(int(v.p.Size)))
	if v.changed {
		title += " · " + T().Changed
	}
	v.title.SetText(title)
	v.pathBox.SetText(v.dir)
	v.upBtn.SetEnabled(v.dir != "/")
	v.writeBtn.SetEnabled(v.changed && v.t.canWriteBoard())
}

func (v *fsView) paintRow(p *ui.ItemPaint) {
	if p.Row >= len(v.entries) {
		return
	}
	e := v.entries[p.Row]
	name, size, kind := e.Name, ops.SizeText(int(e.Size)), T().File
	if e.Dir {
		name, size, kind = "▸ "+e.Name, "", T().Folder
	}
	paintColumns(p, fsCols, []string{name, size, kind}, -1)
}

// join is the path of a name in the folder shown
func (v *fsView) join(name string) string { return path.Join(v.dir, name) }

func (v *fsView) current() (ops.FileInfo, bool) {
	i := v.list.CurrentRow()
	if i < 0 || i >= len(v.entries) {
		return ops.FileInfo{}, false
	}
	return v.entries[i], true
}

// open goes into a folder, or downloads a file
func (v *fsView) open(row int) {
	if row < 0 || row >= len(v.entries) {
		return
	}
	if e := v.entries[row]; e.Dir {
		v.dir = v.join(e.Name)
		v.refresh()
		v.list.SetCurrentRow(-1)
		return
	}
	v.download()
}

func (v *fsView) up() {
	if v.dir != "/" {
		v.dir = path.Dir(v.dir)
		v.refresh()
	}
}

// edited shows a change made in memory
func (v *fsView) edited() {
	v.changed = true
	v.refresh()
}

func (v *fsView) menu() *ui.ContextMenu {
	menu := ui.NewContextMenu(v.list)
	item := func(text func() string, f func()) *ui.ContextMenuItem {
		it := menu.AddItem("", f)
		it.SetTextFunc(text)
		return it
	}
	down := item(func() string { return T().Download }, v.download)
	replace := item(func() string { return T().Replace }, v.replace)
	rename := item(func() string { return T().Rename }, v.rename)
	del := item(func() string { return T().Delete }, v.delete)
	menu.AddSeparator()
	item(func() string { return T().AddFiles }, v.addFilesDialog)
	item(func() string { return T().AddFolder }, v.addFolderDialog)
	item(func() string { return T().NewFolder }, v.newFolder)
	menu.AddSeparator()
	item(func() string { return T().SaveFSImage }, v.saveImage)
	menu.OnShow.Connect(func(*ui.ContextMenu) {
		e, ok := v.current()
		down.SetEnabled(ok)
		replace.SetEnabled(ok && !e.Dir)
		rename.SetEnabled(ok)
		del.SetEnabled(ok)
	})
	return menu
}

// download saves a file, or a folder with what is in it
func (v *fsView) download() {
	e, ok := v.current()
	if !ok {
		return
	}
	src := v.join(e.Name)
	if !e.Dir {
		data, err := v.fs.ReadFile(src)
		if err != nil {
			ui.ShowMessageBox(v.t.m.Form.Panel(), T().Download, err.Error())
			return
		}
		v.t.m.saveBytes(T().Download, e.Name, data)
		return
	}
	v.t.m.Form.ShowSelectDirectoryDialog(ui.SelectDirectoryDialogOptions{Title: T().Download}, func(dir string, err error) {
		if err != nil || dir == "" {
			return
		}
		n, err := v.saveTree(src, filepath.Join(dir, e.Name))
		if err != nil {
			ui.ShowMessageBox(v.t.m.Form.Panel(), T().Download, err.Error())
			return
		}
		v.t.m.status.ShowMessage(fmt.Sprintf(T().Downloaded, n, dir))
	})
}

// saveTree writes the folder of the file system to the disk
func (v *fsView) saveTree(src, dst string) (int, error) {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return 0, err
	}
	list, err := v.fs.ReadDir(src)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range list {
		if e.Dir {
			k, err := v.saveTree(path.Join(src, e.Name), filepath.Join(dst, e.Name))
			n += k
			if err != nil {
				return n, err
			}
			continue
		}
		data, err := v.fs.ReadFile(path.Join(src, e.Name))
		if err != nil {
			return n, err
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name), data, 0o644); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// replace writes a file of the disk in place of the file
func (v *fsView) replace() {
	e, ok := v.current()
	if !ok || e.Dir {
		return
	}
	v.t.m.Form.ShowOpenFileDialog(ui.OpenFileDialogOptions{Title: T().Replace}, func(paths []string, err error) {
		if err != nil || len(paths) == 0 {
			return
		}
		data, err := os.ReadFile(paths[0])
		if err == nil {
			err = v.fs.WriteFile(v.join(e.Name), data)
		}
		if err != nil {
			ui.ShowMessageBox(v.t.m.Form.Panel(), T().Replace, err.Error())
			return
		}
		v.edited()
	})
}

func (v *fsView) rename() {
	e, ok := v.current()
	if !ok {
		return
	}
	ui.ShowInputDialog(v.t.m.Form.Panel(), ui.InputDialog{Title: T().Rename, Prompt: T().NewName, Value: e.Name}, func(text string) {
		name := strings.TrimSpace(text)
		if name == "" || name == e.Name {
			return
		}
		if err := v.fs.Rename(v.join(e.Name), v.join(name)); err != nil {
			ui.ShowMessageBox(v.t.m.Form.Panel(), T().Rename, err.Error())
			return
		}
		v.edited()
	})
}

func (v *fsView) delete() {
	e, ok := v.current()
	if !ok {
		return
	}
	ask(v.t.m.Form, T().Delete, fmt.Sprintf(T().DeleteFileAsk, v.join(e.Name)),
		choice{T().Delete, ui.ButtonOther, func() {
			if err := v.removeTree(v.join(e.Name), e.Dir); err != nil {
				ui.ShowMessageBox(v.t.m.Form.Panel(), T().Delete, err.Error())
			}
			v.edited()
		}},
		choice{T().Cancel, ui.ButtonReject, nil})
}

// removeTree removes a file, or a folder with what is in it
func (v *fsView) removeTree(p string, dir bool) error {
	if dir {
		list, err := v.fs.ReadDir(p)
		if err != nil {
			return err
		}
		for _, e := range list {
			if err := v.removeTree(path.Join(p, e.Name), e.Dir); err != nil {
				return err
			}
		}
		// A SPIFFS folder is gone with its files
		if v.fs.Kind() == ops.FSSPIFFS {
			return nil
		}
	}
	return v.fs.Remove(p)
}

func (v *fsView) newFolder() {
	ui.ShowInputDialog(v.t.m.Form.Panel(), ui.InputDialog{Title: T().NewFolder, Prompt: T().NewName}, func(text string) {
		name := strings.TrimSpace(text)
		if name == "" {
			return
		}
		if err := v.fs.Mkdir(v.join(name)); err != nil {
			msg := err.Error()
			if errors.Is(err, ops.ErrNoFolders) {
				msg = T().SPIFFSFolders
			}
			ui.ShowMessageBox(v.t.m.Form.Panel(), T().NewFolder, msg)
			return
		}
		v.edited()
	})
}

func (v *fsView) addFilesDialog() {
	v.t.m.Form.ShowOpenFileDialog(ui.OpenFileDialogOptions{Title: T().AddFiles, AllowMultiple: true}, func(paths []string, err error) {
		if err == nil {
			v.addPaths(paths)
		}
	})
}

func (v *fsView) addFolderDialog() {
	v.t.m.Form.ShowSelectDirectoryDialog(ui.SelectDirectoryDialogOptions{Title: T().AddFolder}, func(dir string, err error) {
		if err == nil && dir != "" {
			v.addPaths([]string{dir})
		}
	})
}

// addPaths copies files and folders of the disk into the folder shown
func (v *fsView) addPaths(paths []string) {
	n := 0
	var bad []string
	for _, p := range paths {
		k, err := v.addPath(p, v.join(filepath.Base(p)))
		n += k
		if err != nil {
			bad = append(bad, err.Error())
		}
	}
	if n > 0 {
		v.edited()
		v.t.m.status.ShowMessage(fmt.Sprintf(T().FilesAdded, n))
	}
	if len(bad) > 0 {
		ui.ShowMessageBox(v.t.m.Form.Panel(), T().AddFiles, strings.Join(bad, "\n"))
	}
}

func (v *fsView) addPath(src, dst string) (int, error) {
	st, err := os.Stat(src)
	if err != nil {
		return 0, err
	}
	if !st.IsDir() {
		data, err := os.ReadFile(src)
		if err != nil {
			return 0, err
		}
		if err := v.fs.WriteFile(dst, data); err != nil {
			return 0, fmt.Errorf("%s: %w", dst, err)
		}
		return 1, nil
	}
	if err := v.fs.Mkdir(dst); err != nil && !errors.Is(err, ops.ErrNoFolders) && !errors.Is(err, os.ErrExist) {
		return 0, fmt.Errorf("%s: %w", dst, err)
	}
	list, err := os.ReadDir(src)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range list {
		k, err := v.addPath(filepath.Join(src, e.Name()), path.Join(dst, e.Name()))
		n += k
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// saveImage saves the file system, with the changes, as a partition image
func (v *fsView) saveImage() {
	v.t.m.saveBytes(T().SaveFSImage, v.p.Name+".bin", v.fs.Image())
}

// write writes the changed sectors of the file system to the board
func (v *fsView) write() {
	if !v.changed || !v.t.canWriteBoard() {
		return
	}
	m := v.t.m
	text := fmt.Sprintf(T().WriteFSAsk, v.p.Name, v.fs.Kind(), v.p.Offset)
	m.confirmDanger(T().WriteToBoard, text, T().Write, v.p.Offset, int(v.p.Size), func() {
		m.runProgress(T().WriteToBoard, T().Writing, true, func(ctx context.Context, s *ops.Session, progress func(int, int)) error {
			return s.WriteFS(ctx, v.p.Name, v.fs, progress)
		}, func(err error) {
			m.logOp(fmt.Sprintf(T().OpWriteFS, v.p.Name, v.fs.Kind()), err)
			if err != nil {
				m.showError(T().WriteToBoard, err)
				return
			}
			v.changed = false
			v.refresh()
			m.status.ShowMessage(fmt.Sprintf(T().PartWritten, v.p.Name))
		})
	})
}
