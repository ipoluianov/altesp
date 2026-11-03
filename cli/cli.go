// Package cli is the command line of the program: the same actions as the
// window, for scripts and CI. The work is done by the ops package, as in
// the window.
package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/ipoluianov/altesp/app"
)

const usage = `Usage:
  altesp                                   the window
  altesp ports                             the serial ports and the boards on them
  altesp info    [--port P]                the chip, its flash and what is flashed
  altesp flash   [--port P] <build folder>
  altesp flash   [--port P] <address> <file> [<address> <file>...]
  altesp read    [--port P] <address> <size> <file>
  altesp read    [--port P] --parts <folder>   the parts into a new folder: a build
                                           folder to flash back (--no-data: without
                                           NVS, file systems and the other data)
  altesp read    [--port P] --trimmed <file>   the flash without the erased tail
  altesp read    [--port P] --app <file>       the application that boots, alone
  altesp erase   [--port P] [<address> <size>]  the whole flash, or a region
  altesp monitor [--port P] [--elf FILE] [--reset]   panics decoded with the .elf
  altesp coredump [--port P] [--elf FILE] [<file>]  the board's coredump, or a file
  altesp efuse   [--port P] [--all]        the eFuses of the chip
  altesp efuse burn [--port P] FIELD=VALUE... [--confirm BURN]
                                           shows the plan; burns only with --confirm BURN
  altesp mcp                               an MCP server on stdin/stdout for AI agents
  altesp api     [--listen 127.0.0.1:8765] the same tools as a local HTTP API
  altesp version

The port can be omitted when exactly one board is connected.

Options:
  -p, --port P          the port: COM5, /dev/ttyUSB0
  -b, --baud N          the baud rate
      --elf FILE        decode the panics with the .elf (monitor)
      --reset           reset the board when the monitor starts
      --json            machine-readable output
      --no-reset        leave the board in download mode afterwards
      --no-stub         use only the ROM loader (slower, fewer functions)
      --no-verify       don't compare the flash with the files after writing
      --changed         write only the files that differ from the flash
      --encrypt         have the chip encrypt what is written (Flash Encryption,
                        development mode)
      --jtag            work over the chip's built-in USB-JTAG (ESP32-C3, C6, H2)
                        instead of the serial loader; --port is then the USB serial number
      --force           flash even when the files don't suit the board

Exit codes: 0 done, 1 failed, 2 wrong command line.
`

// Exit codes
const (
	exitOK    = 0
	exitFail  = 1
	exitUsage = 2
)

// commands are the commands of the console; the window has the others
var commands = []string{"ports", "info", "flash", "read", "erase", "monitor", "coredump", "efuse", "mcp", "api"}

// handlers run the commands
var handlers = map[string]func(args []string, e env) int{
	"ports": cmdPorts, "info": cmdInfo, "flash": cmdFlash,
	"read": cmdRead, "erase": cmdErase, "monitor": cmdMonitor,
	"coredump": cmdCoredump, "efuse": cmdEFuse, "mcp": cmdMCP, "api": cmdAPI,
}

// IsCommand tells whether the arguments ask for the command line, not the
// window
func IsCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "help", "-h", "--help", "version", "--version":
		return true
	}
	for _, c := range commands {
		if args[0] == c {
			return true
		}
	}
	return false
}

// env is where a command reads and writes; the tests give their own
type env struct {
	stdin          io.Reader
	stdout, stderr io.Writer
}

// Run runs the command of the arguments (without the program name) and
// returns the exit code
func Run(args []string) int {
	return run(args, env{stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr})
}

func run(args []string, e env) int {
	if len(args) == 0 {
		fmt.Fprint(e.stderr, usage)
		return exitUsage
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprint(e.stdout, usage)
		return exitOK
	case "version", "--version":
		fmt.Fprintf(e.stdout, "%s %s\n", app.DisplayName, app.Version)
		return exitOK
	}
	if cmd, ok := handlers[args[0]]; ok {
		return cmd(args[1:], e)
	}
	return usageError(e, fmt.Errorf("unknown command %s", args[0]))
}

func usageError(e env, err error) int {
	fmt.Fprintf(e.stderr, "altesp: %v\n\n%s", err, usage)
	return exitUsage
}
