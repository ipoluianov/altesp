package serial

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// holder looks through the open files of the processes for the device.
// The processes of other users can't be seen without root and are skipped.
func holder(name string) string {
	path, err := filepath.EvalSymlinks(name)
	if err != nil {
		path = name
	}
	procs, err := os.ReadDir("/proc")
	if err != nil {
		return ""
	}
	var found []string
	for _, e := range procs {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		dir := "/proc/" + e.Name() + "/fd"
		fds, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			if target, err := os.Readlink(dir + "/" + fd.Name()); err == nil && target == path {
				found = append(found, processName(pid))
				break
			}
		}
	}
	return strings.Join(found, ", ")
}

// processName is "minicom (pid 1234)"
func processName(pid int) string {
	comm, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm")
	name := strings.TrimSpace(string(comm))
	if err != nil || name == "" {
		name = "process"
	}
	return name + " (pid " + strconv.Itoa(pid) + ")"
}
