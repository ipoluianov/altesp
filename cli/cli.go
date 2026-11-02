// Package cli is the command line of the program: the same actions as the
// window, for scripts and CI. The commands are recognized now; they are
// done as the operations of the program come.
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
  altesp erase   [--port P]                erase the whole flash
  altesp monitor [--port P] [--elf FILE] [--reset]
  altesp version

The port can be omitted when exactly one board is connected.

Options:
  -p, --port P          the port: COM5, /dev/ttyUSB0
  -b, --baud N          the baud rate
      --elf FILE        decode the panics with the .elf (monitor)
      --reset           reset the board when the monitor starts
      --json            machine-readable output
`

// Exit codes
const (
	exitOK    = 0
	exitFail  = 1
	exitUsage = 2
)

// commands are the commands of the console; the window has the others
var commands = []string{"ports", "info", "flash", "read", "erase", "monitor"}

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
	for _, c := range commands {
		if args[0] == c {
			// TODO: the commands call the operations of the program
			fmt.Fprintf(e.stderr, "altesp: %s: not implemented yet\n", c)
			return exitUsage
		}
	}
	return usageError(e, fmt.Errorf("unknown command %s", args[0]))
}

func usageError(e env, err error) int {
	fmt.Fprintf(e.stderr, "altesp: %v\n\n%s", err, usage)
	return exitUsage
}
