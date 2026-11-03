package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"time"

	"github.com/ipoluianov/altesp/internal/formats"
	"github.com/ipoluianov/altesp/internal/ops"
)

// opts are the options shared by the commands
type opts struct {
	port    string
	baud    int
	json    bool
	elf     string
	reset   bool
	noReset bool
	noStub  bool
	force   bool
	noVerif bool
	changed bool
	jtag    bool
	encrypt bool
}

// parse reads the options of a command; the rest are its arguments.
// Options may come after the arguments too.
func parse(name string, args []string, e env) (opts, []string, error) {
	var o opts
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.port, "port", "", "")
	fs.StringVar(&o.port, "p", "", "")
	fs.IntVar(&o.baud, "baud", 0, "")
	fs.IntVar(&o.baud, "b", 0, "")
	fs.BoolVar(&o.json, "json", false, "")
	fs.StringVar(&o.elf, "elf", "", "")
	fs.BoolVar(&o.reset, "reset", false, "")
	fs.BoolVar(&o.noReset, "no-reset", false, "")
	fs.BoolVar(&o.noStub, "no-stub", false, "")
	fs.BoolVar(&o.force, "force", false, "")
	fs.BoolVar(&o.noVerif, "no-verify", false, "")
	fs.BoolVar(&o.changed, "changed", false, "")
	fs.BoolVar(&o.jtag, "jtag", false, "")
	fs.BoolVar(&o.encrypt, "encrypt", false, "")
	var rest []string
	for {
		if err := fs.Parse(args); err != nil {
			return o, nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return o, rest, nil
		}
		rest = append(rest, args[0])
		args = args[1:]
	}
}

// interrupted is a context that ends on Ctrl+C
func interrupted() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}

// result writes v as JSON, or calls text
func result(e env, o opts, v any, text func()) {
	if o.json {
		enc := json.NewEncoder(e.stdout)
		enc.SetIndent("", "  ")
		enc.Encode(v)
		return
	}
	text()
}

func fail(e env, err error) int {
	fmt.Fprintf(e.stderr, "altesp: %v\n", err)
	return exitFail
}

func cmdPorts(args []string, e env) int {
	o, _, err := parse("ports", args, e)
	if err != nil {
		return usageError(e, err)
	}
	ports, err := ops.Ports()
	if err != nil {
		return fail(e, err)
	}
	type line struct {
		Port    string `json:"port"`
		Name    string `json:"name"`
		Adapter string `json:"adapter,omitempty"`
		USB     string `json:"usb_id,omitempty"`
		Serial  string `json:"serial,omitempty"`
		Board   bool   `json:"likely_board"`
	}
	var lines []line
	for _, p := range ports {
		lines = append(lines, line{p.Name, p.Short, p.Chip(), p.IDs(), p.SerialNumber, ops.LikelyBoard(p)})
	}
	result(e, o, lines, func() {
		if len(lines) == 0 {
			fmt.Fprintln(e.stdout, "No serial ports")
		}
		for _, l := range lines {
			mark := " "
			if l.Board {
				mark = "*"
			}
			fmt.Fprintf(e.stdout, "%s %-16s %-14s %s\n", mark, l.Name, l.Adapter, l.USB)
		}
		for _, o := range ops.LinuxObstacles() {
			fmt.Fprintln(e.stderr, "Note: "+o)
		}
	})
	return exitOK
}

// connect picks the port and connects, telling the steps on stderr
func connect(ctx context.Context, o opts, e env) (*ops.Session, error) {
	status := func(s string) {
		if !o.json {
			fmt.Fprintf(e.stderr, "%s...\n", s)
		}
	}
	if o.jtag {
		d, err := ops.PickJTAG(o.port)
		if err != nil {
			return nil, err
		}
		return ops.ConnectJTAG(ctx, d, "", status)
	}
	info, err := ops.PickPort(o.port)
	if err != nil {
		return nil, err
	}
	return ops.Connect(ctx, info, ops.Options{
		Baud: o.baud, NoStub: o.noStub, Status: status,
		Manual: func() {
			fmt.Fprintln(e.stderr, "Could not reset the board into download mode automatically.")
			fmt.Fprintln(e.stderr, "Hold BOOT, press and release RESET, then release BOOT. Waiting...")
		},
	})
}

