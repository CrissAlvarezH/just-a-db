package main

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"
)

type App struct {
	cfg     Config
	db      *DB
	auth    *Auth
	backups *Backups
	system  *System
	pages   map[string]*template.Template
}

// Page is what every template gets.
type Page struct {
	Title   string
	Email   string
	CSRF    string
	Nav     string
	Error   string
	Notice  string
	Backups bool
	// Path of the current page without its query (e.g. a one-time ?notice=),
	// for the Actualizar button.
	Path string
	Data any
}

type ctxKey struct{}

func NewApp(cfg Config, db *DB, auth *Auth, backups *Backups, system *System) *App {
	funcs := template.FuncMap{
		"bytes": humanBytes,
		"time": func(t time.Time) string {
			if t.IsZero() {
				return ""
			}
			return t.In(cfg.Location).Format("2006-01-02 15:04")
		},
		"join":   strings.Join,
		"clock":  func() string { return time.Now().In(cfg.Location).Format("15:04:05") },
		"nowMs":  func() int64 { return time.Now().UnixMilli() },
		"ubytes": func(n uint64) string { return humanBytes(int64(n)) },
		"pct": func(used, total uint64) int {
			if total == 0 {
				return 0
			}
			return int(100 * used / total)
		},
		// Bar color: amber from 75%, red from 90%.
		"level": func(p int) string {
			switch {
			case p >= 90:
				return "high"
			case p >= 75:
				return "mid"
			}
			return "low"
		},
		"loadpct": func(load float64, cpus int) int {
			if cpus == 0 {
				return 0
			}
			return min(100, int(100*load/float64(cpus)))
		},
		"duration": func(d time.Duration) string {
			days := int(d.Hours()) / 24
			if days > 0 {
				return fmt.Sprintf("%d d %d h", days, int(d.Hours())%24)
			}
			return fmt.Sprintf("%d h %d min", int(d.Hours()), int(d.Minutes())%60)
		},
		"f1":         func(f float64) string { return fmt.Sprintf("%.2f", f) },
		"int":        func(f float64) int { return int(f) },
		"sub":        func(a, b uint64) uint64 { return a - b },
		"restorable": restorableBackup,
		"ago": func(t time.Time) string {
			d := time.Since(t)
			switch {
			case d < time.Minute:
				return "hace un momento"
			case d < time.Hour:
				return fmt.Sprintf("hace %d min", int(d.Minutes()))
			case d < 48*time.Hour:
				return fmt.Sprintf("hace %d h", int(d.Hours()))
			}
			return fmt.Sprintf("hace %d días", int(d.Hours()/24))
		},
		// Older than a day and a bit: the nightly backup isn't covering it.
		"stale": func(t time.Time) bool { return time.Since(t) > 26*time.Hour },
	}
	pages := map[string]*template.Template{}
	for _, name := range []string{"login", "verify", "databases", "new", "created", "delete", "backups", "folder", "user"} {
		pages[name] = template.Must(template.New("").Funcs(funcs).ParseFS(assets,
			"templates/layout.html", "templates/partials.html", "templates/"+name+".html"))
	}
	return &App{cfg: cfg, db: db, auth: auth, backups: backups, system: system, pages: pages}
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func (a *App) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.FileServerFS(assets))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })

	mux.HandleFunc("GET /login", a.loginPage)
	mux.HandleFunc("POST /login", a.sendCode)
	mux.HandleFunc("POST /login/verify", a.verifyCode)
	mux.HandleFunc("POST /logout", a.authed(a.logout))

	mux.HandleFunc("GET /{$}", a.authed(a.databasesPage))
	mux.HandleFunc("GET /databases/new", a.authed(a.newDatabasePage))
	mux.HandleFunc("POST /databases", a.authed(a.createDatabase))
	mux.HandleFunc("POST /databases/{name}/restrict", a.authed(a.restrictDatabase))
	mux.HandleFunc("GET /databases/{name}/delete", a.authed(a.deletePage))
	mux.HandleFunc("POST /databases/{name}/delete", a.authed(a.deleteDatabase))
	mux.HandleFunc("GET /users/new", a.authed(a.newUserPage))
	mux.HandleFunc("POST /users", a.authed(a.createUser))
	mux.HandleFunc("POST /roles/{name}/password", a.authed(a.resetPassword))

	mux.HandleFunc("GET /backups", a.authed(a.backupsPage))
	mux.HandleFunc("GET /backups/f/{folder}", a.authed(a.folderPage))
	mux.HandleFunc("GET /backups/download", a.authed(a.download))
	mux.HandleFunc("POST /backups/run", a.authed(a.runBackup))

	return securityHeaders(sameOrigin(mux))
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		// Not "no-referrer": with it browsers send "Origin: null" on form posts,
		// which sameOrigin rejects.
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// sameOrigin rejects cross-site POSTs (on top of SameSite cookies and the
// CSRF token).
func sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || u.Host != r.Host {
					http.Error(w, "origen no permitido", http.StatusForbidden)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) authed(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s := a.auth.Session(r)
		if s == nil {
			if r.Method == http.MethodGet {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
			} else {
				http.Error(w, "sesión vencida", http.StatusUnauthorized)
			}
			return
		}
		if r.Method == http.MethodPost && r.PostFormValue("csrf") != s.csrf {
			http.Error(w, "token CSRF inválido, recarga la página", http.StatusForbidden)
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, s)))
	}
}

