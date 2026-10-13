package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const backupLogLines = 200

type Backups struct {
	bucket   string
	script   string
	client   *s3.Client
	presign  *s3.PresignClient
	logs     *cloudwatchlogs.Client
	logGroup string // the backups container's (nightly runs)
	// The panel's own (manual runs).
	panelLogGroup string

	mu       sync.Mutex
	running  bool
	database string // "" while backing up every database
	started  time.Time
	finished time.Time
	ok       bool
	logLines []string
}

type BackupObject struct {
	Key      string
	Name     string
	Size     int64
	Modified time.Time
}

type BackupRun struct {
	Running  bool
	Database string
	// Why it failed: the script's last message before the exit status.
	Reason   string
	Started  time.Time
	Finished time.Time
	OK       bool
	Log      string
}

// NewBackups uses the default AWS credential chain: the instance role on EC2,
// or AWS_* env vars (AWS_ENDPOINT_URL for a local S3).
func NewBackups(ctx context.Context, cfg Config) (*Backups, error) {
	awsCfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) { o.UsePathStyle = cfg.S3PathStyle })
	return &Backups{
		bucket:  cfg.S3Bucket,
		script:  cfg.BackupScript,
		client:  client,
		presign: s3.NewPresignClient(client),
		logs:    cloudwatchlogs.NewFromConfig(awsCfg),
		// Same default as backups/run.sh.
		// The groups docker-compose.yaml sends each container's output to.
		logGroup:      env("BACKUP_LOG_GROUP", "just-a-db/backups-logs"),
		panelLogGroup: env("PANEL_LOG_GROUP", "just-a-db/database-logs"),
	}, nil
}

// Folders lists the top-level prefixes: one per database, plus _globals.
// Databases that were dropped keep their folder, so their backups stay
// reachable.
func (b *Backups) Folders(ctx context.Context) ([]string, error) {
	var folders []string
	p := s3.NewListObjectsV2Paginator(b.client, &s3.ListObjectsV2Input{
		Bucket:    aws.String(b.bucket),
		Delimiter: aws.String("/"),
	})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, cp := range page.CommonPrefixes {
			folders = append(folders, strings.TrimSuffix(aws.ToString(cp.Prefix), "/"))
		}
	}
	sort.Strings(folders)
	return folders, nil
}

// List returns a folder's backups, newest first.
func (b *Backups) List(ctx context.Context, folder string) ([]BackupObject, error) {
	var out []BackupObject
	p := s3.NewListObjectsV2Paginator(b.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(b.bucket),
		Prefix: aws.String(folder + "/"),
	})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, o := range page.Contents {
			key := aws.ToString(o.Key)
			if strings.HasSuffix(key, "/") {
				continue
			}
			out = append(out, BackupObject{
				Key:      key,
				Name:     path.Base(key),
				Size:     aws.ToInt64(o.Size),
				Modified: aws.ToTime(o.LastModified),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out, nil
}

// BackupRunLog is one run of backups/run.sh (nightly or manual), read from
// CloudWatch Logs: between its "=== Backup run started" and "=== Backup run
// finished" lines.
type BackupRunLog struct {
	Started time.Time
	Result  string // "ok" or "failed"
	Scope   string // "all" or a database name
	Log     string
}

const (
	runStarted  = "=== Backup run started"
	runFinished = "=== Backup run finished"
	// Prefix of run.sh's lines in the panel's own output (manual runs).
	panelBackupPrefix = "[backup] "
	// How far back to look: FilterLogEvents scans every event in range.
	runsLookback = 7 * 24 * time.Hour
)

var runFinishedLine = regexp.MustCompile(`=== Backup run finished: (ok|failed) \((.+)\)`)

type runEvent struct {
	group, stream string
	at            int64
	message       string
}

// RecentRuns returns the latest n finished runs, newest first, with their
// logs. Nightly runs are in the backups container's log group; manual ones,
// run by the panel, in the panel's.
func (b *Backups) RecentRuns(ctx context.Context, n int) ([]BackupRunLog, error) {
	var finished []runEvent
	for _, group := range []string{b.logGroup, b.panelLogGroup} {
		events, err := b.filterEvents(ctx, group, nil, runFinished, time.Now().Add(-runsLookback).UnixMilli(), 0)
		if err != nil {
			return nil, err
		}
		finished = append(finished, events...)
	}
	sort.Slice(finished, func(i, j int) bool { return finished[i].at > finished[j].at })
	if len(finished) > n {
		finished = finished[:n]
	}

	var runs []BackupRunLog
	for _, end := range finished {
		run := BackupRunLog{Started: time.UnixMilli(end.at)}
		if m := runFinishedLine.FindStringSubmatch(end.message); m != nil {
			run.Result, run.Scope = m[1], m[2]
		}
		// The run's start: the last "started" line before its end, same stream.
		starts, err := b.filterEvents(ctx, end.group, []string{end.stream}, runStarted,
			end.at-int64(6*time.Hour/time.Millisecond), end.at+1)
		if err != nil {
			return nil, err
		}
		from := end.at
		if len(starts) > 0 {
			from = starts[len(starts)-1].at
			run.Started = time.UnixMilli(from)
		}
		lines, err := b.streamLines(ctx, end.group, end.stream, from, end.at+1)
		if err != nil {
			return nil, err
		}
		run.Log = strings.Join(lines, "\n")
		runs = append(runs, run)
	}
	return runs, nil
}

// filterEvents returns a group's events containing `term`, oldest first.
// endMs 0 means now.
func (b *Backups) filterEvents(ctx context.Context, group string, streams []string, term string, startMs, endMs int64) ([]runEvent, error) {
	in := &cloudwatchlogs.FilterLogEventsInput{
		LogGroupName:  aws.String(group),
		FilterPattern: aws.String(`"` + term + `"`),
		StartTime:     aws.Int64(startMs),
	}
	if len(streams) > 0 {
		in.LogStreamNames = streams
	}
	if endMs > 0 {
		in.EndTime = aws.Int64(endMs)
	}
	var out []runEvent
	p := cloudwatchlogs.NewFilterLogEventsPaginator(b.logs, in)
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			var notFound *cwtypes.ResourceNotFoundException
			if errors.As(err, &notFound) {
				return nil, nil
			}
			return nil, err
		}
		for _, e := range page.Events {
			out = append(out, runEvent{group: group, stream: aws.ToString(e.LogStreamName),
				at: aws.ToInt64(e.Timestamp), message: aws.ToString(e.Message)})
		}
	}
	return out, nil
}