func cmdInfo(args []string, e env) int {
	o, _, err := parse("info", args, e)
	if err != nil {
		return usageError(e, err)
	}
	ctx, stop := interrupted()
	defer stop()
	s, err := connect(ctx, o, e)
	if err != nil {
		return fail(e, err)
	}
	defer s.Close(context.Background(), !o.noReset)
	sum := s.Board().Summary()
	result(e, o, sum, func() { printSummary(e.stdout, sum) })
	return exitOK
}

func printSummary(w io.Writer, s ops.Summary) {
	row := func(name, value string) {
		if value != "" {
			fmt.Fprintf(w, "%-12s %s\n", name+":", value)
		}
	}
	row("Port", strings.TrimSpace(s.Port+" "+s.Adapter))
	row("Chip", strings.TrimSpace(s.Chip+" "+s.Revision))
	row("MAC", s.MAC)
	row("Flash", strings.TrimSpace(s.Flash+" "+s.FlashID))
	row("PSRAM", s.PSRAM)
	row("Crystal", s.Crystal)
	row("Features", strings.Join(s.Features, ", "))
	row("Security", strings.Join(s.Security, ", "))
	row("Loader", s.Loader)
	row("Firmware", s.Firmware)
	if len(s.Partitions) > 0 {
		fmt.Fprintln(w, "\nPartitions:")
		for _, p := range s.Partitions {
			mark := ""
			if p.Active {
				mark = "  (boots)"
			}
			fmt.Fprintf(w, "  %-16s %-5s %-10s 0x%06X  %s%s\n", p.Name, p.Type, p.SubType, p.Offset, ops.SizeText(int(p.Size)), mark)
		}
	}
	if len(s.Apps) > 0 {
		fmt.Fprintln(w, "\nApplications:")
		for _, a := range s.Apps {
			if a.IDFVersion == "" {
				fmt.Fprintf(w, "  %-16s %s\n", a.Partition, a.Project)
				continue
			}
			fmt.Fprintf(w, "  %-16s %s %s (ESP-IDF %s, %s)\n", a.Partition, a.Project, a.Version, a.IDFVersion, a.Built)
		}
	}
	for _, n := range s.Notes {
		fmt.Fprintln(w, "\nNote: "+n)
	}
}

// progressLine draws a progress bar on stderr
type progressLine struct {
	w     io.Writer
	names []string
	last  time.Time
}

func (p *progressLine) update(i, done, total int) {
	if time.Since(p.last) < 100*time.Millisecond && done < total {
		return
	}
	p.last = time.Now()
	pct := 100
	if total > 0 {
		pct = done * 100 / total
	}
	name := ""
	if i < len(p.names) {
		name = p.names[i]
	}
	fmt.Fprintf(p.w, "\r%-28s %3d%%", name, pct)
	if done >= total {
		fmt.Fprintln(p.w)
	}
}