func (a *App) render(w http.ResponseWriter, r *http.Request, name string, status int, p Page) {
	if s, ok := r.Context().Value(ctxKey{}).(*session); ok {
		p.Email, p.CSRF = s.email, s.csrf
	}
	p.Backups = a.backups != nil
	p.Path = r.URL.Path
	var buf bytes.Buffer
	if err := a.pages[name].ExecuteTemplate(&buf, "layout", p); err != nil {
		log.Printf("render %s: %v", name, err)
		http.Error(w, "error interno", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	buf.WriteTo(w)
}

// --- Login ---

func (a *App) loginPage(w http.ResponseWriter, r *http.Request) {
	if a.auth.Session(r) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	a.render(w, r, "login", http.StatusOK, Page{Title: "Entrar"})
}

func (a *App) sendCode(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(strings.TrimSpace(r.PostFormValue("email")))
	if !a.auth.allowIP(a.auth.clientIP(r)) {
		a.render(w, r, "login", http.StatusTooManyRequests, Page{Title: "Entrar", Error: "Demasiados intentos, espera unos minutos."})
		return
	}
	if email == "" || !strings.Contains(email, "@") {
		a.render(w, r, "login", http.StatusBadRequest, Page{Title: "Entrar", Error: "Escribe un email válido."})
		return
	}
	if err := a.auth.IssueCode(r.Context(), email); err != nil {
		log.Printf("send code: %v", err)
		a.render(w, r, "login", http.StatusBadGateway, Page{Title: "Entrar", Error: "No se pudo enviar el correo, intenta de nuevo."})
		return
	}
	a.render(w, r, "verify", http.StatusOK, Page{Title: "Código", Data: email})
}

func (a *App) verifyCode(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(strings.TrimSpace(r.PostFormValue("email")))
	if !a.auth.allowIP(a.auth.clientIP(r)) {
		a.render(w, r, "verify", http.StatusTooManyRequests, Page{Title: "Código", Data: email, Error: "Demasiados intentos, espera unos minutos."})
		return
	}
	token, ok := a.auth.VerifyCode(email, r.PostFormValue("code"))
	if !ok {
		a.render(w, r, "verify", http.StatusUnauthorized, Page{Title: "Código", Data: email, Error: "Código inválido o vencido."})
		return
	}
	log.Printf("login: %s", email)
	a.auth.SetCookie(w, token, int(sessionTTL.Seconds()))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	a.auth.Logout(r)
	a.auth.SetCookie(w, "", -1)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// --- Databases ---

type databasesData struct {
	System    SystemStats
	Server    ServerInfo
	DataSize  int64
	Databases []Database
	Roles     []Role
	SuperUser string
	DefaultDB string
}

func (a *App) databasesPage(w http.ResponseWriter, r *http.Request) {
	a.renderDatabases(w, r, http.StatusOK, r.URL.Query().Get("notice"), "")
}

func (a *App) renderDatabases(w http.ResponseWriter, r *http.Request, status int, notice, errMsg string) {
	ctx := r.Context()
	data := databasesData{SuperUser: a.cfg.SuperUser, DefaultDB: a.cfg.DefaultDB, System: a.system.Stats()}
	var err error
	if data.Server, err = a.db.ServerInfo(ctx); err == nil {
		if data.Databases, err = a.db.Databases(ctx); err == nil {
			data.Roles, err = a.db.Roles(ctx)
		}
	}
	for _, d := range data.Databases {
		data.DataSize += d.Size
	}
	if err != nil {
		log.Printf("databases: %v", err)
		errMsg = "No se pudo consultar Postgres: " + err.Error()
	}
	a.render(w, r, "databases", status, Page{Title: "Bases de datos", Nav: "databases", Notice: notice, Error: errMsg, Data: data})
}

func redirectNotice(w http.ResponseWriter, r *http.Request, to, notice string) {
	http.Redirect(w, r, to+"?notice="+url.QueryEscape(notice), http.StatusSeeOther)
}

type credentials struct {
	Title    string
	Host     string
	Port     string
	User     string
	Password string
	Database string
	URL      string
	// The same as DB_* lines, to paste into a .env or Vercel's import.
	Env string
}

func (a *App) credentialsFor(title, user, password, database string) credentials {
	u := url.URL{
		Scheme:   "postgresql",
		User:     url.UserPassword(user, password),
		Host:     a.cfg.PublicDBHost + ":" + a.cfg.PublicDBPort,
		Path:     "/" + database,
		RawQuery: "sslmode=require",
	}
	env := strings.Join([]string{
		"DB_HOST=" + a.cfg.PublicDBHost,
		"DB_PORT=" + a.cfg.PublicDBPort,
		"DB_USER=" + user,
		"DB_PASS=" + password,
		"DB_NAME=" + database,
		"DB_SSLMODE=require",
	}, "\n")
	return credentials{Title: title, Host: a.cfg.PublicDBHost, Port: a.cfg.PublicDBPort, User: user,
		Password: password, Database: database, URL: u.String(), Env: env}
}

// Restores run inside the request (the new password is only shown in its
// response), so they get longer than the server's WriteTimeout.
const restoreTimeout = 30 * time.Minute

type newDatabaseData struct {
	Name       string
	Backup     string // S3 key to restore from, "" for an empty database
	Choices    []backupGroup
	Creds      *credentials
	RestoreLog string
}

func (a *App) renderNew(w http.ResponseWriter, r *http.Request, status int, data newDatabaseData, errMsg string) {
	if data.Creds == nil {
		choices, err := a.restoreChoices(r.Context(), 10)
		if err != nil && errMsg == "" {
			errMsg = "No se pudo listar los backups de S3: " + err.Error()
		}
		data.Choices = choices
	}
	title := "Nueva base de datos"
	if data.Creds != nil {
		title = "Base creada"
	}
	a.render(w, r, "new", status, Page{Title: title, Nav: "databases", Error: errMsg, Data: data})
}

func (a *App) newDatabasePage(w http.ResponseWriter, r *http.Request) {
	// ?backup=<key> preselects it (the link on a backups folder page).
	a.renderNew(w, r, http.StatusOK, newDatabaseData{Backup: r.URL.Query().Get("backup")}, "")
}

// createDatabase answers on the same page: the form again with the error, or
// the new credentials.
func (a *App) createDatabase(w http.ResponseWriter, r *http.Request) {
	data := newDatabaseData{
		Name:   strings.TrimSpace(r.PostFormValue("name")),
		Backup: r.PostFormValue("backup"),
	}
	if data.Backup != "" && (a.backups == nil || !restorableBackup(data.Backup)) {
		a.renderNew(w, r, http.StatusBadRequest, data, "Ese backup no se puede restaurar.")
		return
	}
	password, err := a.db.CreateDatabase(r.Context(), data.Name)
	if err != nil {
		a.renderNew(w, r, http.StatusBadRequest, data, err.Error())
		return
	}
	log.Printf("database %q created by %s", data.Name, a.email(r))

	title := "Base de datos " + data.Name + " creada"
	if data.Backup != "" {
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(restoreTimeout + time.Minute))
		ctx, cancel := context.WithTimeout(r.Context(), restoreTimeout)
		defer cancel()
		out, err := a.RestoreBackup(ctx, data.Backup, data.Name, data.Name)
		if err != nil {
			// Brand new and half restored: nothing to keep.
			if _, dropErr := a.db.DropDatabase(context.Background(), data.Name, true); dropErr != nil {
				log.Printf("drop %q after failed restore: %v", data.Name, dropErr)
			}
			log.Printf("restore of %q into %q failed: %v", data.Backup, data.Name, err)
			data.RestoreLog = out
			a.renderNew(w, r, http.StatusBadRequest, data,
				fmt.Sprintf("No se pudo restaurar el backup (%v), así que no se creó la base. Revisa el log abajo.", err))
			return
		}
		log.Printf("backup %q restored into %q by %s", data.Backup, data.Name, a.email(r))
		data.RestoreLog = out
		title = "Base de datos " + data.Name + " creada desde " + path.Base(data.Backup)
	}
	creds := a.credentialsFor(title, data.Name, password, data.Name)
	data.Creds = &creds
	a.renderNew(w, r, http.StatusOK, data, "")
}

type newUserData struct {
	Name      string
	ReadOnly  bool
	Selected  map[string]bool
	Databases []Database
	Creds     *credentials
	// The databases it got, when more than one (the credentials use the first).
	Granted []string
}

func (a *App) renderNewUser(w http.ResponseWriter, r *http.Request, status int, data newUserData, errMsg string) {
	if data.Creds == nil {
		dbs, err := a.db.Databases(r.Context())
		if err != nil && errMsg == "" {
			errMsg = "No se pudo consultar Postgres: " + err.Error()
		}
		data.Databases = dbs
	}
	title := "Nuevo usuario"
	if data.Creds != nil {
		title = "Usuario creado"
	}
	a.render(w, r, "user", status, Page{Title: title, Nav: "databases", Error: errMsg, Data: data})
}

func (a *App) newUserPage(w http.ResponseWriter, r *http.Request) {
	a.renderNewUser(w, r, http.StatusOK, newUserData{}, "")
}

func (a *App) createUser(w http.ResponseWriter, r *http.Request) {
	data := newUserData{
		Name:     strings.TrimSpace(r.PostFormValue("name")),
		ReadOnly: r.PostFormValue("access") == "read",
		Selected: map[string]bool{},
	}
	databases := r.PostForm["databases"]
	sort.Strings(databases)
	for _, db := range databases {
		data.Selected[db] = true
	}
	password, err := a.db.CreateUser(r.Context(), data.Name, databases, data.ReadOnly)
	if err != nil {
		a.renderNewUser(w, r, http.StatusBadRequest, data, err.Error())
		return
	}
	access := "lectura y escritura"
	if data.ReadOnly {
		access = "solo lectura"
	}
	log.Printf("user %q created by %s with %s on %s", data.Name, a.email(r), access, strings.Join(databases, ", "))
	creds := a.credentialsFor("Usuario "+data.Name+" creado ("+access+")", data.Name, password, databases[0])
	data.Creds = &creds
	if len(databases) > 1 {
		data.Granted = databases
	}
	a.renderNewUser(w, r, http.StatusOK, data, "")
}

func (a *App) resetPassword(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	password, err := a.db.ResetPassword(r.Context(), name)
	if err != nil {
		a.renderDatabases(w, r, http.StatusBadRequest, "", err.Error())
		return
	}
	log.Printf("password of %q reset by %s", name, a.email(r))
	database := r.PostFormValue("database")
	if database == "" {
		database = name
	}
	a.render(w, r, "created", http.StatusOK, Page{Title: "Nueva contraseña", Nav: "databases",
		Data: a.credentialsFor("Nueva contraseña de "+name, name, password, database)})
}

func (a *App) restrictDatabase(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := a.db.RestrictDatabase(r.Context(), name); err != nil {
		a.renderDatabases(w, r, http.StatusBadRequest, "", err.Error())
		return
	}
	log.Printf("database %q restricted by %s", name, a.email(r))
	redirectNotice(w, r, "/", fmt.Sprintf("Ahora solo el dueño de %s (y el superusuario) pueden conectarse.", name))
}

// A database can only be dropped right after a backup of it: one taken at
// most this long ago.
const deleteBackupMaxAge = 10 * time.Minute

type deleteData struct {
	Name        string
	LastBackup  *BackupObject
	BackupFresh bool // LastBackup is recent enough to delete
	NoS3        bool
	MaxAgeMin   int
	Run         BackupRun // the panel's current/last manual backup
	RunningThis bool      // a backup of this database is running now
}

// deleteState checks the database's latest backup in S3.
func (a *App) deleteState(ctx context.Context, name string) (deleteData, error) {
	data := deleteData{Name: name, MaxAgeMin: int(deleteBackupMaxAge.Minutes())}
	if a.backups == nil {
		data.NoS3 = true
		return data, nil
	}
	data.Run = a.backups.Status()
	data.RunningThis = data.Run.Running && data.Run.Database == name
	latest, err := a.backups.Latest(ctx, name)
	if err != nil {
		return data, err
	}
	data.LastBackup = latest
	data.BackupFresh = latest != nil && time.Since(latest.Modified) <= deleteBackupMaxAge
	return data, nil
}

func (a *App) renderDelete(w http.ResponseWriter, r *http.Request, status int, name, errMsg string) {
	data, err := a.deleteState(r.Context(), name)
	if err != nil && errMsg == "" {
		errMsg = "No se pudo revisar los backups en S3: " + err.Error()
	}
	a.render(w, r, "delete", status, Page{Title: "Borrar " + name, Nav: "databases",
		Notice: r.URL.Query().Get("notice"), Error: errMsg, Data: data})
}

func (a *App) deletePage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if exists, err := a.db.databaseExists(r.Context(), name); err == nil && !exists {
		redirectNotice(w, r, "/", "La base de datos "+name+" no existe.")
		return
	}
	a.renderDelete(w, r, http.StatusOK, name, "")
}

func (a *App) deleteDatabase(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if r.PostFormValue("confirm") != name {
		a.renderDelete(w, r, http.StatusBadRequest, name, "El nombre no coincide.")
		return
	}
	// Checked again here, not only on the page: the backup may have aged
	// while it was open.
	state, err := a.deleteState(r.Context(), name)
	switch {
	case err != nil:
		a.renderDelete(w, r, http.StatusBadGateway, name, "No se pudo revisar los backups en S3: "+err.Error())
		return
	case state.NoS3:
		a.renderDelete(w, r, http.StatusBadRequest, name, "No hay S3 configurado para backups, así que no se puede borrar.")
		return
	case !state.BackupFresh:
		a.renderDelete(w, r, http.StatusBadRequest, name,
			fmt.Sprintf("Para borrarla necesita un backup de hace %d minutos o menos.", state.MaxAgeMin))
		return
	}
	extra, err := a.db.DropDatabase(r.Context(), name, r.PostFormValue("drop_owner") == "on")
	if err != nil {
		a.renderDelete(w, r, http.StatusBadRequest, name, err.Error())
		return
	}
	log.Printf("database %q dropped by %s", name, a.email(r))
	redirectNotice(w, r, "/", strings.TrimSpace("Se borró "+name+". "+extra))
}

func (a *App) email(r *http.Request) string {
	if s, ok := r.Context().Value(ctxKey{}).(*session); ok {
		return s.email
	}
	return "?"
}

// --- Backups ---

type backupsData struct {
	Databases []backupSummary
	Globals   *backupSummary
	Run       BackupRun
	Recent    []BackupRunLog
	RecentErr string
}

// backupSummary is one row of the backups page: a database (existing or
// dropped) and its backups in S3.
type backupSummary struct {
	Name   string
	Exists bool
	Count  int
	Latest *BackupObject
}

func (a *App) backupsPage(w http.ResponseWriter, r *http.Request) {
	if a.backups == nil {
		http.NotFound(w, r)
		return
	}
	data := backupsData{Run: a.backups.Status()}
	errMsg, err := a.backupSummaries(r.Context(), &data)
	if err != nil {
		log.Printf("backups: %v", err)
	}
	if data.Recent, err = a.backups.RecentRuns(r.Context(), 3); err != nil {
		log.Printf("backup runs: %v", err)
		data.RecentErr = "No se pudieron leer las corridas de CloudWatch: " + err.Error()
	}
	a.render(w, r, "backups", http.StatusOK, Page{Title: "Backups", Nav: "backups",
		Notice: r.URL.Query().Get("notice"), Error: errMsg, Data: data})
}

// backupSummaries fills one row per database in Postgres or folder in S3.
func (a *App) backupSummaries(ctx context.Context, data *backupsData) (string, error) {
	rows := map[string]*backupSummary{}
	var names []string
	add := func(name string) *backupSummary {
		if rows[name] == nil {
			rows[name] = &backupSummary{Name: name}
			names = append(names, name)
		}
		return rows[name]
	}
	dbs, err := a.db.Databases(ctx)
	if err != nil {
		return "No se pudo consultar Postgres: " + err.Error(), err
	}
	for _, d := range dbs {
		add(d.Name).Exists = true
	}
	folders, err := a.backups.Folders(ctx)
	if err != nil {
		return "No se pudo listar S3: " + err.Error(), err
	}
	for _, f := range folders {
		list, err := a.backups.List(ctx, f)
		if err != nil {
			return "No se pudo listar S3: " + err.Error(), err
		}
		if strings.HasPrefix(f, "_") && f != "_globals" {
			continue // _logs
		}
		row := &backupSummary{Name: f}
		if f != "_globals" {
			row = add(f)
		}
		row.Count = len(list)
		if len(list) > 0 {
			row.Latest = &list[0]
		}
		if f == "_globals" {
			data.Globals = row
		}
	}
	sort.Strings(names)
	for _, n := range names {
		data.Databases = append(data.Databases, *rows[n])
	}
	return "", nil
}

type folderData struct {
	Folder  string
	Objects []BackupObject
}

func (a *App) folderPage(w http.ResponseWriter, r *http.Request) {
	if a.backups == nil {
		http.NotFound(w, r)
		return
	}
	data := folderData{Folder: r.PathValue("folder")}
	var errMsg string
	objects, err := a.backups.List(r.Context(), data.Folder)
	if err != nil {
		errMsg = "No se pudo listar S3: " + err.Error()
	}
	data.Objects = objects
	a.render(w, r, "folder", http.StatusOK, Page{Title: "Backups de " + data.Folder, Nav: "backups", Error: errMsg, Data: data})
}

func (a *App) download(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if a.backups == nil || key == "" || strings.Contains(key, "..") {
		http.NotFound(w, r)
		return
	}
	link, err := a.backups.DownloadURL(r.Context(), key)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	log.Printf("backup %q downloaded by %s", key, a.email(r))
	http.Redirect(w, r, link, http.StatusSeeOther)
}

func (a *App) runBackup(w http.ResponseWriter, r *http.Request) {
	if a.backups == nil {
		http.NotFound(w, r)
		return
	}
	// With `database`, back up only that one and go back to its delete page.
	database := r.PostFormValue("database")
	back := "/backups"
	if database != "" {
		exists, err := a.db.databaseExists(r.Context(), database)
		if err != nil || !exists {
			redirectNotice(w, r, "/", "La base de datos "+database+" no existe.")
			return
		}
		back = "/databases/" + url.PathEscape(database) + "/delete"
	}
	notice := "Ya hay un backup corriendo, espera a que termine."
	if a.backups.Run(database) {
		log.Printf("backup of %q started by %s", cmp.Or(database, "every database"), a.email(r))
		notice = "Backup iniciado."
	}
	redirectNotice(w, r, back, notice)
}
