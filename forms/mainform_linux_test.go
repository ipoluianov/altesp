package forms

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ipoluianov/altesp/app"
	"github.com/ipoluianov/altesp/config"
	"github.com/ipoluianov/altesp/internal/ops"
	"github.com/ipoluianov/nui/ui"
	"github.com/ipoluianov/nui/ui/uitest"
)

// shot saves the picture of the window when ALTESP_SHOTS names a directory
func shot(t *testing.T, w *uitest.Window, name string) {
	dir := os.Getenv("ALTESP_SHOTS")
	if dir == "" {
		return
	}
	f, err := os.Create(filepath.Join(dir, name+".png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	png.Encode(f, w.Screenshot())
}

// newTestForm makes the window with its settings in a temporary directory
// and the simulated board as the only port
func newTestForm(t *testing.T) (*MainForm, *uitest.Window) {
	return newTestFormWith(t, ops.SimulateBoard)
}

// newTestFormWith is newTestForm with another simulated board
func newTestFormWith(t *testing.T, simulate func() func()) (*MainForm, *uitest.Window) {
	t.Cleanup(simulate())
	// The tests click faster than a person; TestClickGuard has the guard
	old := clickGuard
	clickGuard = 0
	t.Cleanup(func() { clickGuard = old })
	config.SetDir(t.TempDir())
	config.Load()
	SetLanguage("en")
	t.Cleanup(func() { SetLanguage("en") })
	uitest.SetScreen(1280, 800, 1)
	m := NewMainForm()
	m.Form.SetSize(1100, 640)
	w := uitest.Show(t, m.Form)
	settleAtEnd(t, m, w)
	return m, w
}

// settleAtEnd lets the work in the background finish before the test
// ends: it would reach the simulated board of the next test
func settleAtEnd(t *testing.T, m *MainForm, w *uitest.Window) {
	t.Cleanup(func() {
		m.stopJob()
		loading := func() bool {
			fs, ok := m.memory.src.(*flashSource)
			return ok && fs.loading
		}
		for i := 0; i < 500 && (m.busy || m.listing.Load() || m.monitor.stateKind == monOpening || loading()); i++ {
			w.Wait(10 * time.Millisecond)
		}
	})
}

// waitFor runs the timers and the posted functions until done
func waitFor(t *testing.T, w *uitest.Window, what string, done func() bool) {
	t.Helper()
	for i := 0; i < 1000 && !done(); i++ {
		w.Wait(10 * time.Millisecond)
	}
	if !done() {
		t.Fatal("timed out waiting for", what, "; status:", labelTexts(w))
	}
}

// connect presses Connect and waits for the card
func connect(t *testing.T, m *MainForm, w *uitest.Window) {
	t.Helper()
	w.Click(m.connectBtn)
	waitFor(t, w, "the connection", func() bool { return m.sess != nil && !m.busy })
}

// The window has the top bar, the board card and the four tabs
func TestMainFormTabs(t *testing.T) {
	m, w := newTestForm(t)
	shot(t, w, "01-main")
	w.Find("No board connected")
	w.Find("Connect")
	w.Find("Monitor")
	w.Find("Tools ▾")
	w.Find(T().NoProjects)
	if n := m.tabs.PageCount(); n != 3 {
		t.Fatal("tabs:", n)
	}
	// The only board is selected by itself
	if p, ok := m.selectedPort(); !ok || p.Name != "sim0" {
		t.Fatal("port", p, ok)
	}
	for i := range 3 {
		w.Shortcut("Ctrl+" + string(rune('1'+i)))
		if got := m.tabs.CurrentIndex(); got != i {
			t.Fatalf("Ctrl+%d: tab %d", i+1, got)
		}
	}
	m.tabs.SetCurrentIndex(tabMemory)
	w.Find(T().MemoryHint)
}

// The texts follow the language
func TestMainFormRussian(t *testing.T) {
	_, w := newTestForm(t)
	SetLanguage("ru")
	w.Settle()
	w.Find("Плата не подключена")
	w.Find("Подключить")
	w.Find("Прошить")
	shot(t, w, "02-russian")
}

// Connect fills the card from the board; Disconnect resets it
func TestConnect(t *testing.T) {
	m, w := newTestForm(t)
	connect(t, m, w)
	w.Find("ESP32-C3")
	w.Find("  sensor-node 1.4.2")
	if m.connectBtn.Text() != "Disconnect" {
		t.Error("button:", m.connectBtn.Text())
	}
	if !strings.Contains(m.status.port.Text(), "stub loader") || !strings.Contains(m.status.port.Text(), "USB") {
		t.Error("status:", m.status.port.Text())
	}
	text := summaryText(m.board.Summary())
	for _, want := range []string{"ESP32-C3", "factory", "sensor-node"} {
		if !strings.Contains(text, want) {
			t.Errorf("report has no %q:\n%s", want, text)
		}
	}
	shot(t, w, "03-connected")
	w.Click(m.connectBtn)
	waitFor(t, w, "the disconnection", func() bool { return m.sess == nil && !m.busy })
	// The card stays
	w.Find("ESP32-C3")
}

// A board with an empty flash is told in the card
func TestEmptyBoardCard(t *testing.T) {
	m, w := newTestFormWith(t, ops.SimulateEmptyBoard)
	connect(t, m, w)
	w.Find("No firmware")
	shot(t, w, "03-empty-board")
	if !strings.Contains(summaryText(m.board.Summary()), "the flash is empty") {
		t.Error("the report doesn't tell the empty flash")
	}
}

// Flash connects, writes with progress, resets and shows the monitor
func TestFlash(t *testing.T) {
	m, w := newTestForm(t)
	path := filepath.Join(t.TempDir(), "app.bin")
	data := make([]byte, 2<<20)
	rand.Read(data)
	os.WriteFile(path, data, 0o644)
	m.flash.files = []listFile{{File: ops.File{Path: path, Addr: 0x10000, Size: len(data)}, Enabled: true}}
	m.flash.project = filepath.Dir(path)
	m.flash.changed()
	w.Find(shortFolder(m.flash.project))

	w.Click(m.flash.flashBtn)
	inProgress := false
	waitFor(t, w, "flashing", func() bool {
		if v := m.flash.bar.Value(); !inProgress && v > 0 && v < 1 {
			inProgress = true
			shot(t, w, "04-flashing")
		}
		return !m.busy && m.flash.bar.Value() == 1
	})
	if !strings.HasPrefix(m.flash.progress.Text(), "Written 2 MB in") {
		t.Fatal("progress:", m.flash.progress.Text())
	}
	// Reset into the application, then the monitor window with its log
	if m.sess != nil {
		t.Error("the session is still open")
	}
	if m.monWin == nil {
		t.Fatal("no monitor window")
	}
	waitFor(t, w, "the log", func() bool { return m.monitor.lines.RowCount() >= 5 })
	levels := map[byte]bool{}
	for _, l := range m.monitor.lines.lines {
		levels[l.level] = true
	}
	for _, lv := range []byte{'I', 'W', 'E'} {
		if !levels[lv] {
			t.Errorf("no %c line", lv)
		}
	}
	shot(t, w.Dialog(), "06-monitor")
	m.closeMonitor()
	w.Settle()
	if m.monitor.active() {
		t.Fatal("the port is still open")
	}
	shot(t, w, "05-flashed")
}

// Erase all asks first; Cancel erases nothing
func TestEraseConfirm(t *testing.T) {
	m, w := newTestForm(t)
	connect(t, m, w)
	w.Click(m.flash.eraseBtn)
	d := w.Dialog()
	d.Find("Read flash to a file first…")
	shot(t, d, "07-erase-ask")
	d.Click(d.Find("Cancel"))
	w.Settle()
	if m.busy || m.flash.progress.Text() != "" {
		t.Fatal("erasing after Cancel")
	}
	w.Click(m.flash.eraseBtn)
	d = w.Dialog()
	d.Click(d.Find("Erase"))
	waitFor(t, w, "erasing", func() bool { return !m.busy && strings.HasPrefix(m.flash.progress.Text(), "Flash erased") })
	// The card tells the board is empty now
	w.Find("No firmware")
}

// The monitor strips the ANSI colors of ESP-IDF and tells the levels
func TestLogModel(t *testing.T) {
	var l logModel
	l.add([]byte("\x1b[0;32mI (31) boot: ok\x1b[0m\r\n\x1b[0;31mE (40) app: f"))
	l.add([]byte("ail\x1b"))
	l.add([]byte("[0m\r\nplain\n"))
	want := []logLine{{"I (31) boot: ok", 'I'}, {"E (40) app: fail", 'E'}, {"plain", 0}}
	if len(l.lines) != len(want) {
		t.Fatalf("%q", l.lines)
	}
	for i, w := range want {
		if l.lines[i] != w {
			t.Errorf("%d: %q", i, l.lines[i])
		}
	}
}

// The menu opens under its button; About names the trademark and the stub
func TestMainMenu(t *testing.T) {
	m, w := newTestForm(t)
	w.Click(m.menuBtn)
	if w.Popup() == nil {
		t.Fatal("no menu")
	}
	w.Key(ui.KeyEsc, ui.KeyModifiers{})
	w.Click(m.toolsBtn)
	if w.Popup() == nil {
		t.Fatal("no tools menu")
	}
	text := aboutText()
	for _, want := range []string{"ESP32 is a trademark of Espressif Systems; AltEsp is not affiliated with or endorsed by Espressif.",
		"esp-flasher-stub", "MIT"} {
		if !strings.Contains(text, want) {
			t.Errorf("About has no %q", want)
		}
	}
}

// The recent projects are shown by the two last names of the folder
func TestRecentProjects(t *testing.T) {
	t.Cleanup(ops.SimulateBoard())
	config.SetDir(t.TempDir())
	config.Load()
	config.SetProject("/home/u/sensor-node/build", config.Project{Files: []config.FlashFile{{Path: "/nowhere/app.bin", Address: 0x20000, Enabled: true}}})
	uitest.SetScreen(1280, 800, 1)
	m := NewMainForm()
	w := uitest.Show(t, m.Form)
	settleAtEnd(t, m, w)
	w.Click(w.Find("sensor-node/build"))
	if len(m.flash.files) != 1 || m.flash.files[0].Addr != 0x20000 || m.flash.files[0].Kind != T().Missing {
		t.Fatalf("%+v", m.flash.files)
	}
	if got := shortFolder("/build"); got != "build" {
		t.Error(got)
	}
}

// labelTexts are the texts of the labels, to tell what the window showed
func labelTexts(w *uitest.Window) string {
	var texts []string
	for _, x := range w.FindAll(func(x ui.Widgeter) bool { _, ok := x.(*ui.Label); return ok }) {
		texts = append(texts, x.(*ui.Label).Text())
	}
	return strings.Join(texts, " | ")
}

// The Memory tab reads the flash as it is shown: the partition table at
// 0x8000
func TestMemoryHex(t *testing.T) {
	m, w := newTestForm(t)
	connect(t, m, w)
	w.Shortcut("Ctrl+2")
	if _, ok := m.memory.src.(*flashSource); !ok {
		t.Fatalf("source %T", m.memory.src)
	}
	m.memory.view.GoTo(0x8000)
	waitFor(t, w, "the table read", func() bool {
		// What painting asks for (the frames are painted before the actions)
		m.memory.src.Need(0x8000, 0x8100)
		b, ok := m.memory.src.At(0x8001)
		return ok && b == 0x50
	})
	if b, _ := m.memory.src.At(0x8000); b != 0xAA {
		t.Fatalf("0x8000 is %02X", b)
	}
	if got := m.partitionAt(0x8000); got != "partition table" {
		t.Error("at 0x8000:", got)
	}
	if got := m.partitionAt(0x10010); got != "factory" {
		t.Error("at 0x10010:", got)
	}
	loadShown(t, m, w)
	shot(t, w, "08-memory")
}

// Compare marks the blocks that differ
func TestCompare(t *testing.T) {
	m, w := newTestForm(t)
	connect(t, m, w)
	w.Shortcut("Ctrl+2")
	// Erased flash around the table: only the block of the table differs
	data := make([]byte, 0x3000)
	for i := range data {
		data[i] = 0xFF
	}
	m.memory.compare(0x7000, data)
	waitFor(t, w, "the comparison", func() bool { return !m.busy })
	if len(m.memory.diffs) != 1 || m.memory.diffs[0] != (ops.Range{Addr: 0x8000, Size: 0x1000}) {
		t.Fatalf("diffs %v", m.memory.diffs)
	}
	if !m.memory.diffBox.IsVisible() || !m.memory.view.marked(0x8100) || m.memory.view.marked(0x9000) {
		t.Error("the difference is not shown")
	}
	loadShown(t, m, w)
	shot(t, w, "09-compare")
}

// The Partitions tab marks the app that boots; another OTA slot made active
// takes the mark
func TestOTASwitch(t *testing.T) {
	m, w := newTestFormWith(t, ops.SimulateOTABoard)
	connect(t, m, w)
	w.Shortcut("Ctrl+3")
	active := func() string {
		for _, r := range m.parts.rows {
			if r.Active {
				return r.P.Name
			}
		}
		return ""
	}
	if got := active(); got != "ota_0" {
		t.Fatal("boots:", got)
	}
	for i, r := range m.parts.rows {
		if r.P.Name == "ota_1" {
			m.parts.list.SetCurrentRow(i)
			if r.App == nil || r.App.Version != "1.5.0" {
				t.Errorf("ota_1 app %+v", r.App)
			}
		}
	}
	m.parts.makeActive()
	waitFor(t, w, "the switch", func() bool { return !m.busy })
	if got := active(); got != "ota_1" {
		t.Fatal("after the switch boots:", got)
	}
	shot(t, w, "10-partitions")
}

// A table exported as CSV and imported back is the same
func TestCSVRoundTrip(t *testing.T) {
	m, w := newTestFormWith(t, ops.SimulateOTABoard)
	connect(t, m, w)
	before := append([]ops.Partition(nil), m.board.Partitions...)
	path := filepath.Join(t.TempDir(), "parts.csv")
	if err := m.parts.writeCSV(path, before); err != nil {
		t.Fatal(err)
	}
	csv, _ := os.ReadFile(path)
	if !strings.Contains(string(csv), "ota_1") {
		t.Fatalf("csv:\n%s", csv)
	}
	m.parts.importFrom(path)
	d := w.Dialog()
	shot(t, d, "12-import")
	d.Click(d.Find("Write the table"))
	waitFor(t, w, "the table written", func() bool { return !m.busy })
	after := m.board.Partitions
	if len(after) != len(before) {
		t.Fatalf("%d partitions, were %d", len(after), len(before))
	}
	for i := range before {
		if before[i].Name != after[i].Name || before[i].Offset != after[i].Offset || before[i].Size != after[i].Size {
			t.Errorf("%d: %+v, was %+v", i, after[i], before[i])
		}
	}
}

// A flash dump opens without a board: both tabs show it, writes are off
func TestOpenImage(t *testing.T) {
	m, w := newTestForm(t)
	connect(t, m, w)
	path := filepath.Join(t.TempDir(), "dump.bin")
	m.saveRegionTo(path, 0, m.board.FlashSize, nil)
	waitFor(t, w, "the dump", func() bool { return !m.busy })
	m.disconnect(nil)
	waitFor(t, w, "the disconnection", func() bool { return !m.busy && m.sess == nil })

	m.openImage(path)
	w.Settle()
	if m.tabs.CurrentIndex() != tabPartitions {
		t.Fatal("tab", m.tabs.CurrentIndex())
	}
	if len(m.parts.rows) != 3 || m.parts.rows[2].App == nil || m.parts.rows[2].App.Project != "sensor-node" {
		t.Fatalf("rows %+v", m.parts.rows)
	}
	if !m.parts.imageBar.IsVisible() || m.parts.importB.Enabled() {
		t.Error("the image is not marked, or import is on")
	}
	shot(t, w, "11-image-partitions")
	m.parts.list.SetCurrentRow(1)
	m.parts.list.OnActivated.Emit(1) // phy_init: shown in hex
	if m.tabs.CurrentIndex() != tabMemory || m.memory.view.Caret() < 0xF000 {
		t.Fatal("not shown in hex")
	}
	if b, ok := m.memory.src.At(0x8000); !ok || b != 0xAA {
		t.Fatal("image bytes", b, ok)
	}
	if m.memory.canWrite() {
		t.Error("an image can be written")
	}
	m.memory.view.GoTo(0x8000)
	w.Settle()
	shot(t, w, "13-image-memory")
	w.Click(w.Find("Back to the board"))
	if m.image != nil {
		t.Fatal("still the image")
	}
}

// Find looks for hex bytes and text
func TestParseFind(t *testing.T) {
	for in, want := range map[string]string{`E9 03`: "\xe9\x03", `"esp"`: "esp", "hello": "hello", "0xAA50": "\xaa\x50"} {
		got, err := parseFind(in)
		if err != nil || string(got) != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
}

// loadShown reads the rows the view shows, as painting asks for them
func loadShown(t *testing.T, m *MainForm, w *uitest.Window) {
	t.Helper()
	v := m.memory.view
	from := v.TopAddress()
	to := min(v.src.Size(), from+v.rowsShown()*hexPerRow)
	waitFor(t, w, "the rows shown", func() bool {
		v.src.Need(from, to)
		_, a := v.src.At(from)
		_, b := v.src.At(to - 1)
		return a && b
	})
}

// openPart opens the editor of the partition from the Partitions tab
func openPart(t *testing.T, m *MainForm, w *uitest.Window, name string) partViewer {
	t.Helper()
	w.Shortcut("Ctrl+3")
	for i, r := range m.parts.rows {
		if r.P.Name == name {
			m.parts.list.SetCurrentRow(i)
			m.parts.open(i)
		}
	}
	waitFor(t, w, "the partition read", func() bool { return !m.busy && m.parts.viewer != nil })
	return m.parts.viewer
}

// clickIn clicks the button of the dialog shown over the window
func clickIn(t *testing.T, w *uitest.Window, button string) {
	t.Helper()
	d := w.Dialog()
	d.Click(d.Find(button))
	w.Settle()
}

// An NVS value changed in the editor is written to the board
func TestNVSEdit(t *testing.T) {
	m, w := newTestFormWith(t, func() func() {
		return ops.SimulateBoardWith(ops.SimOptions{NVS: []ops.NVSEntry{
			{Namespace: "app", Key: "count", Type: 0x04, Value: uint64(7)},
			{Namespace: "app", Key: "name", Type: ops.NVSString, Value: "sensor"},
			{Namespace: "wifi", Key: "ssid", Type: ops.NVSString, Value: "home"},
			{Namespace: "wifi", Key: "cal", Type: ops.NVSBlob, Value: []byte{1, 2, 3, 0xFF}},
		}})
	})
	connect(t, m, w)
	v, ok := openPart(t, m, w, "nvs").(*nvsView)
	if !ok {
		t.Fatalf("viewer %T", m.parts.viewer)
	}
	if len(v.rows) != 4 || len(v.spaces) != 2 {
		t.Fatalf("rows %v spaces %v", v.rows, v.spaces)
	}
	w.Settle()
	shot(t, w, "14-nvs")
	// Change app/count through the dialog: a value out of range is refused
	for i, e := range v.rows {
		if e.Key == "count" {
			v.list.SetCurrentRow(i)
			v.editDialog(&e)
		}
	}
	d := w.Dialog()
	boxes := d.FindAll(func(x ui.Widgeter) bool { _, ok := x.(*ui.TextBox); return ok })
	value := boxes[len(boxes)-1].(*ui.TextBox)
	value.SetText("-1")
	d.Click(d.Find("OK"))
	if w.Dialog() == nil {
		t.Fatal("a negative u32 is taken")
	}
	value.SetText("8")
	d.Click(d.Find("OK"))
	w.Settle()
	if e, _ := v.im.Get("app", "count"); e.Value != uint64(8) || !v.dirty() {
		t.Fatalf("after the dialog %+v dirty %v", e, v.dirty())
	}
	v.write()
	clickIn(t, w, "Write")
	waitFor(t, w, "the NVS written", func() bool { return !m.busy })
	if v.dirty() {
		t.Fatal("still changed after writing")
	}
	// Read back from the board
	var got any
	m.run(true, func(ctx context.Context, s *ops.Session) error {
		im, err := s.ReadNVS(ctx, "nvs", "")
		if err == nil {
			e, _ := im.Get("app", "count")
			got = e.Value
		}
		return err
	}, nil)
	waitFor(t, w, "the NVS read", func() bool { return !m.busy })
	if got != uint64(8) {
		t.Fatalf("on the board: %v", got)
	}
	ops, _ := config.Operations()
	if len(ops) != 1 || !strings.Contains(ops[0].What, "NVS nvs") {
		t.Fatalf("log %+v", ops)
	}
}

// A file added to a LittleFS partition and one removed are written back
func TestFSEdit(t *testing.T) {
	m, w := newTestFormWith(t, func() func() {
		return ops.SimulateBoardWith(ops.SimOptions{Files: map[string][]byte{
			"www/index.html": []byte("<h1>hi</h1>"), "cfg.json": []byte("{}")}})
	})
	connect(t, m, w)
	v, ok := openPart(t, m, w, "storage").(*fsView)
	if !ok {
		t.Fatalf("viewer %T", m.parts.viewer)
	}
	if len(v.entries) != 2 || !v.entries[0].Dir || v.entries[0].Name != "www" {
		t.Fatalf("entries %+v", v.entries)
	}
	src := filepath.Join(t.TempDir(), "new.txt")
	os.WriteFile(src, []byte("new file"), 0o644)
	v.addPaths([]string{src})
	for i, e := range v.entries {
		if e.Name == "cfg.json" {
			v.list.SetCurrentRow(i)
		}
	}
	v.delete()
	clickIn(t, w, "Delete")
	v.open(0) // into www
	if v.dir != "/www" || len(v.entries) != 1 {
		t.Fatalf("%s %+v", v.dir, v.entries)
	}
	shot(t, w, "15-fs")
	v.up()
	v.write()
	clickIn(t, w, "Write")
	waitFor(t, w, "the FS written", func() bool { return !m.busy })
	var names []string
	m.run(true, func(ctx context.Context, s *ops.Session) error {
		fs, err := s.ReadFS(ctx, "storage", nil)
		if err != nil {
			return err
		}
		list, err := fs.ReadDir("/")
		for _, e := range list {
			names = append(names, e.Name)
		}
		return err
	}, nil)
	waitFor(t, w, "the FS read", func() bool { return !m.busy })
	if strings.Join(names, ",") != "new.txt,www" && strings.Join(names, ",") != "www,new.txt" {
		t.Fatalf("on the board: %v", names)
	}
}

// A panic in the log is decoded with the .elf
func TestPanicDecode(t *testing.T) {
	elf, _ := filepath.Abs("../internal/formats/elfdbg/testdata/xtensa_app.elf")
	d, err := ops.NewPanicDecoder(elf)
	if err != nil {
		t.Fatal(err)
	}
	crash, ok := d.Symbol("crash")
	if !ok {
		t.Fatal("no crash in the .elf")
	}
	log := fmt.Sprintf("Guru Meditation Error: Core  0 panic'ed (StoreProhibited). Exception was unhandled.\r\n"+
		"\x1b[0;31mBacktrace: 0x%08x:0x3ffb5070\x1b[0m\r\n\r\nELF file SHA256: 0000\r\n", crash)
	m, w := newTestFormWith(t, func() func() { return ops.SimulateBoardWith(ops.SimOptions{AppLog: log}) })
	config.Update(func(s *config.Settings) { s.LastELF = elf })
	w.Shortcut("F6")
	decoded := func() []string {
		var out []string
		for _, l := range m.monitor.lines.lines {
			if l.level == levelDecoded {
				out = append(out, l.text)
			}
		}
		return out
	}
	waitFor(t, w, "the decoded panic", func() bool { return len(decoded()) > 0 })
	if got := strings.Join(decoded(), "\n"); !strings.Contains(got, "crash") {
		t.Fatalf("decoded:\n%s", got)
	}
	shot(t, w.Dialog(), "16-panic")
	// Decode address finds the function; Filter by level keeps the errors
	for i, l := range m.monitor.lines.lines {
		if strings.HasPrefix(l.text, "Backtrace") {
			m.monitor.log.SetCurrentRow(i)
		}
	}
	if line, _ := m.monitor.currentLine(); len(addressesIn(line.text)) == 0 {
		t.Fatal("no address in", line.text)
	}
	m.monitor.setFilter("E", func(l logLine) bool { return l.level == 'E' })
	if n := m.monitor.lines.RowCount(); n != 1 {
		t.Fatal("errors shown:", n)
	}
	m.monitor.setFilter("", nil)
}

// The generator makes images the formats read back
func TestGenerator(t *testing.T) {
	dir := t.TempDir()
	csv := filepath.Join(dir, "nvs.csv")
	os.WriteFile(csv, []byte("key,type,encoding,value\napp,namespace,,\ncount,data,u32,42\n"), 0o644)
	data, err := generateImage(genNVS, csv, 0x6000, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	im, err := ops.ParseNVS(data)
	if err != nil {
		t.Fatal(err)
	}
	if e, ok := im.Get("app", "count"); !ok || e.Value != uint64(42) {
		t.Fatalf("%+v", im.Entries)
	}
	files := filepath.Join(dir, "files")
	os.MkdirAll(filepath.Join(files, "www"), 0o755)
	os.WriteFile(filepath.Join(files, "www", "a.txt"), []byte("A"), 0o644)
	for _, k := range []int{genLittleFS, genSPIFFS, genFAT} {
		data, err := generateImage(k, files, 0x40000, 0, nil)
		if err != nil {
			t.Fatal(k, err)
		}
		fs, err := ops.OpenFS(data)
		if err != nil {
			t.Fatal(k, err)
		}
		if got, err := fs.ReadFile("/www/a.txt"); err != nil || string(got) != "A" {
			t.Fatalf("%d: %q %v", k, got, err)
		}
	}
	a, b := filepath.Join(dir, "a.bin"), filepath.Join(dir, "b.bin")
	os.WriteFile(a, []byte{1, 2}, 0o644)
	os.WriteFile(b, []byte{3}, 0o644)
	merged, err := generateImage(genMerged, "", 0, 0, []ops.File{{Path: b, Addr: 0x10}, {Path: a, Addr: 0}})
	if err != nil || len(merged) != 0x11 || merged[1] != 2 || merged[2] != 0xFF || merged[0x10] != 3 {
		t.Fatalf("%x %v", merged, err)
	}
}

// The partition editor places the rows, and tells the problems
func TestPartitionEditor(t *testing.T) {
	m, w := newTestForm(t)
	connect(t, m, w)
	e := newPartEditor(m)
	if _, problems := e.partitions(); len(problems) != 0 {
		t.Fatal(problems)
	}
	// A row without an offset goes after the last one, an app aligned to 64 KB
	e.rows = append(e.rows, editRow{Name: "data", Type: 1, SubType: 0x83, Size: "0x1000"},
		editRow{Name: "app2", Type: 0, SubType: 0x10, Size: "0x10000"})
	parts, problems := e.partitions()
	if len(problems) != 0 || parts[3].Offset != 0x110000 || parts[4].Offset != 0x120000 {
		t.Fatalf("%+v %v", parts, problems)
	}
	// Overlapping and past the end
	e.rows[4].Offset = "0x100000"
	e.rows = append(e.rows, editRow{Name: "huge", Type: 1, SubType: 0x81, Size: "8M"})
	if _, problems := e.partitions(); len(problems) < 2 {
		t.Fatal("problems:", problems)
	}
	e.show()
	shot(t, w.Dialog(), "17-editor")
	clickIn(t, w, "Close")
}

// The report of a coredump puts the crashed task first
func TestCoredumpText(t *testing.T) {
	r := &ops.CoredumpReport{Format: "ELF", Version: "1.2", Checksum: "CRC32", Exception: "StoreProhibited at 0x400d1234",
		Tasks: []ops.CoredumpTask{{Name: "IDLE", State: "ready"}, {Name: "main", State: "running", Crashed: true, Frames: []string{"crash — app.c:12"}}}}
	text := coredumpText(r)
	if strings.Index(text, "main") > strings.Index(text, "IDLE") || !strings.Contains(text, "crash — app.c:12") {
		t.Fatal(text)
	}
}

// The eFuse window reads the fields; write mode, review and the typed
// word guard a burn of the simulated eFuses
func TestEFuse(t *testing.T) {
	m, w := newTestFormWith(t, func() func() { return ops.SimulateBoardWith(ops.SimOptions{EFuse: true}) })
	connect(t, m, w)
	m.showEFuse()
	waitFor(t, w, "the eFuses", func() bool { return !m.busy && m.efuse != nil })
	v := m.efuse
	v.hideZero.SetChecked(false)
	if len(v.rows) < 20 {
		t.Fatal("rows:", len(v.rows))
	}
	v.filter.SetText("user_data")
	w.Settle()
	found := -1
	for i, r := range v.rows {
		if r.field >= 0 && v.dump.Values[r.field].Field.Name == "USER_DATA" {
			found = i
		}
	}
	if found < 0 {
		t.Fatal("no USER_DATA")
	}
	v.filter.SetText("")
	// Not in write mode: a double click changes nothing
	v.edit(found)
	if len(v.pending) != 0 {
		t.Fatal("edited without write mode")
	}
	v.write.SetChecked(true)
	if !v.banner.IsVisible() {
		t.Fatal("no banner")
	}
	v.setPending("USER_DATA", 0xA5, 0)
	shot(t, w.Top(), "18-efuse")

	v.review()
	c := w.Top()
	burn := c.Find("burn-button").(*ui.Button)
	if burn.Enabled() {
		t.Fatal("Burn is enabled before the word")
	}
	c.Click(c.Find("burn-word"))
	c.Type("burn")
	if burn.Enabled() {
		t.Fatal("Burn is enabled by the lowercase word")
	}
	c.Find("burn-word").(*ui.TextBox).SetText("")
	c.Type("BURN")
	if !burn.Enabled() {
		t.Fatal("Burn is not enabled by BURN")
	}
	shot(t, c, "19-burn-confirm")
	c.Click(burn)
	waitFor(t, w, "the burn", func() bool { return !m.busy && len(v.pending) == 0 })
	waitFor(t, w, "the read-back", func() bool {
		val, _ := v.dump.Value("USER_DATA")
		return !m.busy && val.Raw[0] == 0xA5
	})
	logged, _ := config.Operations()
	if len(logged) != 1 || !strings.Contains(logged[0].What, "USER_DATA") {
		t.Fatalf("log %+v", logged)
	}
	w.Top().Form.Close() // the result
	w.Settle()

	// Clearing bits is refused, with the reasons
	v.pending = []ops.EFuseChange{ops.EFuseUint("USER_DATA", 0)}
	v.review()
	if top := w.Top(); top.Form == v.c.Form() {
		t.Fatal("no refusal shown")
	} else {
		top.Form.Close()
	}
}

// Settings has the language, the theme and the baud rate; the baud rate
// goes to the monitor
func TestSettings(t *testing.T) {
	m, w := newTestForm(t)
	m.settingsDialog()
	d := w.Dialog()
	d.Find("Language")
	d.Find("Theme")
	d.Find("Default port baud rate")
	if n := len(d.FindAll(func(x ui.Widgeter) bool {
		_, a := x.(*ui.ComboBox)
		_, b := x.(*ui.EditableComboBox)
		return a || b
	})); n != 3 {
		t.Fatal("fields:", n)
	}
	d.Form.Close()
	m.applySettings("", "", 9600)
	if config.Get().MonitorBaud != 9600 || m.monitor.baud() != 9600 {
		t.Fatal("baud", config.Get().MonitorBaud, m.monitor.baud())
	}
}

// Ctrl+F in the monitor searches the log; the reset sequence chosen is
// kept for the device
func TestMonitorSearchAndPortMenu(t *testing.T) {
	m, w := newTestForm(t)
	w.Shortcut("F6")
	waitFor(t, w, "the log", func() bool { return m.monitor.lines.RowCount() >= 5 })
	d := w.Dialog()
	d.Shortcut("Ctrl+F")
	if !m.monitor.searchBar.IsVisible() {
		t.Fatal("no search bar")
	}
	d.Type("sensor")
	d.Key(ui.KeyEnter, ui.KeyModifiers{})
	if line, _ := m.monitor.currentLine(); !strings.Contains(line.text, "sensor not found") {
		t.Fatal("found:", line.text)
	}
	m.closeMonitor()
	w.Settle()
	config.Update(func(s *config.Settings) { s.Resets = map[string]string{m.sel.Key(): "manual"} })
	if m.resetFor(m.sel) != ops.ResetManual {
		t.Fatal("the reset sequence is not kept")
	}
	m.showPortMenu()
	if w.Popup() == nil {
		t.Fatal("no port menu")
	}
}

// A 256-bit key typed in hex is burned with its purpose; the read
// protection planned with another key hides that one
func TestEFuseKey(t *testing.T) {
	m, w := newTestFormWith(t, func() func() { return ops.SimulateBoardWith(ops.SimOptions{EFuse: true}) })
	connect(t, m, w)
	m.showEFuse()
	waitFor(t, w, "the eFuses", func() bool { return !m.busy && m.efuse != nil })
	v := m.efuse
	v.hideZero.SetChecked(false)
	v.write.SetChecked(true)
	row := func(name string) int {
		for i, r := range v.rows {
			if r.field >= 0 && v.dump.Values[r.field].Field.Name == name {
				return i
			}
		}
		t.Fatal("no field", name)
		return -1
	}
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	// KEY1 through the dialog: a wrong length is refused
	v.edit(row("KEY1"))
	d := w.Top()
	box := d.Find("key-hex").(*ui.TextBox)
	box.SetText("01 02 03")
	d.Click(d.Find("OK"))
	if w.Top().Form != d.Form {
		t.Fatal("a 3-byte key is taken")
	}
	box.SetText(spacedHex(key))
	shot(t, d, "21-efuse-key")
	d.Click(d.Find("OK"))
	w.Settle()
	// KEY0 read-protected, with its purpose
	v.setPendingRaw("KEY0", key)
	v.setPending("KEY_PURPOSE_0", 4, 0)
	v.setPending("RD_DIS.BLOCK_KEY0", 1, 0)
	if len(v.pending) != 4 {
		t.Fatalf("pending %+v", v.pending)
	}
	plan, err := m.sess.PlanEFuse(v.dump, v.pending)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Warnings) < 2 {
		t.Fatalf("warnings %v", plan.Warnings)
	}
	v.review()
	c := w.Top()
	c.Click(c.Find("burn-word"))
	c.Type("BURN")
	shot(t, c, "22-efuse-key-burn")
	c.Click(c.Find("burn-button"))
	waitFor(t, w, "the burn", func() bool { return !m.busy && len(v.pending) == 0 })
	waitFor(t, w, "the read-back", func() bool {
		k1, _ := v.dump.Value("KEY1")
		return !m.busy && bytes.Equal(k1.Raw, key)
	})
	k0, _ := v.dump.Value("KEY0")
	if !k0.ReadProtected || !bytes.Equal(k0.Raw, make([]byte, 32)) {
		t.Fatalf("KEY0 %v protected %v", k0.Raw, k0.ReadProtected)
	}
	if p, _ := v.dump.Value("KEY_PURPOSE_0"); p.Uint() != 4 {
		t.Fatal("purpose", p.Uint())
	}
}

// A chip on its USB-JTAG: the entry, the card, a flash, no monitor
func TestJTAG(t *testing.T) {
	m, w := newTestFormWith(t, ops.SimulateJTAGBoard)
	if !m.hasSel || !isJTAG(m.sel) || m.portCombo.ItemText(m.portCombo.SelectedIndex()) != "USB-JTAG · C6SIM" {
		t.Fatal("entry", m.sel, m.hasSel)
	}
	connect(t, m, w)
	w.Find("ESP32-C6")
	if s := m.board.Summary(); s.MAC != "40:4C:CA:01:02:03" || !m.board.JTAG {
		t.Fatalf("card %+v", s)
	}
	if !strings.Contains(m.status.port.Text(), "JTAG") {
		t.Error("status:", m.status.port.Text())
	}
	w.Settle()
	shot(t, w, "20-jtag")
	path := filepath.Join(t.TempDir(), "app.bin")
	data := make([]byte, 64<<10)
	rand.Read(data)
	os.WriteFile(path, data, 0o644)
	m.flash.files = []listFile{{File: ops.File{Path: path, Addr: 0x200000, Size: len(data)}, Enabled: true}}
	m.flash.changed()
	w.Click(m.flash.flashBtn)
	waitFor(t, w, "flashing", func() bool { return !m.busy && m.flash.bar.Value() == 1 })
	// Reset after writing; USB-JTAG has no log, so no monitor window
	if m.monWin != nil || m.monitor.active() || m.monitorBtn.Enabled() {
		t.Fatal("monitor", m.monWin != nil, m.monitor.active(), m.monitorBtn.Enabled())
	}
}

// testBootloader is a small bootloader image for the ESP32-C3: one segment,
// DIO, 40 MHz, 4 MB, the checksum, no hash
func testBootloader() []byte {
	img := make([]byte, 24+8+64)
	img[0], img[1], img[2], img[3] = 0xE9, 1, 2, 0x20
	binary.LittleEndian.PutUint32(img[4:], 0x40380000)
	binary.LittleEndian.PutUint16(img[12:], 5) // ESP32-C3
	binary.LittleEndian.PutUint16(img[17:], 0xFFFF)
	binary.LittleEndian.PutUint32(img[24:], 0x3FCD0000)
	binary.LittleEndian.PutUint32(img[28:], 64)
	sum := byte(0xEF)
	for _, b := range img[32:] {
		sum ^= b
	}
	for (len(img)+1)%16 != 0 {
		img = append(img, 0)
	}
	return append(img, sum)
}

// The override of the flash parameters is written into the bootloader's
// header; the rate chosen is kept for the next connection
func TestFlashOverride(t *testing.T) {
	m, w := newTestForm(t)
	connect(t, m, w)
	path := filepath.Join(t.TempDir(), "bootloader.bin")
	os.WriteFile(path, testBootloader(), 0o644)
	file, ok, err := ops.FileFor(path, m.board)
	if err != nil || !ok || file.Kind != "Bootloader" || file.Addr != 0 {
		t.Fatalf("%+v %v %v", file, ok, err)
	}
	m.flash.files = []listFile{{File: file, Enabled: true}}
	m.flash.changed()

	m.flash.paramsDialog()
	d := w.Dialog()
	pick := func(name, value string) {
		cb := d.Find(name).(*ui.ComboBox)
		for i := 0; i < cb.ItemCount(); i++ {
			if cb.ItemText(i) == value {
				cb.SetSelectedIndex(i)
			}
		}
	}
	pick("flash-mode", "QIO")
	pick("flash-freq", "80MHz")
	shot(t, d, "23-override")
	d.Click(d.Find("OK"))
	w.Settle()
	if got := m.flash.overrideText.Text(); !m.flash.overrideRow.IsVisible() || !strings.Contains(got, "QIO 80MHz") {
		t.Fatal("override shown:", got)
	}
	m.flash.reset.SetChecked(false)
	w.Click(m.flash.flashBtn)
	waitFor(t, w, "flashing", func() bool { return !m.busy && m.flash.bar.Value() == 1 })
	var head []byte
	m.run(true, func(ctx context.Context, s *ops.Session) error {
		var err error
		head, err = s.Read(ctx, 0, 16, nil)
		return err
	}, nil)
	waitFor(t, w, "the read", func() bool { return !m.busy })
	if head[2] != 0 || head[3] != 0x2F {
		t.Fatalf("header % X", head[:4])
	}
	// Cleared by its ✕
	m.flash.setParams(ops.FlashParams{})
	if m.flash.overrideRow.IsVisible() {
		t.Fatal("still shown")
	}

	m.flash.setBaud(460800)
	if config.Get().FlashBaud != 460800 {
		t.Fatal("baud", config.Get().FlashBaud)
	}
}

// With flash encryption on, "Encrypt while writing" reaches the options
func TestEncryptOption(t *testing.T) {
	m, w := newTestFormWith(t, func() func() { return ops.SimulateBoardWith(ops.SimOptions{FlashEncryption: true}) })
	connect(t, m, w)
	if !m.board.Security.FlashEncryption {
		t.Fatal("no flash encryption")
	}
	if o := m.flash.flashOptions(); o.Encrypt {
		t.Fatal("encrypting unasked")
	}
	m.flash.encrypt = true
	o := m.flash.flashOptions()
	if !o.Encrypt || o.Verify {
		t.Fatalf("%+v", o)
	}
	// A write with the chip encrypting: the warning of plain images is
	// not there, nothing is verified
	path := filepath.Join(t.TempDir(), "data.bin")
	os.WriteFile(path, bytes.Repeat([]byte{0x5A}, 0x2000), 0o644)
	m.flash.files = []listFile{{File: ops.File{Path: path, Addr: 0x200000, Size: 0x2000}, Enabled: true}}
	m.flash.reset.SetChecked(false)
	w.Click(m.flash.flashBtn)
	waitFor(t, w, "the encrypted write", func() bool { return !m.busy && m.flash.bar.Value() == 1 })
}

// The monitor is a window over the main one: a board in download mode is
// reset into its application first; the log of the window before stays,
// under a line of its own
func TestMonitorWindow(t *testing.T) {
	m, w := newTestForm(t)
	connect(t, m, w)
	w.Click(m.monitorBtn)
	waitFor(t, w, "the log", func() bool { return m.monitor.lines.RowCount() >= 5 })
	if m.sess != nil || m.monWin == nil || m.monitorBtn.Enabled() {
		t.Fatal("session", m.sess != nil, "window", m.monWin != nil)
	}
	w.Dialog().Find("Close")
	m.closeMonitor()
	w.Settle()
	if m.monWin != nil || m.monitor.active() || !m.monitorBtn.Enabled() {
		t.Fatal("after closing: window", m.monWin != nil, "port", m.monitor.active())
	}
	before := m.monitor.lines.RowCount()
	w.Shortcut("F6")
	waitFor(t, w, "the log again", func() bool { return m.monitor.lines.RowCount() > before+1 })
	marks := 0
	for _, l := range m.monitor.lines.lines {
		if l.level == levelMark {
			marks++
		}
	}
	if marks != 1 {
		t.Fatal("marks:", marks)
	}
	m.closeMonitor()
	w.Settle()
	if config.Get().MonitorWidth <= 0 {
		t.Error("the size of the window is not kept")
	}
}

// Check for updates asks the server only when asked, and offers the page
func TestCheckUpdates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tag_name":"v9.9.9","html_url":"https://example.com/r"}`))
	}))
	defer srv.Close()
	t.Cleanup(app.SetReleasesURL(srv.URL))
	m, w := newTestForm(t)
	m.checkUpdates()
	waitFor(t, w, "the answer", func() bool { return w.Top().Form != m.Form })
	d := w.Top()
	d.Find("Open the release page")
	d.Find("AltEsp v9.9.9 is available (you have dev).")
	d.Form.Close()
}

