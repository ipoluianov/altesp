package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/ipoluianov/altesp/internal/ops"
)

// tool is a tool of the server: its description for the agent and the
// function that does it
type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	run         func(args json.RawMessage) (any, error)
}

// Limits of one call, so it can't hang the server
const (
	callTimeout   = 10 * time.Minute
	maxMonitorSec = 120
)

// confirmWord must be given to the tools that erase
const confirmWord = "ERASE"

func object(props map[string]any, required ...string) map[string]any {
	o := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		o["required"] = required
	}
	return o
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func boolean(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}
func integer(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

var (
	portProp    = str(`The port: "/dev/ttyUSB0", "COM5". Omit it when exactly one board is connected`)
	noResetProp = boolean("Leave the board in download mode afterwards instead of resetting it into its application")
	jtagProp    = boolean("Work over the chip's built-in USB-JTAG (ESP32-C3, C6, H2) instead of the serial loader; port is then the USB serial number")
	numberProp  = func(what string) map[string]any {
		return str(what + `: "0x10000", "65536", "4MB", "64K"`)
	}
	confirmProp = str(`Must be "` + confirmWord + `": the data is lost`)
)

var tools []*tool

func init() {
	tools = []*tool{
		{
			Name:        "list_ports",
			Description: "Lists the serial ports; likely_board marks USB-UART bridges and ESP chips' own USB.",
			InputSchema: object(map[string]any{}),
			run:         listPorts,
		},
		{
			Name: "board_info",
			Description: "Resets the board into download mode and tells what it is: chip, revision, MAC, flash size, " +
				"crystal, security state, the partition table, the applications in it and which one boots.",
			InputSchema: object(map[string]any{"port": portProp, "no_reset": noResetProp}),
			run:         boardInfo,
		},
		{
			Name: "flash",
			Description: "Writes firmware and verifies it by MD5. Give build_dir (an ESP-IDF build folder, a PlatformIO or " +
				"Arduino project) or files with addresses. A file without an address goes where its content says " +
				"(an application to its partition, a merged image to 0). The files are checked first: built for " +
				"another chip, overlapping, past the end of the flash — then nothing is written unless force is set.",
			InputSchema: object(map[string]any{
				"port":      portProp,
				"build_dir": str("A build or project folder"),
				"files": map[string]any{"type": "array", "description": "Files to write",
					"items": object(map[string]any{"path": str("The file"), "address": numberProp("Where it goes; optional")}, "path")},
				"only_changed": boolean("Skip the files already on the board"),
				"force":        boolean("Write even when the check finds the files don't suit the board"),
				"no_reset":     noResetProp,
			}),
			run: flash,
		},
		{
			Name:        "read_flash",
			Description: "Reads a region of the flash into a file (a backup when address 0 and the flash size).",
			InputSchema: object(map[string]any{"port": portProp, "address": numberProp("Start"), "size": numberProp("Size"),
				"path": str("The file to write"), "no_reset": noResetProp}, "address", "size", "path"),
			run: readFlash,
		},
		{
			Name:        "erase_flash",
			Description: "Erases the whole flash, or a region of whole 4 KB sectors when address and size are given.",
			InputSchema: object(map[string]any{"port": portProp, "address": numberProp("Start"), "size": numberProp("Size"),
				"confirm": confirmProp, "no_reset": noResetProp}, "confirm"),
			run: eraseFlash,
		},
		{
			Name:        "read_partition",
			Description: "Saves a partition of the board's table into a file.",
			InputSchema: object(map[string]any{"port": portProp, "name": str("The partition name"), "path": str("The file to write"),
				"no_reset": noResetProp}, "name", "path"),
			run: readPartition,
		},
		{
			Name:        "write_partition",
			Description: "Writes a file at the start of a partition and verifies it; the rest of the partition is erased.",
			InputSchema: object(map[string]any{"port": portProp, "name": str("The partition name"), "path": str("The file"),
				"confirm": confirmProp, "no_reset": noResetProp}, "name", "path", "confirm"),
			run: writePartition,
		},
		{
			Name:        "erase_partition",
			Description: "Erases a whole partition.",
			InputSchema: object(map[string]any{"port": portProp, "name": str("The partition name"),
				"confirm": confirmProp, "no_reset": noResetProp}, "name", "confirm"),
			run: erasePartition,
		},
		{
			Name:        "set_ota_slot",
			Description: "Makes the bootloader start OTA slot n (0 for ota_0) from the next boot, by writing the otadata.",
			InputSchema: object(map[string]any{"port": portProp, "slot": integer("The OTA slot"), "no_reset": noResetProp}, "slot"),
			run:         setOTASlot,
		},
		{
			Name: "monitor",
			Description: "Captures what the board prints for some seconds (ESP-IDF colour codes removed). With reset the " +
				"board is restarted first, so the capture starts with its boot log. send is a line written to the board.",
			InputSchema: object(map[string]any{"port": portProp,
				"seconds": integer(fmt.Sprintf("How long to listen, up to %d; default 5", maxMonitorSec)),
				"baud":    integer("Baud rate; default 115200"), "reset": boolean("Restart the board first"),
				"send": str("A line to send, CR LF added")}),
			run: monitor,
		},
		{
			Name:        "reset_board",
			Description: `Resets the board by the control lines: mode "app" (default) or "bootloader" (download mode).`,
			InputSchema: object(map[string]any{"port": portProp, "mode": map[string]any{"type": "string", "enum": []string{"app", "bootloader"}}}),
			run:         resetBoard,
		},
	}
}

// sessionTools connect to the board and can do it over JTAG
var sessionTools = []string{"board_info", "flash", "read_flash", "erase_flash",
	"read_partition", "write_partition", "erase_partition", "set_ota_slot"}

func init() {
	for _, name := range sessionTools {
		t, _ := toolByName(name)
		t.InputSchema["properties"].(map[string]any)["jtag"] = jtagProp
	}
}

func toolList() []*tool { return tools }

func toolByName(name string) (*tool, bool) {
	for _, t := range tools {
		if t.Name == name {
			return t, true
		}
	}
	return nil, false
}

// decode reads the arguments of a call
func decode(args json.RawMessage, v any) error {
	if err := json.Unmarshal(args, v); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}

// withSession connects to the board (over JTAG when the arguments say
// so), runs f and resets the board unless noReset
func withSession(args json.RawMessage, port string, noReset bool, f func(ctx context.Context, s *ops.Session) (any, error)) (any, error) {
	var j struct {
		JTAG bool `json:"jtag"`
	}
	json.Unmarshal(args, &j)
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	var s *ops.Session
	if j.JTAG {
		d, err := ops.PickJTAG(port)
		if err != nil {
			return nil, err
		}
		if s, err = ops.ConnectJTAG(ctx, d, "", nil); err != nil {
			return nil, err
		}
	} else {
		info, err := ops.PickPort(port)
		if err != nil {
			return nil, err
		}
		if s, err = ops.Connect(ctx, info, ops.Options{}); err != nil {
			return nil, err
		}
	}
	out, err := f(ctx, s)
	if cerr := s.Close(context.Background(), !noReset); err == nil && cerr != nil && !noReset {
		err = fmt.Errorf("reset after the work: %w", cerr)
	}
	return out, err
}

func needConfirm(word string) error {
	if word != confirmWord {
		return fmt.Errorf(`this erases data on the board: pass confirm="%s"`, confirmWord)
	}
	return nil
}

func listPorts(json.RawMessage) (any, error) {
	ports, err := ops.Ports()
	if err != nil {
		return nil, err
	}
	type line struct {
		Port    string `json:"port"`
		Adapter string `json:"adapter,omitempty"`
		USBID   string `json:"usb_id,omitempty"`
		Serial  string `json:"serial,omitempty"`
		Board   bool   `json:"likely_board"`
	}
	out := []line{}
	for _, p := range ports {
		out = append(out, line{p.Name, p.Chip(), p.IDs(), p.SerialNumber, ops.LikelyBoard(p)})
	}
	return map[string]any{"ports": out, "notes": ops.LinuxObstacles()}, nil
}

func boardInfo(args json.RawMessage) (any, error) {
	var a struct {
		Port    string `json:"port"`
		NoReset bool   `json:"no_reset"`
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	return withSession(args, a.Port, a.NoReset, func(ctx context.Context, s *ops.Session) (any, error) {
		return s.Board().Summary(), nil
	})
}

func flash(args json.RawMessage) (any, error) {
	var a struct {
		Port     string `json:"port"`
		BuildDir string `json:"build_dir"`
		Files    []struct {
			Path    string `json:"path"`
			Address string `json:"address"`
		} `json:"files"`
		OnlyChanged bool `json:"only_changed"`
		Force       bool `json:"force"`
		NoReset     bool `json:"no_reset"`
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	if (a.BuildDir == "") == (len(a.Files) == 0) {
		return nil, errors.New("give build_dir or files")
	}
	return withSession(args, a.Port, a.NoReset, func(ctx context.Context, s *ops.Session) (any, error) {
		b := s.Board()
		var files []ops.File
		if a.BuildDir != "" {
			p, err := ops.LoadProject(a.BuildDir)
			if err != nil {
				return nil, err
			}
			files = p.Files
		}
		for _, f := range a.Files {
			file, ok, err := ops.FileFor(f.Path, b)
			if err != nil {
				return nil, err
			}
			if f.Address != "" {
				if file.Addr, err = ops.ParseNumber(f.Address); err != nil {
					return nil, err
				}
			} else if !ok {
				return nil, fmt.Errorf("%s: can't tell where it goes, give its address", f.Path)
			}
			files = append(files, file)
		}
		items, err := ops.ItemsOf(files)
		if err != nil {
			return nil, err
		}
		var problems []string
		fatal := false
		for _, p := range ops.Check(b, items) {
			problems = append(problems, p.String())
			fatal = fatal || p.Severity == ops.Fatal
		}
		if fatal && !a.Force {
			return nil, fmt.Errorf("not flashed: %s (force writes anyway)", strings.Join(problems, "; "))
		}
		skipped := map[int]bool{}
		opt := ops.FlashOptions{Verify: true, OnlyChanged: a.OnlyChanged, Skipped: func(i int) { skipped[i] = true }}
		if err := s.Flash(ctx, items, opt, nil); err != nil {
			return nil, err
		}
		written, same := []string{}, []string{}
		for i, it := range items {
			line := fmt.Sprintf("%s at 0x%X (%s)", it.Name, it.Addr, ops.SizeText(len(it.Data)))
			if skipped[i] {
				same = append(same, line)
			} else {
				written = append(written, line)
			}
		}
		return map[string]any{"flashed": written, "unchanged": same, "warnings": problems}, nil
	})
}

// addrSize reads an address and a size
func addrSize(addr, size string) (uint32, uint32, error) {
	a, err := ops.ParseNumber(addr)
	if err != nil {
		return 0, 0, err
	}
	n, err := ops.ParseNumber(size)
	return a, n, err
}

func readFlash(args json.RawMessage) (any, error) {
	var a struct {
		Port, Address, Size, Path string
		NoReset                   bool `json:"no_reset"`
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	addr, size, err := addrSize(a.Address, a.Size)
	if err != nil {
		return nil, err
	}
	return withSession(args, a.Port, a.NoReset, func(ctx context.Context, s *ops.Session) (any, error) {
		data, err := s.Read(ctx, addr, int(size), nil)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(a.Path, data, 0o644); err != nil {
			return nil, err
		}
		return fmt.Sprintf("Read %s from 0x%X into %s", ops.SizeText(len(data)), addr, a.Path), nil
	})
}

func eraseFlash(args json.RawMessage) (any, error) {
	var a struct {
		Port, Address, Size, Confirm string
		NoReset                      bool `json:"no_reset"`
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	if err := needConfirm(a.Confirm); err != nil {
		return nil, err
	}
	region := a.Address != "" || a.Size != ""
	var addr, size uint32
	if region {
		var err error
		if addr, size, err = addrSize(a.Address, a.Size); err != nil {
			return nil, err
		}
	}
	return withSession(args, a.Port, a.NoReset, func(ctx context.Context, s *ops.Session) (any, error) {
		if region {
			return fmt.Sprintf("Erased %s at 0x%X", ops.SizeText(int(size)), addr), s.Erase(ctx, addr, int(size))
		}
		return "Erased the whole flash", s.EraseAll(ctx)
	})
}

func readPartition(args json.RawMessage) (any, error) {
	var a struct {
		Port, Name, Path string
		NoReset          bool `json:"no_reset"`
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	return withSession(args, a.Port, a.NoReset, func(ctx context.Context, s *ops.Session) (any, error) {
		data, err := s.ReadPartition(ctx, a.Name, nil)
		if err != nil {
			return nil, err
		}
		return fmt.Sprintf("Saved %s (%s) into %s", a.Name, ops.SizeText(len(data)), a.Path), os.WriteFile(a.Path, data, 0o644)
	})
}

func writePartition(args json.RawMessage) (any, error) {
	var a struct {
		Port, Name, Path, Confirm string
		NoReset                   bool `json:"no_reset"`
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	if err := needConfirm(a.Confirm); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(a.Path)
	if err != nil {
		return nil, err
	}
	return withSession(args, a.Port, a.NoReset, func(ctx context.Context, s *ops.Session) (any, error) {
		return fmt.Sprintf("Wrote %s into %s", a.Path, a.Name), s.WritePartition(ctx, a.Name, data, true, nil)
	})
}

func erasePartition(args json.RawMessage) (any, error) {
	var a struct {
		Port, Name, Confirm string
		NoReset             bool `json:"no_reset"`
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	if err := needConfirm(a.Confirm); err != nil {
		return nil, err
	}
	return withSession(args, a.Port, a.NoReset, func(ctx context.Context, s *ops.Session) (any, error) {
		return "Erased " + a.Name, s.ErasePartition(ctx, a.Name)
	})
}

func setOTASlot(args json.RawMessage) (any, error) {
	var a struct {
		Port    string `json:"port"`
		Slot    int    `json:"slot"`
		NoReset bool   `json:"no_reset"`
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	return withSession(args, a.Port, a.NoReset, func(ctx context.Context, s *ops.Session) (any, error) {
		return fmt.Sprintf("OTA slot %d boots next", a.Slot), s.SetActiveOTA(ctx, a.Slot)
	})
}

// ansi are the colour codes of the ESP-IDF log
var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func monitor(args json.RawMessage) (any, error) {
	var a struct {
		Port    string `json:"port"`
		Seconds int    `json:"seconds"`
		Baud    int    `json:"baud"`
		Reset   bool   `json:"reset"`
		Send    string `json:"send"`
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	if a.Seconds <= 0 {
		a.Seconds = 5
	}
	a.Seconds = min(a.Seconds, maxMonitorSec)
	info, err := ops.PickPort(a.Port)
	if err != nil {
		return nil, err
	}
	m, err := ops.OpenMonitor(info, a.Baud)
	if err != nil {
		return nil, err
	}
	defer m.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(a.Seconds)*time.Second)
	defer cancel()
	if a.Reset {
		if err := m.Reset(ctx); err != nil {
			return nil, err
		}
	}
	if a.Send != "" {
		m.Write([]byte(a.Send + "\r\n"))
	}
	done := make(chan []byte)
	go func() {
		var out []byte
		buf := make([]byte, 4096)
		for {
			n, err := m.Read(buf)
			out = append(out, buf[:n]...)
			if err != nil || len(out) > 1<<20 {
				done <- out
				return
			}
		}
	}()
	<-ctx.Done()
	m.Close()
	out := <-done
	text := strings.ReplaceAll(ansi.ReplaceAllString(string(out), ""), "\r", "")
	return text, nil
}

func resetBoard(args json.RawMessage) (any, error) {
	var a struct {
		Port string `json:"port"`
		Mode string `json:"mode"`
	}
	if err := decode(args, &a); err != nil {
		return nil, err
	}
	info, err := ops.PickPort(a.Port)
	if err != nil {
		return nil, err
	}
	m, err := ops.OpenMonitor(info, 0)
	if err != nil {
		return nil, err
	}
	defer m.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if a.Mode == "bootloader" {
		return "The board is in download mode", m.Bootloader(ctx)
	}
	return "The board was reset into its application", m.Reset(ctx)
}
