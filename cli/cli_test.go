package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestIsCommand(t *testing.T) {
	yes := [][]string{{"ports"}, {"info", "--port", "COM5"}, {"flash", "build/"}, {"read"},
		{"erase"}, {"monitor"}, {"help"}, {"-h"}, {"--help"}, {"version"}, {"--version"}}
	for _, a := range yes {
		if !IsCommand(a) {
			t.Error("not a command:", a)
		}
	}
	no := [][]string{nil, {}, {"-psn_0_12345"}, {"--uninstall"}, {"--installed"}, {"file.bin"}, {"COM5"}}
	for _, a := range no {
		if IsCommand(a) {
			t.Error("a command:", a)
		}
	}
}

func TestRun(t *testing.T) {
	tests := []struct {
		args []string
		code int
		out  string // in stdout, or in stderr when the code is not 0
	}{
		{[]string{"--help"}, exitOK, "altesp flash"},
		{[]string{"version"}, exitOK, "AltEsp"},
		{[]string{"ports"}, exitUsage, "not implemented yet"},
		{[]string{"flash", "--port", "COM5", "build/"}, exitUsage, "not implemented yet"},
		{[]string{"burn"}, exitUsage, "unknown command"},
		{nil, exitUsage, "Usage"},
	}
	for _, tt := range tests {
		var out, errOut bytes.Buffer
		code := run(tt.args, env{stdout: &out, stderr: &errOut})
		text := out.String()
		if code != exitOK {
			text = errOut.String()
		}
		if code != tt.code || !strings.Contains(text, tt.out) {
			t.Errorf("%v: %d %q", tt.args, code, text)
		}
	}
}
