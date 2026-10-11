package adb

import "testing"

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
