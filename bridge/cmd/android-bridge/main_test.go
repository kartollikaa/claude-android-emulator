//go:build unix

package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Set, it makes this test binary run as the bridge.
const beTheBridge = "ANDROID_BRIDGE_TEST_MAIN"

func TestMain(m *testing.M) {
	if os.Getenv(beTheBridge) != "" {
		main()
		return
	}
	os.Exit(m.Run())
}

var liveViewURL = regexp.MustCompile(`^http://127\.0\.0\.1:([0-9]+)$`)

func bridgeCommand(t *testing.T, name string, args ...string) *exec.Cmd {
	t.Helper()
	empty := t.TempDir()
	cmd := exec.Command(name, append(args, "-port", "0", "-discovery-dir", empty, "-sdk", empty)...)
	cmd.Env = append(os.Environ(), beTheBridge+"=1")
	return cmd
}

func TestPortZeroAnnouncesTheLiveView(t *testing.T) {
	cmd := bridgeCommand(t, os.Args[0])
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill() })

	lines := bufio.NewReader(stdout)
	firstLine := make(chan string, 1)
	go func() {
		line, _ := lines.ReadString('\n')
		firstLine <- line
	}()
	var first string
	select {
	case first = <-firstLine:
	case <-time.After(5 * time.Second):
		t.Fatalf("no line on stdout within 5 s; stderr: %s", stderr.String())
	}
	var announced map[string]string
	if err := json.Unmarshal([]byte(first), &announced); err != nil || len(announced) != 1 {
		t.Fatalf("stdout line %q, want exactly {\"liveView\": <url>}", first)
	}
	match := liveViewURL.FindStringSubmatch(announced["liveView"])
	if match == nil || match[1] == "0" {
		t.Fatalf("live view %q, want http://127.0.0.1:<a free port>", announced["liveView"])
	}
	response, err := http.Get(announced["liveView"])
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Errorf("GET %s answered %d, want 200", announced["liveView"], response.StatusCode)
	}

	cmd.Process.Signal(syscall.SIGTERM)
	rest, _ := io.ReadAll(lines)
	cmd.Wait()
	if len(rest) > 0 {
		t.Errorf("stdout carried more than the announcement: %q", rest)
	}
	if !strings.Contains(stderr.String(), "live view ready") {
		t.Errorf("stderr = %q, want the bridge's log", stderr.String())
	}
}

func TestBridgeExitsWithItsParent(t *testing.T) {
	parent := bridgeCommand(t, "/bin/sh", "-c", `"$0" "$@" & echo "pid $!"; wait`, os.Args[0])
	stdout, err := parent.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { parent.Process.Kill() })

	pids := make(chan int, 1)
	go func() {
		lines := bufio.NewScanner(stdout)
		for lines.Scan() {
			if pid, isPid := strings.CutPrefix(lines.Text(), "pid "); isPid {
				number, _ := strconv.Atoi(pid)
				pids <- number
			}
		}
	}()
	var bridge int
	select {
	case bridge = <-pids:
	case <-time.After(5 * time.Second):
		t.Fatal("the bridge never started")
	}
	t.Cleanup(func() { syscall.Kill(bridge, syscall.SIGKILL) })

	time.Sleep(1500 * time.Millisecond)
	if syscall.Kill(bridge, 0) != nil {
		t.Fatal("the bridge exited while its parent ran")
	}
	parent.Process.Kill()
	parent.Wait()
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(bridge, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatal("the bridge kept running 5 s after its parent was gone")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
