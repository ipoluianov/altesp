package forms

import (
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ipoluianov/altesp/config"
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
func newTestForm(t *testing.T) (*MainForm, *uitest.Window) {
	config.SetDir(t.TempDir())
	config.Load()
	SetLanguage("en")
	t.Cleanup(func() { SetLanguage("en") })
	uitest.SetScreen(1280, 800, 1)
	m := NewMainForm()
	m.Form.SetSize(1100, 640)
	w := uitest.Show(t, m.Form)
	return m, w
}

// The window has the top bar, the board card and the four tabs, each with
// its hint
func TestMainFormTabs(t *testing.T) {
	m, w := newTestForm(t)
	shot(t, w, "01-main")
	w.Find("No board connected")
	w.Find("Connect")
	w.Find("Tools ▾")
	w.Find(T().NoProjects)
	if n := m.tabs.PageCount(); n != 4 {
		t.Fatal("tabs:", n)
	}
	hints := []string{T().HintFlash, T().HintMemory, T().HintPartitions, T().HintMonitor}
	for i, hint := range hints {
		w.Shortcut("Ctrl+" + string(rune('1'+i)))
		if got := m.tabs.CurrentIndex(); got != i {
			t.Fatalf("Ctrl+%d: tab %d", i+1, got)
		}
		w.Find(hint)
	}
}

// The texts follow the language
func TestMainFormRussian(t *testing.T) {
	_, w := newTestForm(t)
	SetLanguage("ru")
	w.Settle()
	w.Find("Плата не подключена")
	w.Find("Подключить")
	shot(t, w, "02-russian")
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
	config.SetDir(t.TempDir())
	config.Load()
	config.SetProject("/home/u/sensor-node/build", config.Project{})
	uitest.SetScreen(1280, 800, 1)
	m := NewMainForm()
	w := uitest.Show(t, m.Form)
	w.Find("sensor-node/build")
	if got := shortFolder("/build"); got != "build" {
		t.Error(got)
	}
}
