//go:build unix

package proc

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestAlive(t *testing.T) {
	if !Alive(os.Getpid()) {
		t.Error("this process is not alive")
	}
	child := exec.Command("/bin/sh", "-c", "exit 0")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if Alive(child.Process.Pid) {
		t.Errorf("a child that exited and was not reaped yet counts as alive")
	}
	child.Wait()
	if Alive(child.Process.Pid) {
		t.Errorf("a reaped child counts as alive")
	}
}
