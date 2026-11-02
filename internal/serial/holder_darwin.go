package serial

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// holder asks lsof, which lists the pid and the command of each process
// that has the file open: "p1234" and "cminicom" lines
func holder(name string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// lsof exits with 1 when nobody has the file open; the output is read
	// anyway
	out, _ := exec.CommandContext(ctx, "lsof", "-F", "pc", name).Output()
	return parseLsof(string(out))
}

func parseLsof(out string) string {
	var found []string
	pid := ""
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			pid = line[1:]
		case 'c':
			if pid != "" {
				found = append(found, line[1:]+" (pid "+pid+")")
				pid = ""
			}
		}
	}
	return strings.Join(found, ", ")
}