// Refresh drops the entry of a board unplugged while connected, and its
// session; the card stays; the only board left is selected
func TestRefreshPorts(t *testing.T) {
	m, w := newTestForm(t)
	connect(t, m, w)
	m.sel = ops.PortInfo{Name: "/dev/ttyUSB9", Short: "ttyUSB9", USB: true, VID: 0x10c4, PID: 0xea60, SerialNumber: "gone"}
	w.Click(m.refreshBtn)
	w.Settle()
	if m.sess != nil || !m.hasBoard {
		t.Fatal("session", m.sess != nil, "card", m.hasBoard)
	}
	if !m.hasSel || m.sel.Name != "sim0" {
		t.Fatal("selected:", m.sel.Name, m.hasSel)
	}
	if strings.Contains(m.portCombo.ItemText(m.portCombo.SelectedIndex()), "ttyUSB9") {
		t.Fatal("the unplugged port is still listed")
	}
}

// A board unplugged while connected is disconnected by itself: through a
// bridge at once, on its own USB when its port stays away past a reset
func TestUnplugged(t *testing.T) {
	m, w := newTestForm(t)
	connect(t, m, w)
	m.sel = ops.PortInfo{Name: "/dev/ttyUSB9", Short: "ttyUSB9", USB: true, VID: 0x10c4, PID: 0xea60, SerialNumber: "gone"}
	m.checkUnplugged()
	w.Settle()
	if m.sess != nil || !m.hasBoard {
		t.Fatal("bridge: session", m.sess != nil, "card", m.hasBoard)
	}
	waitFor(t, w, "the board left", func() bool { return m.hasSel && m.sel.Name == "sim0" })

	connect(t, m, w)
	m.sel = ops.PortInfo{Name: "/dev/ttyACM9", Short: "ttyACM9", USB: true, VID: 0x303a, PID: 0x1001, SerialNumber: "AA:BB"}
	m.checkUnplugged()
	if m.sess == nil {
		t.Fatal("own USB: disconnected at once, as if it were a reset")
	}
	m.goneSince = time.Now().Add(-unplugWait)
	m.resetAt = time.Time{}
	m.checkUnplugged()
	w.Settle()
	if m.sess != nil {
		t.Fatal("own USB: still connected after the wait")
	}
}

