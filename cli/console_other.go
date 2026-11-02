//go:build !windows

package cli

// AttachConsole does nothing here: a program started from a terminal has it
func AttachConsole() {}
