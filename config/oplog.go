package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The log of the dangerous operations: every erase, partition write, table
// write, NVS write, OTA switch (and eFuse burn) is appended to a text file
// in the settings directory, a line each: the time, the board's MAC and
// port, what was done. Copies of the program append whole lines.

// OperationsLogPath is the file of the log
func OperationsLogPath() string { return filepath.Join(Dir(), "operations.log") }

// LogOperation appends a line to the log
func LogOperation(mac, port, what string) error {
	if mac == "" {
		mac = "-"
	}
	if port == "" {
		port = "-"
	}
	what = strings.ReplaceAll(what, "\n", " ")
	line := fmt.Sprintf("%s\t%s\t%s\t%s\n", time.Now().Format("2006-01-02 15:04:05"), mac, port, what)
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(OperationsLogPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = f.WriteString(line)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// OperationLine is a line of the log
type OperationLine struct {
	Time, MAC, Port, What string
}

// Operations reads the log, the newest last; a missing log is empty
func Operations() ([]OperationLine, error) {
	data, err := os.ReadFile(OperationsLogPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []OperationLine
	for _, l := range strings.Split(string(data), "\n") {
		f := strings.SplitN(l, "\t", 4)
		if len(f) == 4 {
			out = append(out, OperationLine{f[0], f[1], f[2], f[3]})
		}
	}
	return out, nil
}
