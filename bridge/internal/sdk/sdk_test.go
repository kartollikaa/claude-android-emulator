package sdk

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func sdkWithEmulator(t *testing.T, root, binary string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "emulator"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "emulator", binary), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestSDKLookupOrder(t *testing.T) {
	androidHome := sdkWithEmulator(t, t.TempDir(), "emulator")
	sdkRoot := sdkWithEmulator(t, t.TempDir(), "emulator")
	withoutEmulator := t.TempDir()
	macHome := t.TempDir()
	macDefault := sdkWithEmulator(t, filepath.Join(macHome, "Library", "Android", "sdk"), "emulator")
	linuxHome := t.TempDir()
	linuxDefault := sdkWithEmulator(t, filepath.Join(linuxHome, "Android", "Sdk"), "emulator")
	localAppData := t.TempDir()
	windowsDefault := sdkWithEmulator(t, filepath.Join(localAppData, "Android", "Sdk"), "emulator.exe")

	for _, step := range []struct {
		name string
		env  map[string]string
		home string
		goos string
		want string
	}{
		{"ANDROID_HOME first", map[string]string{"ANDROID_HOME": androidHome, "ANDROID_SDK_ROOT": sdkRoot}, macHome, "darwin", androidHome},
		{"ANDROID_SDK_ROOT next", map[string]string{"ANDROID_SDK_ROOT": sdkRoot}, macHome, "darwin", sdkRoot},
		{"a variable without the emulator is skipped", map[string]string{"ANDROID_HOME": withoutEmulator, "ANDROID_SDK_ROOT": sdkRoot}, macHome, "darwin", sdkRoot},
		{"macOS default", map[string]string{"ANDROID_HOME": withoutEmulator}, macHome, "darwin", macDefault},
		{"Linux default", nil, linuxHome, "linux", linuxDefault},
		{"Windows default", map[string]string{"LOCALAPPDATA": localAppData}, t.TempDir(), "windows", windowsDefault},
	} {
		getenv := func(name string) string { return step.env[name] }
		found, err := Find(Candidates(getenv, step.home, step.goos), step.goos)
		if err != nil || found.Root != step.want {
			t.Errorf("%s: Find = %q, %v; want %q", step.name, found.Root, err, step.want)
		}
	}

	getenv := func(name string) string { return map[string]string{"ANDROID_HOME": withoutEmulator}[name] }
	empty := t.TempDir()
	candidates := Candidates(getenv, empty, "linux")
	_, err := Find(candidates, "linux")
	var notFound *NotFoundError
	if !errors.As(err, &notFound) || !slices.Equal(notFound.Looked, []string{withoutEmulator, filepath.Join(empty, "Android", "Sdk")}) {
		t.Errorf("Find with no emulator anywhere = %v, want a NotFoundError naming both places", err)
	}
}

func TestAAPT2IsTheNewestBuildTools(t *testing.T) {
	root := t.TempDir()
	if got := (SDK{Root: root, goos: "darwin"}).AAPT2(); got != "" {
		t.Errorf("an SDK without build-tools answered %q", got)
	}
	for _, path := range []string{"9.0.0/aapt2", "36.1.0/aapt2", "35.0.1/aapt2", "36.0.0/aapt2.exe", "34.0.0/aapt2.exe"} {
		file := filepath.Join(root, "build-tools", path)
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := (SDK{Root: root, goos: "darwin"}).AAPT2(), filepath.Join(root, "build-tools", "36.1.0", "aapt2"); got != want {
		t.Errorf("AAPT2 = %q, want %q", got, want)
	}
	if got, want := (SDK{Root: root, goos: "windows"}).AAPT2(), filepath.Join(root, "build-tools", "36.0.0", "aapt2.exe"); got != want {
		t.Errorf("AAPT2 on Windows = %q, want %q", got, want)
	}
}
