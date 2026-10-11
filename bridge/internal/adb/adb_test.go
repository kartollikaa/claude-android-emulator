package adb

import (
	"os/exec"
	"testing"
)

func TestForegroundPackage(t *testing.T) {
	for _, step := range []struct{ activities, want string }{
		{"  topResumedActivity=ActivityRecord{6f3a1c2 u0 com.google.android.youtube/.HomeActivity t23}\n", "com.google.android.youtube"},
		{"    mResumedActivity: ActivityRecord{1d2e3f u0 com.android.settings/.Settings t7}\n", "com.android.settings"},
		{"  topResumedActivity=ActivityRecord{6f3a1c2 u0 com.x;rm -rf/.A t1}\n", ""},
		{"no activity in front\n", ""},
	} {
		if got := ForegroundPackage(step.activities); got != step.want {
			t.Errorf("ForegroundPackage(%q) = %q, want %q", step.activities, got, step.want)
		}
	}
}

func TestResolvedPackage(t *testing.T) {
	resolved := "priority=0 preferredOrder=0 match=0x108000 specificIndex=-1 isDefault=true\ncom.google.android.apps.nexuslauncher/.NexusLauncherActivity\n"
	if got := ResolvedPackage(resolved); got != "com.google.android.apps.nexuslauncher" {
		t.Errorf("ResolvedPackage = %q, want the launcher's package", got)
	}
	if got := ResolvedPackage("No activity found\n"); got != "" {
		t.Errorf("ResolvedPackage of no match = %q, want none", got)
	}
}

func TestResolvedComponent(t *testing.T) {
	for resolved, want := range map[string]string{
		"priority=0 match=0x108000\ncom.example/.MainActivity\n": "com.example/.MainActivity",
		"com.example/com.example.ui.Main$Alias":                  "com.example/com.example.ui.Main$Alias",
		"No activity found\n":                                    "",
		"com.example/.Main; reboot":                              "",
		"not a package/.Main":                                    "",
	} {
		if got := ResolvedComponent(resolved); got != want {
			t.Errorf("ResolvedComponent(%q) = %q, want %q", resolved, got, want)
		}
	}
}

func TestQuoteKeepsOneWordForTheShell(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no POSIX shell to read the quoting")
	}
	for _, word := range []string{"plain", "a b", "it's", "''", "$HOME `id` \"x\" \\ ; &"} {
		output, err := exec.Command("sh", "-c", "printf %s "+Quote(word)).Output()
		if err != nil || string(output) != word {
			t.Errorf("sh read Quote(%q) as %q (%v)", word, output, err)
		}
	}
}
