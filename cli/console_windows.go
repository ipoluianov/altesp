package cli

import (
	"os"

	"golang.org/x/sys/windows"
)

// attachParentProcess is ATTACH_PARENT_PROCESS: the console of the parent
const attachParentProcess = ^uint32(0)

var procAttachConsole = windows.NewLazySystemDLL("kernel32.dll").NewProc("AttachConsole")

// AttachConsole connects the program to the console of the command line
// that started it: the build for the window has no console of its own.
// The streams that are redirected (to a file, a pipe of a script)
// are kept as they are.
func AttachConsole() {
	in := valid(windows.STD_INPUT_HANDLE)
	out := valid(windows.STD_OUTPUT_HANDLE)
	errOK := valid(windows.STD_ERROR_HANDLE)
	if in && out && errOK {
		return
	}
	if r, _, _ := procAttachConsole.Call(uintptr(attachParentProcess)); r == 0 {
		return // started without a console: from Explorer, a service
	}
	if !in {
		if f, err := os.OpenFile("CONIN$", os.O_RDWR, 0); err == nil {
			os.Stdin = f
			windows.SetStdHandle(windows.STD_INPUT_HANDLE, windows.Handle(f.Fd()))
		}
	}
	if !out || !errOK {
		if f, err := os.OpenFile("CONOUT$", os.O_RDWR, 0); err == nil {
			if !out {
				os.Stdout = f
				windows.SetStdHandle(windows.STD_OUTPUT_HANDLE, windows.Handle(f.Fd()))
			}
			if !errOK {
				os.Stderr = f
				windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(f.Fd()))
			}
		}
	}
}

// valid tells whether the standard handle is open
func valid(which uint32) bool {
	h, err := windows.GetStdHandle(which)
	return err == nil && h != 0 && h != windows.InvalidHandle
}