// The guide shows each topic with its picture and text; it moves
func TestGuide(t *testing.T) {
	m, w := newTestForm(t)
	m.showGuide()
	d := w.Dialog()
	g := m.guide
	for i := range g.topics {
		g.list.SetCurrentRow(i)
		d.Settle()
		if g.current != i || g.title.Text() != g.topics[i].title() {
			t.Fatalf("topic %d: %q", i, g.title.Text())
		}
		// A few moments of each picture
		for _, at := range []float64{0.2, 1.7, 3.9} {
			g.start = time.Now().Add(-time.Duration(at * float64(time.Second)))
			d.Settle()
			shot(t, d, fmt.Sprintf("guide-%02d-%.0f", i, at*10))
		}
	}
	d.Click(d.Find("Next →"))
	if g.current != 0 {
		t.Fatal("Next after the last topic:", g.current)
	}
	d.Form.Close()
	w.Settle()
	// In Russian from the start: the language can't change while it is open
	SetLanguage("ru")
	m.showGuide()
	d = w.Dialog()
	d.Find("Дальше →")
	for i := range m.guide.topics {
		m.guide.list.SetCurrentRow(i)
		m.guide.start = time.Now().Add(-1700 * time.Millisecond)
		d.Settle()
		shot(t, d, fmt.Sprintf("guide-ru-%02d", i))
	}
}

