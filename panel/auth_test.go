package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func testAuth(key string) *Auth {
	return NewAuth(Config{
		AllowedEmails: map[string]bool{"me@example.com": true},
		SessionKey:    sessionKey(key),
		DevLogCodes:   true,
	})
}

func requestWith(cookie string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	return r
}

func TestSessionCookie(t *testing.T) {
	a := testAuth("pw")
	token := a.sign(session{email: "me@example.com", csrf: "abc", expires: time.Now().Add(time.Hour)})

	s := a.Session(requestWith(token))
	if s == nil || s.email != "me@example.com" || s.csrf != "abc" {
		t.Fatalf("valid cookie rejected: %+v", s)
	}
	// Survives a restart: a new Auth with the same key accepts it.
	if testAuth("pw").Session(requestWith(token)) == nil {
		t.Fatal("cookie rejected after restart")
	}
	if testAuth("other").Session(requestWith(token)) != nil {
		t.Fatal("cookie accepted with another key")
	}

	tampered := a.sign(session{email: "me@example.com", csrf: "abc", expires: time.Now().Add(time.Hour)})
	tampered = "x" + tampered[1:]
	if a.Session(requestWith(tampered)) != nil {
		t.Fatal("tampered cookie accepted")
	}

	expired := a.sign(session{email: "me@example.com", csrf: "abc", expires: time.Now().Add(-time.Minute)})
	if a.Session(requestWith(expired)) != nil {
		t.Fatal("expired cookie accepted")
	}

	notAllowed := a.sign(session{email: "old@example.com", csrf: "abc", expires: time.Now().Add(time.Hour)})
	if a.Session(requestWith(notAllowed)) != nil {
		t.Fatal("cookie of an email no longer allowed accepted")
	}

	a.Logout(requestWith(token))
	if a.Session(requestWith(token)) != nil {
		t.Fatal("cookie accepted after logout")
	}
}

func TestLoginCode(t *testing.T) {
	a := testAuth("pw")
	if err := a.IssueCode(t.Context(), "me@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.VerifyCode("me@example.com", "not-it"); ok {
		t.Fatal("wrong code accepted")
	}
	if _, ok := a.VerifyCode("other@example.com", "000000"); ok {
		t.Fatal("code accepted for an email without one")
	}
}
