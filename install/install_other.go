//go:build !windows

package install

import "errors"

var errNotSupported = errors.New("installation is supported on Windows only")

// CurrentStatus is always StatusNone: there is nothing to offer here
func CurrentStatus() Status { return StatusNone }

func IsInstalledCopy() bool { return false }

func Install() error { return errNotSupported }

func Uninstall() error { return errNotSupported }

func StartInstalled() error { return errNotSupported }

// Confirm and Inform are the dialogs of the uninstaller, which has no window
func Confirm(title, text string) bool { return false }

func Inform(title, text string) {}
