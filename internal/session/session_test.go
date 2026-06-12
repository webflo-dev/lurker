package session

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func requestWithCookies(t *testing.T, rec *httptest.ResponseRecorder) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range rec.Result().Cookies() {
		r.AddCookie(c)
	}
	return r
}

func TestSessionRoundtrip(t *testing.T) {
	m := NewManager([]byte("0123456789abcdef0123456789abcdef"), false)

	rec := httptest.NewRecorder()
	if err := m.Issue(rec, "user-123", "alice"); err != nil {
		t.Fatal(err)
	}

	sess := m.Get(requestWithCookies(t, rec))
	if sess == nil {
		t.Fatal("expected a valid session")
	}
	if sess.Sub != "user-123" || sess.Name != "alice" {
		t.Fatalf("unexpected session: %+v", sess)
	}
}

func TestSessionTamper(t *testing.T) {
	m := NewManager([]byte("0123456789abcdef0123456789abcdef"), false)

	rec := httptest.NewRecorder()
	if err := m.Issue(rec, "user-123", "alice"); err != nil {
		t.Fatal(err)
	}
	cookie := rec.Result().Cookies()[0]

	// Alter the signed payload (the base64 part before the dot)
	// without re-signing.
	body, sig, _ := strings.Cut(cookie.Value, ".")
	flipped := byte('A')
	if body[0] == 'A' {
		flipped = 'B'
	}
	tampered := string(flipped) + body[1:] + "." + sig

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: CookieName, Value: tampered})
	if m.Get(r) != nil {
		t.Fatal("tampered session must be rejected")
	}

	other := NewManager([]byte("another-secret-another-secret-32"), false)
	r2 := httptest.NewRequest(http.MethodGet, "/", nil)
	r2.AddCookie(cookie)
	if other.Get(r2) != nil {
		t.Fatal("session signed with a different secret must be rejected")
	}
}

func TestSessionExpiry(t *testing.T) {
	m := NewManager([]byte("0123456789abcdef0123456789abcdef"), false)
	value, err := m.Encode(Session{Sub: "x", Name: "x", Expiry: time.Now().Add(-time.Minute).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: CookieName, Value: value})
	if m.Get(r) != nil {
		t.Fatal("expired session must be rejected")
	}
}

func TestTempCookie(t *testing.T) {
	m := NewManager([]byte("0123456789abcdef0123456789abcdef"), false)
	type payload struct{ State string }

	rec := httptest.NewRecorder()
	if err := m.SetTemp(rec, "tmp", payload{State: "abc"}, time.Minute); err != nil {
		t.Fatal(err)
	}

	rec2 := httptest.NewRecorder()
	var got payload
	if err := m.PopTemp(rec2, requestWithCookies(t, rec), "tmp", &got); err != nil {
		t.Fatal(err)
	}
	if got.State != "abc" {
		t.Fatalf("got %+v", got)
	}
	// PopTemp must clear the cookie.
	cleared := rec2.Result().Cookies()
	if len(cleared) != 1 || cleared[0].MaxAge != -1 {
		t.Fatalf("expected cookie to be cleared, got %+v", cleared)
	}
}
