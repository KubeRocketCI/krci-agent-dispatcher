package proctest

import (
	"bytes"
	"os"
	"strconv"
)

// zombie reports whether /proc/<pid>/stat shows state Z: the field after the last ")".
func zombie(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}

	i := bytes.LastIndexByte(b, ')')

	return i >= 0 && len(b) > i+2 && b[i+2] == 'Z'
}