func cmdFlash(args []string, e env) int {
	o, rest, err := parse("flash", args, e)
	if err != nil {
		return usageError(e, err)
	}
	if len(rest) == 0 {
		return usageError(e, errors.New("flash: give a build folder or an address and a file"))
	}
	// A single file without an address is placed by its kind once the
	// board is known
	var single []byte
	var items []ops.Item
	if len(rest) == 1 {
		if st, err := os.Stat(rest[0]); err == nil && !st.IsDir() {
			if single, err = os.ReadFile(rest[0]); err != nil {
				return fail(e, err)
			}
		}
	}
	if single == nil {
		items, _, err = ops.ItemsFromArgs(rest)
		if err != nil {
			return fail(e, err)
		}
	}
	ctx, stop := interrupted()
	defer stop()
	s, err := connect(ctx, o, e)
	if err != nil {
		return fail(e, err)
	}
	b := s.Board()
	if single != nil {
		addr, err := singleAddress(b, single)
		if err != nil {
			s.Close(context.Background(), false)
			return fail(e, err)
		}
		items = []ops.Item{{Name: rest[0], Addr: addr, Data: single}}
	}
	fatal := false
	for _, p := range ops.Check(b, items) {
		fmt.Fprintln(e.stderr, "Warning: "+p.String())
		fatal = fatal || p.Severity == ops.Fatal
	}
	if fatal && !o.force {
		s.Close(context.Background(), false)
		return fail(e, errors.New("not flashed; --force writes anyway"))
	}
	names := make([]string, len(items))
	for i, it := range items {
		names[i] = fmt.Sprintf("%s @0x%X", it.Name, it.Addr)
	}
	pl := &progressLine{w: e.stderr, names: names}
	var flashed, same []string
	skipped := map[int]bool{}
	err = s.Flash(ctx, items, ops.FlashOptions{Verify: !o.noVerif, OnlyChanged: o.changed, Encrypt: o.encrypt,
		Skipped: func(i int) { skipped[i] = true }}, pl.update)
	cerr := s.Close(context.Background(), err == nil && !o.noReset)
	if err != nil {
		return fail(e, err)
	}
	if cerr != nil {
		fmt.Fprintln(e.stderr, "Warning: reset after flashing: "+cerr.Error())
	}
	for i, n := range names {
		if skipped[i] {
			same = append(same, n)
		} else {
			flashed = append(flashed, n)
		}
	}
	result(e, o, map[string]any{"flashed": flashed, "unchanged": same}, func() {
		fmt.Fprintf(e.stdout, "Flashed %d file(s)", len(flashed))
		if len(same) > 0 {
			fmt.Fprintf(e.stdout, ", %d already on the board", len(same))
		}
		fmt.Fprintln(e.stdout)
	})
	return exitOK
}

// singleAddress places a lone file by what it is
func singleAddress(b ops.Board, data []byte) (uint32, error) {
	switch formats.Detect(data) {
	case formats.App:
		return ops.AppAddress(b), nil
	case formats.Merged:
		return 0, nil
	case formats.Bootloader:
		return b.Chip.BootloaderAddr, nil
	case formats.PartitionTable:
		if b.PartitionTableAddr != 0 {
			return b.PartitionTableAddr, nil
		}
		return 0x8000, nil
	}
	return 0, errors.New("can't tell where this file goes: give its address")
}

func cmdRead(args []string, e env) int {
	o, rest, err := parse("read", args, e)
	if err != nil {
		return usageError(e, err)
	}
	if len(rest) != 3 {
		return usageError(e, errors.New("read: give an address, a size and a file"))
	}
	addr, err := ops.ParseNumber(rest[0])
	if err != nil {
		return fail(e, err)
	}
	size, err := ops.ParseNumber(rest[1])
	if err != nil {
		return fail(e, err)
	}
	ctx, stop := interrupted()
	defer stop()
	s, err := connect(ctx, o, e)
	if err != nil {
		return fail(e, err)
	}
	defer s.Close(context.Background(), !o.noReset)
	pl := &progressLine{w: e.stderr, names: []string{rest[2]}}
	data, err := s.Read(ctx, addr, int(size), func(done, total int) { pl.update(0, done, total) })
	if err != nil {
		return fail(e, err)
	}
	if err := os.WriteFile(rest[2], data, 0o644); err != nil {
		return fail(e, err)
	}
	result(e, o, map[string]any{"file": rest[2], "address": addr, "size": len(data)}, func() {
		fmt.Fprintf(e.stdout, "Read %s from 0x%X into %s\n", ops.SizeText(len(data)), addr, rest[2])
	})
	return exitOK
}

func cmdErase(args []string, e env) int {
	o, rest, err := parse("erase", args, e)
	if err != nil {
		return usageError(e, err)
	}
	var addr, size uint32
	region := len(rest) == 2
	switch {
	case region:
		if addr, err = ops.ParseNumber(rest[0]); err != nil {
			return fail(e, err)
		}
		if size, err = ops.ParseNumber(rest[1]); err != nil {
			return fail(e, err)
		}
	case len(rest) != 0:
		return usageError(e, errors.New("erase: nothing for the whole flash, or an address and a size"))
	}
	ctx, stop := interrupted()
	defer stop()
	s, err := connect(ctx, o, e)
	if err != nil {
		return fail(e, err)
	}
	what := "the whole flash"
	if region {
		what = fmt.Sprintf("%s at 0x%X", ops.SizeText(int(size)), addr)
	}
	if !o.json {
		fmt.Fprintf(e.stderr, "Erasing %s...\n", what)
	}
	if region {
		err = s.Erase(ctx, addr, int(size))
	} else {
		err = s.EraseAll(ctx)
	}
	s.Close(context.Background(), err == nil && !o.noReset)
	if err != nil {
		return fail(e, err)
	}
	result(e, o, map[string]any{"erased": what}, func() { fmt.Fprintf(e.stdout, "Erased %s\n", what) })
	return exitOK
}

