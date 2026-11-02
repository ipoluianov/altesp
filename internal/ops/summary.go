package ops

import (
	"fmt"
	"sort"
	"strings"
)

// Summary is the board card in words and plain values: what the window
// shows and the command line prints (also as JSON)
type Summary struct {
	Port       string          `json:"port"`
	Adapter    string          `json:"adapter,omitempty"`
	Chip       string          `json:"chip"`
	Revision   string          `json:"revision,omitempty"`
	MAC        string          `json:"mac,omitempty"`
	Flash      string          `json:"flash"`
	FlashID    string          `json:"flash_id,omitempty"`
	PSRAM      string          `json:"psram,omitempty"`
	Crystal    string          `json:"crystal,omitempty"`
	Features   []string        `json:"features,omitempty"`
	Security   []string        `json:"security"`
	Loader     string          `json:"loader"`
	Baud       int             `json:"baud,omitempty"`
	Partitions []PartitionLine `json:"partitions,omitempty"`
	Apps       []AppLine       `json:"apps,omitempty"`
	Notes      []string        `json:"notes,omitempty"`
}

// PartitionLine is a row of the partition table
type PartitionLine struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	SubType string `json:"subtype"`
	Offset  uint32 `json:"offset"`
	Size    uint32 `json:"size"`
}

// AppLine is an application found on the board
type AppLine struct {
	Partition  string `json:"partition"`
	Project    string `json:"project"`
	Version    string `json:"version"`
	IDFVersion string `json:"idf_version"`
	Built      string `json:"built"`
}

// Summary puts the card into words
func (b Board) Summary() Summary {
	s := Summary{Port: b.Port.Short, Adapter: b.Port.Chip(), Baud: b.Baud}
	if b.Chip != nil {
		s.Chip = b.Chip.Name
	}
	if b.Info.Revision.Major != 0 || b.Info.Revision.Minor != 0 {
		s.Revision = b.Info.Revision.String()
	}
	if b.Info.HasMAC {
		m := b.Info.MAC
		s.MAC = fmt.Sprintf("%02X:%02X:%02X:%02X:%02X:%02X", m[0], m[1], m[2], m[3], m[4], m[5])
	}
	s.Flash = SizeText(b.FlashSize)
	if !b.FlashSizeKnown {
		s.Flash += " (assumed)"
	}
	if b.Info.FlashID != 0 {
		s.FlashID = fmt.Sprintf("%06X", b.Info.FlashID)
	}
	if b.Info.PSRAMSize > 0 {
		s.PSRAM = SizeText(b.Info.PSRAMSize)
	}
	if b.Info.CrystalMHz > 0 {
		s.Crystal = fmt.Sprintf("%d MHz", b.Info.CrystalMHz)
	}
	s.Features = b.Info.Features
	sec := b.Security
	onOff := func(name string, on bool) string {
		if on {
			return name + ": on"
		}
		return name + ": off"
	}
	s.Security = []string{onOff("Flash encryption", sec.FlashEncryption), onOff("Secure Boot", sec.SecureBoot)}
	if b.SecureDownload {
		s.Security = append(s.Security, "Secure Download Mode")
	}
	if sec.JTAGDisabled {
		s.Security = append(s.Security, "JTAG disabled")
	}
	s.Loader = "ROM"
	if b.Stub {
		s.Loader = "stub"
	}
	for _, p := range b.Partitions {
		s.Partitions = append(s.Partitions, PartitionLine{p.Name, p.TypeName(), p.SubTypeName(), p.Offset, p.Size})
	}
	names := make([]string, 0, len(b.Apps))
	for n := range b.Apps {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		img := b.Apps[n]
		if img.App == nil {
			// Built before ESP-IDF v3.3, or by another SDK
			s.Apps = append(s.Apps, AppLine{Partition: n, Project: "(no description)"})
			continue
		}
		a := img.App
		s.Apps = append(s.Apps, AppLine{n, a.ProjectName, a.Version, a.IDFVersion, strings.TrimSpace(a.Date + " " + a.Time)})
	}
	s.Notes = b.Notes
	return s
}

// SizeText writes a size the short way: "4 MB", "512 KB", "300 B"
func SizeText(n int) string {
	switch {
	case n >= 1<<20 && n%(1<<20) == 0:
		return fmt.Sprintf("%d MB", n>>20)
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", (n+1<<9)>>10)
	}
	return fmt.Sprintf("%d B", n)
}