// Every text of the guide is translated
func TestGuideTranslated(t *testing.T) {
	if miss := guideCatalog.Missing(); len(miss) > 0 {
		t.Fatal("not translated:", miss)
	}
	if len(guideRussian.AreaText) != len(flashAreas) || len(guideEnglish.AreaText) != len(flashAreas) {
		t.Fatal("area texts", len(guideEnglish.AreaText), len(guideRussian.AreaText), len(flashAreas))
	}
}

// The strip of the data beside the map tells the sectors that hold
// something other than FF: found out by MD5 on the board, from the bytes
// of an image
func TestDataStrip(t *testing.T) {
	m, w := newTestForm(t)
	connect(t, m, w)
	w.Shortcut("Ctrl+2")
	fs := m.memory.src.(*flashSource)
	waitFor(t, w, "the scan", func() bool {
		// What painting the map asks for (the frames are painted before the actions)
		for _, u := range m.memory.sectorUses() {
			if u == useUnknown {
				return false
			}
		}
		return true
	})
	use := fs.sectorUses()
	for addr, want := range map[int]sectorUse{0: useData, 0x8000: useData, 0x10000: useData, 0x9000: useErased, 0x200000: useErased} {
		if use[addr/pageSize] != want {
			t.Errorf("0x%X: %d, want %d", addr, use[addr/pageSize], want)
		}
	}
	if tip := m.memory.fmap.tooltipAt(0); !strings.Contains(tip, "data") {
		t.Error("tooltip:", tip)
	}
	shot(t, w, "08-memory-strip")

	// An image: from its bytes
	data := bytes.Repeat([]byte{0xFF}, 0x10000)
	data[0x3005] = 1
	got := imageUses(data)
	if len(got) != 16 || got[3] != useData || got[2] != useErased {
		t.Fatalf("image: %v", got)
	}
}

