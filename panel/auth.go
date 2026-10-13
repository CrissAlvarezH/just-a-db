package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	codeTTL         = 10 * time.Minute
	codeMaxAttempts = 5
	codeResendAfter = time.Minute
	sessionTTL      = 12 * time.Hour
	// Login requests (send or verify) allowed per IP per window.
	ipLimit       = 20
	ipLimitWindow = 15 * time.Minute
	sessionCookie = "jad_session"
)

type session struct {
	email   string
	csrf    string
	expires time.Time
}

type pendingCode struct {
	hash     [32]byte
	expires  time.Time
	sentAt   time.Time
	attempts int
}

type ipCounter struct {
	count int
	reset time.Time
}

// Auth keeps login codes in memory, and sessions in a signed cookie
// (email|expiry|csrf + HMAC), so restarting or redeploying the panel doesn't
// log anyone out. Removing an email from ALLOWED_EMAILS ends its sessions.
type Auth struct {
	cfg    Config
	client *http.Client
	mu     sync.Mutex
	codes  map[string]*pendingCode
	ips    map[string]*ipCounter
	// Cookies logged out before they expire. In memory: after a restart a
	// copied cookie would work again until it expires (12h at most).
	revoked map[string]time.Time
}

func NewAuth(cfg Config) *Auth {
	a := &Auth{
		cfg:     cfg,
		client:  &http.Client{Timeout: 15 * time.Second},
		codes:   map[string]*pendingCode{},
		ips:     map[string]*ipCounter{},
		revoked: map[string]time.Time{},
	}
	go a.sweep()
	return a
}

func (a *Auth) sweep() {
	for range time.Tick(time.Minute) {
		now := time.Now()
		a.mu.Lock()
		for k, exp := range a.revoked {
			if now.After(exp) {
				delete(a.revoked, k)
			}
		}
		for k, c := range a.codes {
			if now.After(c.expires) {
				delete(a.codes, k)
			}
		}
		for k, c := range a.ips {
			if now.After(c.reset) {
				delete(a.ips, k)
			}
		}
		a.mu.Unlock()
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func (a *Auth) clientIP(r *http.Request) string {
	if a.cfg.TrustProxy {
		// Caddy appends the client's address as the last entry.
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// allowIP counts a login request from this IP and reports whether it's
// within the limit.
func (a *Auth) allowIP(ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	c := a.ips[ip]
	if c == nil || now.After(c.reset) {
		c = &ipCounter{reset: now.Add(ipLimitWindow)}
		a.ips[ip] = c
	}
	c.count++
	return c.count <= ipLimit
}

// IssueCode creates a code for an allowed email and sends it. Emails not in
// ALLOWED_EMAILS get no code but the same response, so the form doesn't tell
// which addresses are valid.
func (a *Auth) IssueCode(ctx context.Context, email string) error {
	if !a.cfg.AllowedEmails[email] {
		log.Printf("login code requested for a non-allowed email")
		return nil
	}
	now := time.Now()
	a.mu.Lock()
	if c := a.codes[email]; c != nil && now.Sub(c.sentAt) < codeResendAfter {
		a.mu.Unlock()
		return nil // One email per minute; the previous code still works.
	}
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		a.mu.Unlock()
		return err
	}
	code := fmt.Sprintf("%06d", n.Int64())
	a.codes[email] = &pendingCode{
		hash:    sha256.Sum256([]byte(code)),
		expires: now.Add(codeTTL),
		sentAt:  now,
	}
	a.mu.Unlock()

	if a.cfg.DevLogCodes {
		log.Printf("DEV_LOG_CODES: login code for %s is %s", email, code)
		return nil
	}
	if err := a.sendMail(ctx, email, code); err != nil {
		a.mu.Lock()
		delete(a.codes, email)
		a.mu.Unlock()
		return err
	}
	return nil
}

// VerifyCode checks a code and, if right, returns a new session token.
func (a *Auth) VerifyCode(email, code string) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	c := a.codes[email]
	if c == nil || time.Now().After(c.expires) {
		return "", false
	}
	c.attempts++
	got := sha256.Sum256([]byte(strings.TrimSpace(code)))
	if subtle.ConstantTimeCompare(got[:], c.hash[:]) != 1 {
		if c.attempts >= codeMaxAttempts {
			delete(a.codes, email)
		}
		return "", false
	}
	delete(a.codes, email)
	return a.sign(session{email: email, csrf: randomHex(16), expires: time.Now().Add(sessionTTL)}), true
}

func (a *Auth) mac(payload string) []byte {
	m := hmac.New(sha256.New, a.cfg.SessionKey)
	m.Write([]byte(payload))
	return m.Sum(nil)
}

func (a *Auth) sign(s session) string {
	payload := strings.Join([]string{s.email, strconv.FormatInt(s.expires.Unix(), 10), s.csrf}, "|")
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(payload)) + "." + enc.EncodeToString(a.mac(payload))
}

func (a *Auth) Session(r *http.Request) *session {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil
	}
	enc := base64.RawURLEncoding
	payloadB64, sigB64, ok := strings.Cut(cookie.Value, ".")
	if !ok {
		return nil
	}
	payload, err1 := enc.DecodeString(payloadB64)
	sig, err2 := enc.DecodeString(sigB64)
	if err1 != nil || err2 != nil || !hmac.Equal(sig, a.mac(string(payload))) {
		return nil
	}
	parts := strings.Split(string(payload), "|")
	if len(parts) != 3 {
		return nil
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Now().Unix() > exp || !a.cfg.AllowedEmails[parts[0]] {
		return nil
	}
	a.mu.Lock()
	_, revoked := a.revoked[sigB64]
	a.mu.Unlock()
	if revoked {
		return nil
	}
	return &session{email: parts[0], expires: time.Unix(exp, 0), csrf: parts[2]}
}

func (a *Auth) Logout(r *http.Request) {
	s := a.Session(r)
	if s == nil {
		return
	}
	cookie, _ := r.Cookie(sessionCookie)
	_, sig, _ := strings.Cut(cookie.Value, ".")
	a.mu.Lock()
	a.revoked[sig] = s.expires
	a.mu.Unlock()
}

func (a *Auth) SetCookie(w http.ResponseWriter, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   a.cfg.SecureCookies,
		SameSite: http.SameSiteStrictMode,
	})
}

func (a *Auth) sendMail(ctx context.Context, to, code string) error {
	body, _ := json.Marshal(map[string]any{
		"sender":  map[string]string{"name": a.cfg.MailFromName, "email": a.cfg.MailFrom},
		"to":      []map[string]string{{"email": to}},
		"subject": "Tu código de acceso: " + code,
		"textContent": fmt.Sprintf(
			"Tu código para entrar al panel de just-a-db es %s.\n\nVence en 10 minutos. Si no lo pediste, ignora este correo.", code),
		"htmlContent": fmt.Sprintf(
			`<p>Tu código para entrar al panel de just-a-db es:</p><p style="font-size:28px;font-weight:bold;letter-spacing:4px">%s</p><p>Vence en 10 minutos. Si no lo pediste, ignora este correo.</p>`, code),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.brevo.com/v3/smtp/email", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("api-key", a.cfg.BrevoAPIKey)
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("brevo: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("brevo: %s: %s", resp.Status, msg)
	}
	return nil
}
