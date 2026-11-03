// Package config keeps the settings of the program and the memory of each
// project: the files last flashed from its folder and its .elf.
// Several copies of the program may run at once, so a save reads the file
// again and changes only what this copy changed.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Default baud rates
const (
	// DefaultMonitorBaud is the baud rate of the monitor: what ESP-IDF and
	// Arduino print their logs at
	DefaultMonitorBaud = 115200
	// DefaultFlashBaud is the baud rate the loader is switched to for flashing
	DefaultFlashBaud = 921600
)

// FlashFile is a file of the flash list
type FlashFile struct {
	Path    string `json:"path"`
	Address uint32 `json:"address"`
	Enabled bool   `json:"enabled"`
}

// Project is what is remembered for a project folder
type Project struct {
	Files []FlashFile `json:"files,omitempty"`
	ELF   string      `json:"elf,omitempty"`
	// Used is when the project was used last (Unix seconds): the order of
	// the recent projects
	Used int64 `json:"used"`
}

// Settings of the program
type Settings struct {
	Theme    string `json:"theme"`     // "", "light"
	Language string `json:"language"`  // "" is the system's
	LastPort string `json:"last_port"` // the key of the device
	// MonitorBaud is the default baud rate of the port
	MonitorBaud int `json:"monitor_baud"`
	// FlashBaud is the baud rate of flashing and reading
	FlashBaud int `json:"flash_baud"`
	// MonitorAfterFlash switches to the monitor after a board is flashed
	// and reset
	MonitorAfterFlash bool `json:"monitor_after_flash"`
	// ExportDir is the folder the firmware was saved to last
	ExportDir string `json:"export_dir,omitempty"`
	// MonitorWidth and MonitorHeight are the size of the monitor window
	// last closed; 0 is the default
	MonitorWidth  int `json:"monitor_width,omitempty"`
	MonitorHeight int `json:"monitor_height,omitempty"`
	// LineEnding is what the send line of the monitor adds: "", "LF",
	// "CR", "CRLF"
	LineEnding string `json:"line_ending"`
	// LastELF is the .elf set by hand last, for a monitor without a
	// project
	LastELF string `json:"last_elf,omitempty"`
	// Resets are the reset sequences chosen by hand, by the device key:
	// "bridge", "usbjtag", "manual"; a device without one is "auto"
	Resets map[string]string `json:"resets,omitempty"`
	// Projects are keyed by the project folder
	Projects map[string]*Project `json:"projects"`
}

// MaxProjects is the most projects remembered: the ones used longest ago
// are forgotten first
const MaxProjects = 50

var (
	mu       sync.Mutex
	settings = defaults()
	dir      string
	// changed are the projects changed by this copy since the last save
	changed       = map[string]bool{}
	globalChanged bool
)

func defaults() Settings {
	return Settings{Projects: map[string]*Project{},
		MonitorBaud: DefaultMonitorBaud, FlashBaud: DefaultFlashBaud,
		MonitorAfterFlash: true, LineEnding: "LF"}
}

// Dir is the directory of the settings: ~/.altbins/.altesp
func Dir() string {
	mu.Lock()
	defer mu.Unlock()
	return directory()
}

func directory() string {
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		dir = filepath.Join(home, ".altbins", ".altesp")
	}
	return dir
}

// SetDir keeps the settings in another directory (tests)
func SetDir(d string) {
	mu.Lock()
	dir = d
	mu.Unlock()
}

func path() string { return filepath.Join(directory(), "settings.json") }

// Load reads the settings; a missing or broken file gives the defaults
func Load() {
	mu.Lock()
	defer mu.Unlock()
	settings = read()
}

func read() Settings {
	s := defaults()
	data, err := os.ReadFile(path())
	if err == nil {
		json.Unmarshal(data, &s)
	}
	if s.Projects == nil {
		s.Projects = map[string]*Project{}
	}
	if s.MonitorBaud <= 0 {
		s.MonitorBaud = DefaultMonitorBaud
	}
	if s.FlashBaud <= 0 {
		s.FlashBaud = DefaultFlashBaud
	}
	return s
}

// Get returns a copy of the settings; the projects are shared, read them
// with ProjectFor
func Get() Settings {
	mu.Lock()
	defer mu.Unlock()
	s := settings
	return s
}

// Update changes the settings of the program (not the projects)
func Update(f func(s *Settings)) {
	mu.Lock()
	f(&settings)
	globalChanged = true
	mu.Unlock()
	Save()
}

// ProjectFor returns what is remembered for the project folder, and
// whether anything is
func ProjectFor(folder string) (Project, bool) {
	mu.Lock()
	defer mu.Unlock()
	p, ok := settings.Projects[folder]
	if !ok {
		return Project{}, false
	}
	cp := *p
	cp.Files = append([]FlashFile(nil), p.Files...)
	return cp, true
}

// SetProject remembers the project folder and makes it the most recent one
func SetProject(folder string, p Project) {
	mu.Lock()
	cp := p
	cp.Files = append([]FlashFile(nil), p.Files...)
	cp.Used = time.Now().Unix()
	settings.Projects[folder] = &cp
	changed[folder] = true
	mu.Unlock()
}

// RecentProjects returns the project folders, the last used first, at
// most n of them
func RecentProjects(n int) []string {
	mu.Lock()
	defer mu.Unlock()
	return recent(settings.Projects, n)
}

func recent(projects map[string]*Project, n int) []string {
	folders := make([]string, 0, len(projects))
	for f := range projects {
		folders = append(folders, f)
	}
	sort.Slice(folders, func(i, j int) bool {
		a, b := projects[folders[i]].Used, projects[folders[j]].Used
		if a != b {
			return a > b
		}
		return folders[i] < folders[j]
	})
	if n >= 0 && len(folders) > n {
		folders = folders[:n]
	}
	return folders
}

// Save writes the settings: the file is read again, and what this copy
// changed is put into it, so other copies keep their projects
func Save() error {
	mu.Lock()
	defer mu.Unlock()
	if !globalChanged && len(changed) == 0 {
		return nil
	}
	disk := read()
	for key := range changed {
		if p, ok := settings.Projects[key]; ok {
			disk.Projects[key] = p
		}
	}
	if globalChanged {
		projects := disk.Projects
		disk = settings
		disk.Projects = projects
	}
	// The projects used longest ago are forgotten
	if len(disk.Projects) > MaxProjects {
		keep := map[string]*Project{}
		for _, f := range recent(disk.Projects, MaxProjects) {
			keep[f] = disk.Projects[f]
		}
		disk.Projects = keep
	}
	// The projects changed by the other copies are seen from now on
	settings.Projects = map[string]*Project{}
	for k, p := range disk.Projects {
		settings.Projects[k] = p
	}
	data, err := json.MarshalIndent(disk, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(directory(), 0o755); err != nil {
		return err
	}
	// A file of its own: another copy may be saving at the same moment
	f, err := os.CreateTemp(directory(), "settings-*.tmp")
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), path())
	}
	if err != nil {
		os.Remove(f.Name())
		return err
	}
	changed = map[string]bool{}
	globalChanged = false
	return nil
}