// The log has the flashing and every failure, once
func TestOperationsLog(t *testing.T) {
	m, w := newTestForm(t)
	path := filepath.Join(t.TempDir(), "app.bin")
	data := make([]byte, 64<<10)
	rand.Read(data)
	os.WriteFile(path, data, 0o644)
	m.flash.files = []listFile{{File: ops.File{Path: path, Addr: 0x10000, Size: len(data)}, Enabled: true}}
	m.flash.reset.SetChecked(false)
	m.flash.changed()
	w.Click(m.flash.flashBtn)
	waitFor(t, w, "flashing", func() bool { return !m.busy && m.flash.bar.Value() == 1 })
	err := fmt.Errorf("reading at 0x20000: no answer from the chip")
	m.logOp("Erased the partition x", err)
	m.showError("Erase", err)
	w.Settle()
	if d := w.Top(); d.Form != m.Form {
		d.Form.Close()
	}
	m.showError("Save to file", fmt.Errorf("reading at 0x30000: no answer"))
	w.Settle()
	lines, _ := config.Operations()
	var whats []string
	for _, l := range lines {
		whats = append(whats, l.What)
	}
	text := strings.Join(whats, "\n")
	for _, want := range []string{"Flashed app.bin @0x10000", "reading at 0x20000", "Save to file — failed: reading at 0x30000"} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in the log:\n%s", want, text)
		}
	}
	if n := strings.Count(text, "0x20000"); n != 1 {
		t.Errorf("logged %d times:\n%s", n, text)
	}
}

