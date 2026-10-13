// Command panel is a small admin UI for just-a-db: list databases and roles,
// create a database with its own user, reset passwords, browse and download
// backups from S3, and run a backup on demand. Login is by a one-time code
// sent by email to the addresses in ALLOWED_EMAILS.
package main

import (
	"context"
	"crypto/sha256"
	"embed"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // TZ works in a scratch/alpine image without tzdata.
)

//go:embed templates static
var assets embed.FS

type Config struct {
	Addr          string
	DatabaseURL   string
	DefaultDB     string
	SuperUser     string
	SuperPassword string
	DBHost        string
	DBPort        string
	AllowedEmails map[string]bool
	BrevoAPIKey   string
	MailFrom      string
	MailFromName  string
	DevLogCodes   bool
	SecureCookies bool
	TrustProxy    bool
	PublicDBHost  string
	PublicDBPort  string
	SessionKey    []byte
	S3Bucket      string
	S3PathStyle   bool
	BackupScript  string
	DiskPath      string
	Location      *time.Location
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func loadConfig() Config {
	user := env("POSTGRES_USER", "")
	pass := os.Getenv("POSTGRES_PASSWORD")
	if user == "" || pass == "" {
		log.Fatal("POSTGRES_USER and POSTGRES_PASSWORD are required")
	}
	dbURL := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, pass),
		Host:     net.JoinHostPort(env("DB_HOST", "database"), env("DB_PORT", "5432")),
		Path:     "/postgres",
		RawQuery: "sslmode=disable",
	}

	allowed := map[string]bool{}
	for _, e := range strings.Split(os.Getenv("ALLOWED_EMAILS"), ",") {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			allowed[e] = true
		}
	}
	if len(allowed) == 0 {
		log.Fatal("ALLOWED_EMAILS is empty: nobody could log in")
	}

	loc, err := time.LoadLocation(env("TZ", "UTC"))
	if err != nil {
		log.Fatalf("TZ: %v", err)
	}

	cfg := Config{
		Addr:          env("PANEL_ADDR", ":8080"),
		DatabaseURL:   dbURL.String(),
		DefaultDB:     env("POSTGRES_DB", ""),
		SuperUser:     user,
		SuperPassword: pass,
		DBHost:        env("DB_HOST", "database"),
		DBPort:        env("DB_PORT", "5432"),
		AllowedEmails: allowed,
		BrevoAPIKey:   env("BREVO_API_KEY", ""),
		MailFrom:      env("MAIL_FROM", ""),
		MailFromName:  env("MAIL_FROM_NAME", "just-a-db"),
		DevLogCodes:   env("DEV_LOG_CODES", "") == "true",
		SecureCookies: env("SECURE_COOKIES", "true") != "false",
		TrustProxy:    env("TRUST_PROXY", "true") != "false",
		PublicDBHost:  env("PUBLIC_DB_HOST", "localhost"),
		PublicDBPort:  env("PUBLIC_DB_PORT", "6432"),
		SessionKey:    sessionKey(pass),
		S3Bucket:      env("S3_BUCKET", ""),
		S3PathStyle:   env("S3_FORCE_PATH_STYLE", "") == "true",
		BackupScript:  env("BACKUP_SCRIPT", "/app/run.sh"),
		DiskPath:      env("DISK_PATH", "/"),
		Location:      loc,
	}
	if !cfg.DevLogCodes && (cfg.BrevoAPIKey == "" || cfg.MailFrom == "") {
		log.Fatal("BREVO_API_KEY and MAIL_FROM are required to send login codes")
	}
	return cfg
}

// sessionKey signs session cookies: SESSION_SECRET if set, else derived from
// the Postgres password, so there's nothing else to configure. Changing it
// logs everyone out.
func sessionKey(dbPassword string) []byte {
	secret := os.Getenv("SESSION_SECRET")
	if secret == "" {
		secret = "just-a-db-panel/session/" + dbPassword
	}
	sum := sha256.Sum256([]byte(secret))
	return sum[:]
}

func main() {
	cfg := loadConfig()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := NewDB(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer db.Close()

	var backups *Backups
	if cfg.S3Bucket != "" {
		if backups, err = NewBackups(ctx, cfg); err != nil {
			log.Fatalf("s3: %v", err)
		}
	} else {
		log.Print("S3_BUCKET not set: backups page disabled")
	}

	app := NewApp(cfg, db, NewAuth(cfg), backups, NewSystem(cfg.DiskPath))
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           app.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	log.Printf("panel listening on %s (%d allowed emails)", cfg.Addr, len(cfg.AllowedEmails))
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(fmt.Errorf("listen: %w", err))
	}
}
