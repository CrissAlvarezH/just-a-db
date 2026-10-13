package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// Lines of a plain-SQL dump that refer to the original roles, which don't
// exist here (the restored objects belong to the new database's user).
var plainDumpSkip = regexp.MustCompile(`^(ALTER .* OWNER TO .*;|GRANT .*;|REVOKE .*;)$`)

// restorableBackup reports whether key is a database backup the panel can
// restore: a pg_dump custom-format .dump (backups/run.sh), or a plain SQL
// dump, optionally gzipped (what older versions of run.sh made).
func restorableBackup(key string) bool {
	folder, file, ok := strings.Cut(key, "/")
	// _globals (roles) and _logs aren't databases.
	if !ok || folder == "" || strings.HasPrefix(folder, "_") || strings.Contains(file, "/") || strings.Contains(key, "..") {
		return false
	}
	return strings.HasSuffix(file, ".dump") || strings.HasSuffix(file, ".sql.gz") || strings.HasSuffix(file, ".sql")
}

// RestoreBackup loads a backup from S3 into database, an empty database owned
// by role, streaming it from S3 into pg_restore/psql (nothing is held in
// memory or written to disk). Objects end up owned by role, and the original
// owners and grants are dropped. It runs in one transaction: on any error
// nothing is restored. Returns the tool's output.
func (a *App) RestoreBackup(ctx context.Context, key, database, role string) (string, error) {
	if a.backups == nil {
		return "", fmt.Errorf("no hay S3 configurado para backups")
	}
	if !restorableBackup(key) {
		return "", fmt.Errorf("%q no es un backup que se pueda restaurar", key)
	}
	body, err := a.backups.Open(ctx, key)
	if err != nil {
		return "", fmt.Errorf("no se pudo leer el backup de S3: %w", err)
	}
	defer body.Close()

	conn := []string{"-h", a.cfg.DBHost, "-p", a.cfg.DBPort, "-U", a.cfg.SuperUser, "-d", database}
	var cmd *exec.Cmd
	if strings.HasSuffix(key, ".dump") {
		args := append(conn, "--no-owner", "--no-privileges", "--role="+role,
			"--single-transaction", "--exit-on-error")
		cmd = exec.CommandContext(ctx, "pg_restore", args...)
		cmd.Stdin = body
	} else {
		var src io.Reader = body
		if strings.HasSuffix(key, ".gz") {
			gz, err := gzip.NewReader(body)
			if err != nil {
				return "", fmt.Errorf("el backup no es un .gz válido: %w", err)
			}
			defer gz.Close()
			src = gz
		}
		args := append(conn, "-v", "ON_ERROR_STOP=1", "--single-transaction", "-q", "-X")
		cmd = exec.CommandContext(ctx, "psql", args...)
		cmd.Stdin = io.MultiReader(
			strings.NewReader(fmt.Sprintf("SET ROLE %s;\n", ident(role))),
			filterPlainDump(src),
		)
	}
	cmd.Env = append(os.Environ(), "PGPASSWORD="+a.cfg.SuperPassword)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err = cmd.Run()
	return tail(out.String(), 60), err
}

// filterPlainDump drops the owner/grant lines of a plain SQL dump, leaving
// COPY data untouched.
func filterPlainDump(src io.Reader) io.Reader {
	pr, pw := io.Pipe()
	go func() {
		sc := bufio.NewScanner(src)
		sc.Buffer(make([]byte, 1024*1024), 64*1024*1024) // long COPY rows
		w := bufio.NewWriter(pw)
		inCopy := false
		for sc.Scan() {
			line := sc.Text()
			switch {
			case inCopy:
				inCopy = line != `\.`
			case strings.HasPrefix(line, "COPY ") && strings.HasSuffix(line, "FROM stdin;"):
				inCopy = true
			case plainDumpSkip.MatchString(line):
				continue
			}
			w.WriteString(line)
			w.WriteByte('\n')
		}
		if err := sc.Err(); err != nil {
			pw.CloseWithError(err)
			return
		}
		pw.CloseWithError(w.Flush())
	}()
	return pr
}

func tail(s string, lines int) string {
	parts := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(parts) > lines {
		parts = append([]string{"…"}, parts[len(parts)-lines:]...)
	}
	return strings.Join(parts, "\n")
}

// backupGroup is a folder's backups, for the restore picker.
type backupGroup struct {
	Folder  string
	Objects []BackupObject
}

// restoreChoices lists the restorable backups, the newest `perFolder` of each
// database folder.
func (a *App) restoreChoices(ctx context.Context, perFolder int) ([]backupGroup, error) {
	if a.backups == nil {
		return nil, nil
	}
	folders, err := a.backups.Folders(ctx)
	if err != nil {
		return nil, err
	}
	var groups []backupGroup
	for _, f := range folders {
		if strings.HasPrefix(f, "_") {
			continue
		}
		list, err := a.backups.List(ctx, f)
		if err != nil {
			return nil, err
		}
		g := backupGroup{Folder: f}
		for _, o := range list {
			if restorableBackup(o.Key) && len(g.Objects) < perFolder {
				g.Objects = append(g.Objects, o)
			}
		}
		if len(g.Objects) > 0 {
			groups = append(groups, g)
		}
	}
	return groups, nil
}