func cmdMonitor(args []string, e env) int {
	o, _, err := parse("monitor", args, e)
	if err != nil {
		return usageError(e, err)
	}
	info, err := ops.PickPort(o.port)
	if err != nil {
		return fail(e, err)
	}
	m, err := ops.OpenMonitor(info, o.baud)
	if err != nil {
		return fail(e, err)
	}
	defer m.Close()
	ctx, stop := interrupted()
	defer stop()
	if o.reset {
		if err := m.Reset(ctx); err != nil {
			return fail(e, err)
		}
	}
	var dec *ops.PanicDecoder
	if o.elf != "" {
		if dec, err = ops.NewPanicDecoder(o.elf); err != nil {
			return fail(e, err)
		}
	}
	fmt.Fprintf(e.stderr, "Monitor on %s, Ctrl+C to quit\n", info.Short)
	go func() {
		// The lines typed go to the board
		sc := bufio.NewScanner(e.stdin)
		for sc.Scan() {
			m.Write([]byte(sc.Text() + "\r\n"))
		}
	}()
	go func() {
		<-ctx.Done()
		m.Close()
	}()
	buf := make([]byte, 4096)
	var line []byte
	for {
		n, err := m.Read(buf)
		if n > 0 {
			e.stdout.Write(buf[:n])
			if dec != nil {
				// Whole lines go to the decoder; its frames follow the line
				line = append(line, buf[:n]...)
				for {
					i := bytes.IndexByte(line, '\n')
					if i < 0 {
						break
					}
					text := ansi.ReplaceAllString(strings.TrimRight(string(line[:i]), "\r"), "")
					line = line[i+1:]
					for _, x := range dec.Line(text) {
						fmt.Fprintln(e.stdout, "    "+x)
					}
				}
			}
		}
		if err != nil {
			if dec != nil {
				for _, x := range dec.Flush() {
					fmt.Fprintln(e.stdout, "    "+x)
				}
			}
			if ctx.Err() != nil {
				return exitOK
			}
			return fail(e, err)
		}
	}
}

// ansi are the colour codes of the ESP-IDF log
var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func cmdCoredump(args []string, e env) int {
	o, rest, err := parse("coredump", args, e)
	if err != nil {
		return usageError(e, err)
	}
	var rep *ops.CoredumpReport
	switch len(rest) {
	case 1:
		rep, err = ops.ParseCoredumpFile(rest[0], o.elf)
	case 0:
		ctx, stop := interrupted()
		defer stop()
		s, cerr := connect(ctx, o, e)
		if cerr != nil {
			return fail(e, cerr)
		}
		rep, err = s.ReadCoredump(ctx, o.elf)
		s.Close(context.Background(), !o.noReset)
	default:
		return usageError(e, errors.New("coredump: a file, or nothing to read the board's partition"))
	}
	if err != nil {
		return fail(e, err)
	}
	result(e, o, rep, func() {
		fmt.Fprintf(e.stdout, "Coredump %s v%s (%s)\n", rep.Format, rep.Version, rep.Checksum)
		if rep.ELFMismatch {
			fmt.Fprintln(e.stdout, "Warning: the .elf is not the one the application was built from")
		}
		if rep.Exception != "" {
			fmt.Fprintln(e.stdout, "Exception: "+rep.Exception)
		}
		for _, t := range rep.Tasks {
			mark := ""
			if t.Crashed {
				mark = " (crashed)"
			}
			fmt.Fprintf(e.stdout, "\nTask %s%s, %s\n", t.Name, mark, t.State)
			for _, f := range t.Frames {
				fmt.Fprintln(e.stdout, "  "+f)
			}
		}
	})
	return exitOK
}
