package discovery

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func write(t *testing.T, dir, name, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverPicksTheEmulatorWithGrpc(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "pid_100.ini", "port.serial=5556\nport.adb=5557\navd.name=NoGrpc\n")
	write(t, dir, "pid_200.ini", "port.serial=5554\r\ngrpc.port=8554\r\ngrpc.token=abc==\r\navd.id=Pixel_7\r\navd.name=Pixel 7\r\n")
	write(t, dir, "notes.txt", "grpc.port=9999\n")

	found, err := Find(dir)
	if err != nil {
		t.Fatal(err)
	}

	want := []Emulator{{Serial: "emulator-5554", AVD: "Pixel_7", GRPCPort: 8554, Token: "abc=="}}
	if !reflect.DeepEqual(found, want) {
		t.Fatalf("Find = %+v, want %+v", found, want)
	}
}

func TestDiscoverWithNoEmulator(t *testing.T) {
	for name, dir := range map[string]string{
		"empty dir":   t.TempDir(),
		"missing dir": filepath.Join(t.TempDir(), "absent"),
	} {
		t.Run(name, func(t *testing.T) {
			found, err := Find(dir)
			if err != nil || len(found) != 0 {
				t.Fatalf("Find = %+v, %v; want none and no error", found, err)
			}
		})
	}
}

func TestRunningAVDsSkipsFilesOfDeadProcesses(t *testing.T) {
	finished := exec.Command("true")
	if err := finished.Run(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	write(t, dir, fmt.Sprintf("pid_%d.ini", os.Getpid()), "avd.id=Alive\nport.serial=5554\n")
	write(t, dir, fmt.Sprintf("pid_%d.ini", finished.Process.Pid), "avd.id=Killed\nport.serial=5556\ngrpc.port=8556\n")

	running, err := RunningAVDs(dir)

	if err != nil || !reflect.DeepEqual(running, map[string]bool{"Alive": true}) {
		t.Fatalf("RunningAVDs = %v, %v; want only Alive", running, err)
	}
}