// streamLines returns a stream's lines between two times. In the panel's
// stream only run.sh's lines (with their prefix removed) are kept.
func (b *Backups) streamLines(ctx context.Context, group, stream string, startMs, endMs int64) ([]string, error) {
	var lines []string
	in := &cloudwatchlogs.GetLogEventsInput{
		LogGroupName:  aws.String(group),
		LogStreamName: aws.String(stream),
		StartTime:     aws.Int64(startMs),
		EndTime:       aws.Int64(endMs),
		StartFromHead: aws.Bool(true),
	}
	for page := 0; page < 20; page++ {
		out, err := b.logs.GetLogEvents(ctx, in)
		if err != nil {
			return nil, err
		}
		for _, e := range out.Events {
			line := strings.TrimRight(aws.ToString(e.Message), "\n")
			if group == b.panelLogGroup {
				var ok bool
				if line, ok = strings.CutPrefix(line, panelBackupPrefix); !ok {
					continue
				}
			}
			lines = append(lines, line)
		}
		// The same token back means the end of the stream.
		if len(out.Events) == 0 || aws.ToString(out.NextForwardToken) == aws.ToString(in.NextToken) {
			break
		}
		in.NextToken = out.NextForwardToken
	}
	return lines, nil
}

// Latest is a database's newest backup, or nil if it has none.
func (b *Backups) Latest(ctx context.Context, database string) (*BackupObject, error) {
	list, err := b.List(ctx, database)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

// Open streams an object from S3.
func (b *Backups) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := b.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(b.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, err
	}
	return out.Body, nil
}

// DownloadURL is a short-lived link straight to S3, so the file never goes
// through the panel's memory.
func (b *Backups) DownloadURL(ctx context.Context, key string) (string, error) {
	req, err := b.presign.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket:                     aws.String(b.bucket),
		Key:                        aws.String(key),
		ResponseContentDisposition: aws.String(`attachment; filename="` + path.Base(key) + `"`),
	}, s3.WithPresignExpires(5*time.Minute))
	if err != nil {
		return "", err
	}
	return req.URL, nil
}

// Run starts backups/run.sh (the same script the nightly cron runs) for every
// database, or only `database` if set, unless a backup is already running.
func (b *Backups) Run(database string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.running {
		return false
	}
	b.running = true
	b.database = database
	b.started = time.Now()
	b.finished = time.Time{}
	b.logLines = nil
	go b.run(database)
	return true
}

func (b *Backups) run(database string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	var args []string
	if database != "" {
		args = []string{database}
	}
	cmd := exec.CommandContext(ctx, b.script, args...)
	cmd.Env = os.Environ()
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			b.appendLog(sc.Text())
		}
	}()

	err := cmd.Run()
	pw.Close()
	<-done
	if err != nil {
		b.appendLog("ERROR: " + err.Error())
	}

	b.mu.Lock()
	b.running = false
	b.finished = time.Now()
	b.ok = err == nil
	b.mu.Unlock()
}

func (b *Backups) appendLog(line string) {
	// Also to the panel's output, which Docker sends to CloudWatch, so manual
	// runs show up in "Últimas corridas" too.
	fmt.Println(panelBackupPrefix + line)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.logLines = append(b.logLines, line)
	if len(b.logLines) > backupLogLines {
		b.logLines = b.logLines[len(b.logLines)-backupLogLines:]
	}
}

func failureReason(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" && !strings.HasPrefix(l, "ERROR: ") && l != "Backup FAILED" {
			return l
		}
	}
	return ""
}

func (b *Backups) Status() BackupRun {
	b.mu.Lock()
	defer b.mu.Unlock()
	return BackupRun{
		Running:  b.running,
		Database: b.database,
		Reason:   failureReason(b.logLines),
		Started:  b.started,
		Finished: b.finished,
		OK:       b.ok,
		Log:      strings.Join(b.logLines, "\n"),
	}
}
