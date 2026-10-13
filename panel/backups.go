package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const backupLogLines = 200

type Backups struct {
	bucket  string
	script  string
	client  *s3.Client
	presign *s3.PresignClient
	logs    *cloudwatchlogs.Client
	region  string
	// Where run.sh writes each run's log, one stream per run ("" = none).
	runsGroup string

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
	// Same default as backups/run.sh; set but empty means no CloudWatch.
	runsGroup := "just-a-db/backup-runs"
	if v, ok := os.LookupEnv("BACKUP_RUN_LOG_GROUP"); ok {
		runsGroup = strings.TrimSpace(v)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) { o.UsePathStyle = cfg.S3PathStyle })
	return &Backups{
		bucket:    cfg.S3Bucket,
		script:    cfg.BackupScript,
		client:    client,
		presign:   s3.NewPresignClient(client),
		logs:      cloudwatchlogs.NewFromConfig(awsCfg),
		region:    awsCfg.Region,
		runsGroup: runsGroup,
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

// BackupRunLog is one run of backups/run.sh (nightly or manual): its own
// CloudWatch Logs stream, named <UTC start>_<scope>_<trigger>.
type BackupRunLog struct {
	Stream  string
	Started time.Time
	Last    time.Time // its latest line
	Scope   string    // "all" or a database name
	Trigger string    // "nightly" or "manual"
	// "ok", "failed", "running" (no end yet, recent lines) or "interrupted".
	Result     string
	ConsoleURL string    // the stream in the CloudWatch console
	Lines      []RunLine // only from RunLog
}

// RunLine is one line of a run's log, with the time CloudWatch has for it.
type RunLine struct {
	At   time.Time
	Text string
}

const runStarted = "=== Backup run started"

var (
	runFinishedLine = regexp.MustCompile(`=== Backup run finished: (ok|failed) \((.+)\)`)
	runStreamName   = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}_\d{2}-\d{2}-\d{2})_(.+)_(nightly|manual)$`)
)

// A run without its "finished" line whose log hasn't moved for this long was
// killed before it could finish (run.sh sends its log after every database).
const runStaleAfter = 30 * time.Minute

func parseRunStream(name string) (BackupRunLog, bool) {
	m := runStreamName.FindStringSubmatch(name)
	if m == nil {
		return BackupRunLog{}, false
	}
	started, err := time.ParseInLocation("2006-01-02_15-04-05", m[1], time.UTC)
	if err != nil {
		return BackupRunLog{}, false
	}
	return BackupRunLog{Stream: name, Started: started, Scope: m[2], Trigger: m[3]}, true
}

// runResult reads a run's result from its last line.
func runResult(lastLine string, last time.Time, now time.Time) string {
	if m := runFinishedLine.FindStringSubmatch(lastLine); m != nil {
		return m[1]
	}
	if now.Sub(last) < runStaleAfter {
		return "running"
	}
	return "interrupted"
}

// RecentRuns returns the latest n runs, newest first. Stream names start with
// the run's start time, so sorting by name sorts by time.
func (b *Backups) RecentRuns(ctx context.Context, n int) ([]BackupRunLog, error) {
	if b.runsGroup == "" {
		return nil, nil
	}
	out, err := b.logs.DescribeLogStreams(ctx, &cloudwatchlogs.DescribeLogStreamsInput{
		LogGroupName: aws.String(b.runsGroup),
		OrderBy:      cwtypes.OrderByLogStreamName,
		Descending:   aws.Bool(true),
		Limit:        aws.Int32(int32(n)),
	})
	if err != nil {
		var notFound *cwtypes.ResourceNotFoundException
		if errors.As(err, &notFound) {
			return nil, nil
		}
		return nil, err
	}
	var runs []BackupRunLog
	for _, st := range out.LogStreams {
		run, ok := parseRunStream(aws.ToString(st.LogStreamName))
		if !ok {
			continue
		}
		run.ConsoleURL = b.ConsoleURL(run.Stream)
		run.Last = run.Started
		if t := aws.ToInt64(st.LastEventTimestamp); t > 0 {
			run.Last = time.UnixMilli(t)
		}
		runs = append(runs, run)
	}

	// Each run's last line says how it ended.
	var wg sync.WaitGroup
	errs := make([]error, len(runs))
	for i := range runs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ev, err := b.logs.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{
				LogGroupName:  aws.String(b.runsGroup),
				LogStreamName: aws.String(runs[i].Stream),
				StartFromHead: aws.Bool(false),
				Limit:         aws.Int32(1),
			})
			if err != nil {
				errs[i] = err
				return
			}
			last := ""
			if len(ev.Events) > 0 {
				e := ev.Events[len(ev.Events)-1]
				last = aws.ToString(e.Message)
				runs[i].Last = time.UnixMilli(aws.ToInt64(e.Timestamp))
			}
			runs[i].Result = runResult(last, runs[i].Last, time.Now())
		}()
	}
	wg.Wait()
	return runs, errors.Join(errs...)
}

// RunLog returns one run with its whole log.
func (b *Backups) RunLog(ctx context.Context, stream string) (BackupRunLog, error) {
	run, ok := parseRunStream(stream)
	if !ok || b.runsGroup == "" {
		return BackupRunLog{}, errRunNotFound
	}
	in := &cloudwatchlogs.GetLogEventsInput{
		LogGroupName:  aws.String(b.runsGroup),
		LogStreamName: aws.String(stream),
		StartFromHead: aws.Bool(true),
	}
	var lines []RunLine
	last := ""
	run.ConsoleURL = b.ConsoleURL(stream)
	run.Last = run.Started
	for page := 0; page < 20; page++ {
		out, err := b.logs.GetLogEvents(ctx, in)
		if err != nil {
			var notFound *cwtypes.ResourceNotFoundException
			if errors.As(err, &notFound) {
				return BackupRunLog{}, errRunNotFound
			}
			return BackupRunLog{}, err
		}
		for _, e := range out.Events {
			last = strings.TrimRight(aws.ToString(e.Message), "\n")
			run.Last = time.UnixMilli(aws.ToInt64(e.Timestamp))
			lines = append(lines, RunLine{At: run.Last, Text: last})
		}
		// The same token back means the end of the stream.
		if len(out.Events) == 0 || aws.ToString(out.NextForwardToken) == aws.ToString(in.NextToken) {
			break
		}
		in.NextToken = out.NextForwardToken
	}
	run.Result = runResult(last, run.Last, time.Now())
	run.Lines = lines
	return run, nil
}

var errRunNotFound = errors.New("backup run not found")

// ConsoleURL links to a run's stream in the CloudWatch console, which
// escapes the group and stream names twice, with "$" instead of "%".
func (b *Backups) ConsoleURL(stream string) string {
	esc := func(s string) string { return strings.ReplaceAll(url.QueryEscape(url.QueryEscape(s)), "%", "$") }
	return fmt.Sprintf("https://%[1]s.console.aws.amazon.com/cloudwatch/home?region=%[1]s#logsV2:log-groups/log-group/%s/log-events/%s",
		b.region, esc(b.runsGroup), esc(stream))
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
	// run.sh sends its log to CloudWatch itself, tagged as a manual run.
	cmd.Env = append(os.Environ(), "BACKUP_TRIGGER=manual")
	// On the hour timeout, SIGTERM first: run.sh still sends its log.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = time.Minute
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
