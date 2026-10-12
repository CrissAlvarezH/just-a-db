package main

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type App struct {
	cfg     Config
	db      *DB
	auth    *Auth
	backups *Backups
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
	Data    any
}

type ctxKey struct{}

func NewApp(cfg Config, db *DB, auth *Auth, backups *Backups) *App {
	funcs := template.FuncMap{
		"bytes": humanBytes,
		"time": func(t time.Time) string {
			if t.IsZero() {
				return ""
			}
			return t.In(cfg.Location).Format("2006-01-02 15:04")
		},
		"join": strings.Join,
	}
	pages := map[string]*template.Template{}
	for _, name := range []string{"login", "verify", "databases", "created", "delete", "backups", "folder"} {
		pages[name] = template.Must(template.New("").Funcs(funcs).ParseFS(assets,
			"templates/layout.html", "templates/"+name+".html"))
	}
	return &App{cfg: cfg, db: db, auth: auth, backups: backups, pages: pages}
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
	mux.HandleFunc("POST /databases", a.authed(a.createDatabase))
	mux.HandleFunc("POST /databases/{name}/restrict", a.authed(a.restrictDatabase))
	mux.HandleFunc("GET /databases/{name}/delete", a.authed(a.deletePage))
	mux.HandleFunc("POST /databases/{name}/delete", a.authed(a.deleteDatabase))
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
		h.Set("Referrer-Policy", "no-referrer")
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
	Server    ServerInfo
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
	data := databasesData{SuperUser: a.cfg.SuperUser, DefaultDB: a.cfg.DefaultDB}
	var err error
	if data.Server, err = a.db.ServerInfo(ctx); err == nil {
		if data.Databases, err = a.db.Databases(ctx); err == nil {
			data.Roles, err = a.db.Roles(ctx)
		}
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
	User     string
	Password string
	Database string
	URL      string
}

func (a *App) credentialsFor(title, user, password, database string) credentials {
	u := url.URL{
		Scheme:   "postgresql",
		User:     url.UserPassword(user, password),
		Host:     a.cfg.PublicDBHost + ":" + a.cfg.PublicDBPort,
		Path:     "/" + database,
		RawQuery: "sslmode=require",
	}
	return credentials{Title: title, User: user, Password: password, Database: database, URL: u.String()}
}

func (a *App) createDatabase(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PostFormValue("name"))
	password, err := a.db.CreateDatabase(r.Context(), name)
	if err != nil {
		a.renderDatabases(w, r, http.StatusBadRequest, "", err.Error())
		return
	}
	log.Printf("database %q created by %s", name, a.email(r))
	a.render(w, r, "created", http.StatusOK, Page{Title: "Base creada", Nav: "databases",
		Data: a.credentialsFor("Base de datos "+name+" creada", name, password, name)})
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

type deleteData struct {
	Name       string
	LastBackup *BackupObject
}

func (a *App) deletePage(w http.ResponseWriter, r *http.Request) {
	data := deleteData{Name: r.PathValue("name")}
	if a.backups != nil {
		if list, err := a.backups.List(r.Context(), data.Name); err == nil && len(list) > 0 {
			data.LastBackup = &list[0]
		}
	}
	a.render(w, r, "delete", http.StatusOK, Page{Title: "Borrar " + data.Name, Nav: "databases", Data: data})
}

func (a *App) deleteDatabase(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if r.PostFormValue("confirm") != name {
		a.render(w, r, "delete", http.StatusBadRequest, Page{Title: "Borrar " + name, Nav: "databases",
			Error: "El nombre no coincide.", Data: deleteData{Name: name}})
		return
	}
	extra, err := a.db.DropDatabase(r.Context(), name, r.PostFormValue("drop_owner") == "on")
	if err != nil {
		a.render(w, r, "delete", http.StatusBadRequest, Page{Title: "Borrar " + name, Nav: "databases",
			Error: err.Error(), Data: deleteData{Name: name}})
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
	Folders []string
	Run     BackupRun
}

func (a *App) backupsPage(w http.ResponseWriter, r *http.Request) {
	if a.backups == nil {
		http.NotFound(w, r)
		return
	}
	data := backupsData{Run: a.backups.Status()}
	var errMsg string
	folders, err := a.backups.Folders(r.Context())
	if err != nil {
		log.Printf("backups: %v", err)
		errMsg = "No se pudo listar S3: " + err.Error()
	}
	data.Folders = folders
	a.render(w, r, "backups", http.StatusOK, Page{Title: "Backups", Nav: "backups",
		Notice: r.URL.Query().Get("notice"), Error: errMsg, Data: data})
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
	notice := "Ya hay un backup corriendo."
	if a.backups.Run() {
		log.Printf("backup started by %s", a.email(r))
		notice = "Backup iniciado. Recarga la página para ver el progreso."
	}
	redirectNotice(w, r, "/backups", notice)
}
