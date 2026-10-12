package main

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const backupLogLines = 200

type Backups struct {
	bucket  string
	script  string
	client  *s3.Client
	presign *s3.PresignClient

	mu       sync.Mutex
	running  bool
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

// Run starts backups/run.sh (the same script the nightly cron runs) unless
// one is already running.
func (b *Backups) Run() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.running {
		return false
	}
	b.running = true
	b.started = time.Now()
	b.finished = time.Time{}
	b.logLines = nil
	go b.run()
	return true
}

func (b *Backups) run() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	cmd := exec.CommandContext(ctx, b.script)
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
	b.mu.Lock()
	defer b.mu.Unlock()
	b.logLines = append(b.logLines, line)
	if len(b.logLines) > backupLogLines {
		b.logLines = b.logLines[len(b.logLines)-backupLogLines:]
	}
}

func (b *Backups) Status() BackupRun {
	b.mu.Lock()
	defer b.mu.Unlock()
	return BackupRun{
		Running:  b.running,
		Started:  b.started,
		Finished: b.finished,
		OK:       b.ok,
		Log:      strings.Join(b.logLines, "\n"),
	}
}
