package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestTwoCopiesKeepTheirProjects(t *testing.T) {
	dir := t.TempDir()
	SetDir(dir)
	Load()
	p := Project{Files: []FlashFile{{Path: "/a/build/app.bin", Address: 0x10000, Enabled: true}}}
	SetProject("/a", p)
	if err := Save(); err != nil {
		t.Fatal(err)
	}

	// Another copy writes its project to the file meanwhile
	var disk Settings
	data, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	json.Unmarshal(data, &disk)
	disk.Projects["/b"] = &Project{ELF: "/b/build/app.elf", Used: 1}
	data, _ = json.Marshal(disk)
	os.WriteFile(filepath.Join(dir, "settings.json"), data, 0o644)

	p.ELF = "/a/build/app.elf"
	SetProject("/a", p)
	Update(func(s *Settings) { s.Theme = "light" })
	Load()
	if got, _ := ProjectFor("/a"); got.ELF != "/a/build/app.elf" || len(got.Files) != 1 || got.Files[0].Address != 0x10000 {
		t.Fatalf("own project %+v", got)
	}
	if got, ok := ProjectFor("/b"); !ok || got.ELF != "/b/build/app.elf" {
		t.Fatalf("project of the other copy %+v", got)
	}
	if Get().Theme != "light" {
		t.Fatal("theme", Get().Theme)
	}
	if r := RecentProjects(5); len(r) != 2 || r[0] != "/a" {
		t.Fatal("recent", r)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatal("left files:", entries)
	}
}

func TestDefaultsAndLimit(t *testing.T) {
	SetDir(t.TempDir())
	Load()
	if s := Get(); s.MonitorBaud != DefaultMonitorBaud || s.FlashBaud != DefaultFlashBaud {
		t.Fatalf("%+v", s)
	}
	for i := 0; i < MaxProjects+5; i++ {
		SetProject(fmt.Sprint("/p", i), Project{})
	}
	if err := Save(); err != nil {
		t.Fatal(err)
	}
	Load()
	if n := len(RecentProjects(-1)); n != MaxProjects {
		t.Fatal("projects kept:", n)
	}
}
