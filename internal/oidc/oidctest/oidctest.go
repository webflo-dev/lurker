// Package oidctest provides a minimal in-process OpenID Connect
// provider for tests: discovery, authorization, token and JWKS
// endpoints backed by a throwaway RSA key.
package oidctest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"time"
)

const KeyID = "test-key"

// Server is a fake OIDC provider.
type Server struct {
	*httptest.Server
	Key          *rsa.PrivateKey
	ClientID     string
	ClientSecret string

	// Subject and Username are the claims minted into ID tokens.
	Subject  string
	Username string

	mu     sync.Mutex
	codes  map[string]string // code -> nonce
	nextID int
}

// New starts a fake provider; callers must Close it.
func New(clientID, clientSecret string) *Server {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	s := &Server{
		Key:          key,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Subject:      "user-1",
		Username:     "alice",
		codes:        map[string]string{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", s.discovery)
	mux.HandleFunc("GET /authorize", s.authorize)
	mux.HandleFunc("POST /token", s.token)
	mux.HandleFunc("GET /jwks", s.jwks)
	s.Server = httptest.NewServer(mux)
	return s
}

func (s *Server) discovery(w http.ResponseWriter, _ *http.Request) {
	json.NewEncoder(w).Encode(map[string]string{
		"issuer":                 s.URL,
		"authorization_endpoint": s.URL + "/authorize",
		"token_endpoint":         s.URL + "/token",
		"jwks_uri":               s.URL + "/jwks",
	})
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("client_id") != s.ClientID || q.Get("response_type") != "code" {
		http.Error(w, "invalid authorization request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.nextID++
	code := fmt.Sprintf("code-%d", s.nextID)
	s.codes[code] = q.Get("nonce")
	s.mu.Unlock()

	redirect, err := url.Parse(q.Get("redirect_uri"))
	if err != nil {
		http.Error(w, "bad redirect_uri", http.StatusBadRequest)
		return
	}
	rq := redirect.Query()
	rq.Set("code", code)
	rq.Set("state", q.Get("state"))
	redirect.RawQuery = rq.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	id, secret, ok := r.BasicAuth()
	if !ok || id != url.QueryEscape(s.ClientID) || secret != url.QueryEscape(s.ClientSecret) {
		http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
		return
	}
	r.ParseForm()
	s.mu.Lock()
	nonce, ok := s.codes[r.PostFormValue("code")]
	delete(s.codes, r.PostFormValue("code"))
	s.mu.Unlock()
	if !ok {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		return
	}
	json.NewEncoder(w).Encode(map[string]string{
		"access_token": "unused",
		"token_type":   "Bearer",
		"id_token":     s.MintIDToken(nonce, time.Hour),
	})
}

func (s *Server) jwks(w http.ResponseWriter, _ *http.Request) {
	pub := s.Key.Public().(*rsa.PublicKey)
	json.NewEncoder(w).Encode(map[string]any{
		"keys": []map[string]string{{
			"kty": "RSA",
			"use": "sig",
			"kid": KeyID,
			"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}},
	})
}

// MintIDToken signs an RS256 ID token with the server's key.
func (s *Server) MintIDToken(nonce string, ttl time.Duration) string {
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": KeyID})
	payload, _ := json.Marshal(map[string]any{
		"iss":                s.URL,
		"sub":                s.Subject,
		"aud":                s.ClientID,
		"exp":                time.Now().Add(ttl).Unix(),
		"iat":                time.Now().Unix(),
		"nonce":              nonce,
		"preferred_username": s.Username,
	})
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.Key, crypto.SHA256, digest[:])
	if err != nil {
		panic(err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}
