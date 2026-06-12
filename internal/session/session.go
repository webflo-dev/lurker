// Package session implements stateless, HMAC-signed cookie sessions
// using only the standard library.
package session

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

const (
	// CookieName carries the login session.
	CookieName = "lurker_session"
	// TTL is the lifetime of a login session.
	TTL = 30 * 24 * time.Hour
)

// Session identifies a logged-in user.
type Session struct {
	// Sub is the stable OIDC subject identifier.
	Sub string `json:"sub"`
	// Name is the display name shown in the UI.
	Name string `json:"name"`
	// Expiry is a unix timestamp after which the session is invalid.
	Expiry int64 `json:"exp"`
	// Local marks the synthetic session injected when OIDC is disabled;
	// it never goes through a cookie, hence excluded from encoding.
	Local bool `json:"-"`
}

// Manager signs and verifies cookie payloads.
type Manager struct {
	secret []byte
	secure bool
}

// NewManager creates a Manager. secure controls the cookies' Secure flag.
func NewManager(secret []byte, secure bool) *Manager {
	return &Manager{secret: secret, secure: secure}
}

// Encode serializes v and appends an HMAC-SHA256 signature. The result
// is safe to store in a cookie value.
func (m *Manager) Encode(v any) (string, error) {
	payload, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	return body + "." + m.sign(body), nil
}

// Decode verifies the signature of an Encode result and unmarshals the
// payload into v.
func (m *Manager) Decode(value string, v any) error {
	body, sig, ok := strings.Cut(value, ".")
	if !ok || !hmac.Equal([]byte(m.sign(body)), []byte(sig)) {
		return errors.New("session: invalid signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return err
	}
	return json.Unmarshal(payload, v)
}

func (m *Manager) sign(body string) string {
	h := hmac.New(sha256.New, m.secret)
	h.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// Issue writes a login session cookie for the given user.
func (m *Manager) Issue(w http.ResponseWriter, sub, name string) error {
	value, err := m.Encode(Session{Sub: sub, Name: name, Expiry: time.Now().Add(TTL).Unix()})
	if err != nil {
		return err
	}
	http.SetCookie(w, m.cookie(CookieName, value, int(TTL.Seconds())))
	return nil
}

// Get returns the session carried by the request, or nil when absent,
// tampered with, or expired.
func (m *Manager) Get(r *http.Request) *Session {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return nil
	}
	var s Session
	if err := m.Decode(c.Value, &s); err != nil || time.Now().Unix() >= s.Expiry {
		return nil
	}
	return &s
}

// Clear deletes the session cookie.
func (m *Manager) Clear(w http.ResponseWriter) {
	http.SetCookie(w, m.cookie(CookieName, "", -1))
}

// SetTemp stores a short-lived signed value (e.g. the OIDC state) in a
// cookie of the given name.
func (m *Manager) SetTemp(w http.ResponseWriter, name string, v any, ttl time.Duration) error {
	value, err := m.Encode(v)
	if err != nil {
		return err
	}
	http.SetCookie(w, m.cookie(name, value, int(ttl.Seconds())))
	return nil
}

// PopTemp reads, verifies and clears a cookie written by SetTemp.
func (m *Manager) PopTemp(w http.ResponseWriter, r *http.Request, name string, v any) error {
	c, err := r.Cookie(name)
	if err != nil {
		return err
	}
	http.SetCookie(w, m.cookie(name, "", -1))
	return m.Decode(c.Value, v)
}

func (m *Manager) cookie(name, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
	}
}
