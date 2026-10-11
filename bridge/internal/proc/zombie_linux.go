package proc

import (
	"bytes"
	"fmt"
	"os"
)

func isZombie(pid int) bool {
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	afterName := bytes.Fields(stat[bytes.LastIndexByte(stat, ')')+1:])

	return len(afterName) > 0 && string(afterName[0]) == "Z"
}
