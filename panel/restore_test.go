package main

import (
	"io"
	"strings"
	"testing"
)

func TestRestorableBackup(t *testing.T) {
	for key, want := range map[string]bool{
		"app/backup_app_2026-10-12.dump":   true,
		"app/backup_app_2026-10-12.sql.gz": true,
		"app/backup_app.sql":               true,
		"_globals/globals_x.sql.gz":        false,
		"app/sub/x.dump":                   false,
		"x.dump":                           false,
		"app/../x.dump":                    false,
		"app/notes.txt":                    false,
	} {
		if got := restorableBackup(key); got != want {
			t.Errorf("restorableBackup(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestFilterPlainDump(t *testing.T) {
	in := strings.Join([]string{
		`CREATE TABLE public.t (x text);`,
		`ALTER TABLE public.t OWNER TO "db-user-old";`,
		`GRANT ALL ON SCHEMA public TO someone;`,
		`COPY public.t (x) FROM stdin;`,
		`ALTER TABLE x OWNER TO y;`, // data, not SQL: kept
		`\.`,
		`REVOKE ALL ON TABLE public.t FROM PUBLIC;`,
		`SELECT 1;`,
	}, "\n") + "\n"
	out, err := io.ReadAll(filterPlainDump(strings.NewReader(in)))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		`CREATE TABLE public.t (x text);`,
		`COPY public.t (x) FROM stdin;`,
		`ALTER TABLE x OWNER TO y;`,
		`\.`,
		`SELECT 1;`,
	}, "\n") + "\n"
	if string(out) != want {
		t.Errorf("got:\n%s\nwant:\n%s", out, want)
	}
}
