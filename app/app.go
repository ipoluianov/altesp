// Package app holds the name and the version of the program
package app

import (
	"fmt"
	"os/exec"
	"runtime"
	"time"
)

const (
	DisplayName = "AltEsp"
	Website     = "https://altbins.pro/altesp/"
	Author      = "Ivan Poluianov"
	License     = "MIT"

	copyrightStartYear = 2026
)

// Version is set at build time from the git tag:
// -ldflags "-X github.com/ipoluianov/altesp/app.Version=<tag>"
var Version = "dev"

// Copyright returns "Copyright © 2026-<current year> <author>"
func Copyright() string {
	years := fmt.Sprint(copyrightStartYear)
	if y := time.Now().Year(); y > copyrightStartYear {
		years = fmt.Sprintf("%d-%d", copyrightStartYear, y)
	}
	return "Copyright © " + years + " " + Author
}

// OpenURL opens the url in the default browser
func OpenURL(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
