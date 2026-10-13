package main

import "testing"

func TestRunFinishedLine(t *testing.T) {
	for line, want := range map[string][2]string{
		"=== Backup run finished: ok (all)":                {"ok", "all"},
		"=== Backup run finished: failed (app_nueva_1)":    {"failed", "app_nueva_1"},
		"[backup] === Backup run finished: ok (just-a-db)": {"ok", "just-a-db"},
	} {
		m := runFinishedLine.FindStringSubmatch(line)
		if m == nil || m[1] != want[0] || m[2] != want[1] {
			t.Errorf("%q: got %v, want %v", line, m, want)
		}
	}
	if runFinishedLine.MatchString("=== Backup run started (all)") {
		t.Error("a start line matched")
	}
}