// A second click soon after the first doesn't cancel the connection, nor
// disconnect the board just connected; the Connect button of the Memory
// tab only connects
func TestClickGuard(t *testing.T) {
	m, w := newTestForm(t)
	clickGuard = time.Second
	w.Click(m.connectBtn)
	if !m.busy {
		t.Fatal("not connecting")
	}
	m.connect()
	if m.cancel == nil && m.sess == nil && !m.busy {
		t.Fatal("the second click cancelled the connection")
	}
	waitFor(t, w, "the connection", func() bool { return m.sess != nil && !m.busy })
	m.connect()
	if m.sess == nil {
		t.Fatal("a click at once disconnected the board")
	}
	if m.connectBtn.Enabled() {
		t.Error("the button takes clicks while the state settles")
	}
	waitFor(t, w, "the button", func() bool { return m.connectBtn.Enabled() })
	if m.connectBtn.Text() != "Disconnect" {
		t.Fatal("button:", m.connectBtn.Text())
	}
	// The Memory tab's button doesn't disconnect
	m.connectOnly()
	if m.sess == nil {
		t.Fatal("connectOnly disconnected the board")
	}
}

// Read flash asks how and where: in parts, a folder is made in the folder
// chosen, a build folder; then it offers to open it
func TestExportDialog(t *testing.T) {
	m, w := newTestForm(t)
	connect(t, m, w)
	dir := t.TempDir()
	config.Update(func(s *config.Settings) { s.ExportDir = dir })
	w.Click(m.flash.readBtn)
	d := w.Dialog()
	e := m.flash.export
	if e.kind != ops.ExportWhole || filepath.Dir(e.path.Text()) != dir || !strings.HasSuffix(e.path.Text(), ".bin") {
		t.Fatal("whole:", e.kind, e.path.Text())
	}
	shot(t, d, "22-export")
	d.Click(e.kinds[2])
	if e.kind != ops.ExportTrimmed || !strings.HasSuffix(e.path.Text(), "_trimmed.bin") || e.userData.Enabled() {
		t.Fatal("trimmed:", e.path.Text())
	}
	d.Click(e.kinds[1])
	if e.kind != ops.ExportParts || e.path.Text() != dir || !e.userData.Enabled() || !e.userData.Checked() {
		t.Fatal("parts:", e.path.Text())
	}
	shot(t, d, "23-export-parts")
	d.Click(d.Find("Save"))
	waitFor(t, w, "the export", func() bool { return !m.busy && m.flash.bar.Value() == 1 })
	done := w.Dialog()
	done.Find("Open the folder")
	shot(t, done, "24-export-done")
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || !entries[0].IsDir() {
		t.Fatal("folders:", entries)
	}
	folder := filepath.Join(dir, entries[0].Name())
	if !strings.HasPrefix(entries[0].Name(), "esp32-c3_") {
		t.Error("folder name", entries[0].Name())
	}
	for _, f := range []string{"flasher_args.json", "flash_args", "bootloader.bin", "partition-table.bin", "factory.bin"} {
		if _, err := os.Stat(filepath.Join(folder, f)); err != nil {
			t.Error(err)
		}
	}
}

