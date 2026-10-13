package main

import (
	"testing"
	"time"
)

func TestRunFinishedLine(t *testing.T) {
	for line, want := range map[string][2]string{
		"=== Backup run finished: ok (all)":             {"ok", "all"},
		"=== Backup run finished: failed (app_nueva_1)": {"failed", "app_nueva_1"},
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

func TestParseRunStream(t *testing.T) {
	run, ok := parseRunStream("2026-10-13_02-00-03_content-studio_dev_manual")
	if !ok || run.Scope != "content-studio_dev" || run.Trigger != "manual" ||
		!run.Started.Equal(time.Date(2026, 10, 13, 2, 0, 3, 0, time.UTC)) {
		t.Errorf("got %+v, %v", run, ok)
	}
	for _, bad := range []string{"", "7beb2e00d5b0", "2026-10-13_02-00-03_all", "2026-10-13_02-00-03_all_weekly", "../x"} {
		if _, ok := parseRunStream(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestRunResult(t *testing.T) {
	now := time.Now()
	cases := []struct {
		line string
		last time.Time
		want string
	}{
		{"=== Backup run finished: ok (all)", now.Add(-time.Hour), "ok"},
		{"=== Backup run finished: failed (x)", now, "failed"},
		{"Backing up database: x", now.Add(-time.Minute), "running"},
		{"Backing up database: x", now.Add(-time.Hour), "interrupted"},
	}
	for _, c := range cases {
		if got := runResult(c.line, c.last, now); got != c.want {
			t.Errorf("%q: got %s, want %s", c.line, got, c.want)
		}
	}
}

func TestConsoleURL(t *testing.T) {
	b := &Backups{region: "us-east-1", runsGroup: "just-a-db/backup-runs"}
	want := "https://us-east-1.console.aws.amazon.com/cloudwatch/home?region=us-east-1#logsV2:log-groups/log-group/just-a-db$252Fbackup-runs/log-events/2026-10-13_02-00-03_all_nightly"
	if got := b.ConsoleURL("2026-10-13_02-00-03_all_nightly"); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}