// The empty list shows the hint with links; a build folder is chosen by
// its button as one dropped
func TestBuildFolderButton(t *testing.T) {
	m, w := newTestForm(t)
	if m.flash.list.IsVisible() || !m.flash.emptyBox.IsVisible() {
		t.Fatal("the empty list doesn't show the hint")
	}
	w.Find("Add files…")
	w.Find("Choose a build folder…")
	shot(t, w, "03b-empty-list")
	// A folder saved in parts is a build
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "bootloader.bin"), testBootloader(), 0o644)
	os.WriteFile(filepath.Join(dir, "flasher_args.json"), []byte(`{"flash_files": {"0x0": "bootloader.bin"}, "extra_esptool_args": {"chip": "esp32c3"}}`), 0o644)
	m.flash.addPaths([]string{dir})
	w.Settle()
	if len(m.flash.files) != 1 || m.flash.files[0].Addr != 0 || !m.flash.list.IsVisible() || m.flash.emptyBox.IsVisible() {
		t.Fatalf("files %v", m.flash.files)
	}
	w.Find("Build folder…")
}

// The text windows open at a size to read them
func TestTextDialogSize(t *testing.T) {
	m, w := newTestForm(t)
	m.logOp("Erased the whole flash", nil)
	m.showOperations()
	d := w.Dialog()
	if wd, h := d.Form.Size(); wd < 800 || h < 500 {
		t.Fatalf("size %dx%d", wd, h)
	}
	shot(t, d, "25-operations")
}
