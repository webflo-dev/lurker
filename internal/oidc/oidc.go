// Package oidc implements an OpenID Connect relying party (authorization
// code flow) using only the standard library: provider discovery, token
// exchange, and ID token verification against the provider's JWKS.
// Supported ID token signature algorithms: RS256 and ES256.
package oidc

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Provider is an OIDC relying party bound to one issuer and client.
type Provider struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	HTTP         *http.Client

	mu        sync.Mutex
	endpoints *endpoints
	keys      map[string]crypto.PublicKey
	lastJWKS  time.Time
}

type endpoints struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
}

// Claims are the ID token claims lurker cares about.
type Claims struct {
	Issuer            string   `json:"iss"`
	Subject           string   `json:"sub"`
	Audience          audience `json:"aud"`
	Expiry            int64    `json:"exp"`
	Nonce             string   `json:"nonce"`
	Email             string   `json:"email"`
	Name              string   `json:"name"`
	PreferredUsername string   `json:"preferred_username"`
}

// DisplayName picks the best available human-readable identifier.
func (c *Claims) DisplayName() string {
	for _, v := range []string{c.PreferredUsername, c.Name, c.Email} {
		if v != "" {
			return v
		}
	}
	return c.Subject
}

// audience unmarshals the aud claim, which may be a string or an array.
type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*a = []string{s}
		return nil
	}
	return json.Unmarshal(b, (*[]string)(a))
}

// New creates a Provider. Discovery happens lazily on first use so the
// application can start while the identity provider is still booting.
func New(issuer, clientID, clientSecret string) *Provider {
	return &Provider{
		Issuer:       strings.TrimRight(issuer, "/"),
		ClientID:     clientID,
		ClientSecret: clientSecret,
		HTTP:         &http.Client{Timeout: 15 * time.Second},
	}
}

func (p *Provider) discover(ctx context.Context) (*endpoints, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.endpoints != nil {
		return p.endpoints, nil
	}
	var eps endpoints
	if err := p.getJSON(ctx, p.Issuer+"/.well-known/openid-configuration", &eps); err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}
	if eps.AuthorizationEndpoint == "" || eps.TokenEndpoint == "" || eps.JWKSURI == "" {
		return nil, errors.New("oidc discovery: incomplete provider metadata")
	}
	p.endpoints = &eps
	return p.endpoints, nil
}

// AuthURL builds the authorization request URL.
func (p *Provider) AuthURL(ctx context.Context, redirectURI, state, nonce string, scopes []string) (string, error) {
	eps, err := p.discover(ctx)
	if err != nil {
		return "", err
	}
	q := url.Values{
		"response_type": {"code"},
		"client_id":     {p.ClientID},
		"redirect_uri":  {redirectURI},
		"scope":         {strings.Join(scopes, " ")},
		"state":         {state},
		"nonce":         {nonce},
	}
	sep := "?"
	if strings.Contains(eps.AuthorizationEndpoint, "?") {
		sep = "&"
	}
	return eps.AuthorizationEndpoint + sep + q.Encode(), nil
}

// Exchange swaps an authorization code for tokens and returns the
// verified ID token claims. nonce must match the value sent in AuthURL.
func (p *Provider) Exchange(ctx context.Context, code, redirectURI, nonce string) (*Claims, error) {
	eps, err := p.discover(ctx)
	if err != nil {
		return nil, err
	}

	form := url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {redirectURI},
		"client_id":    {p.ClientID},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, eps.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// client_secret_basic, which every conforming server must support
	// (RFC 6749 §2.3.1), with form-encoded credentials.
	req.SetBasicAuth(url.QueryEscape(p.ClientID), url.QueryEscape(p.ClientSecret))

	resp, err := p.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oidc token request: %w", err)
	}
	defer resp.Body.Close()

	var token struct {
		IDToken          string `json:"id_token"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return nil, fmt.Errorf("oidc token response: %w", err)
	}
	if token.Error != "" {
		return nil, fmt.Errorf("oidc token endpoint: %s (%s)", token.Error, token.ErrorDescription)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oidc token endpoint returned %s", resp.Status)
	}
	if token.IDToken == "" {
		return nil, errors.New("oidc token response missing id_token")
	}
	return p.verifyIDToken(ctx, token.IDToken, nonce)
}

func (p *Provider) verifyIDToken(ctx context.Context, raw, nonce string) (*Claims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, errors.New("id_token: malformed JWT")
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("id_token header: %w", err)
	}
	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("id_token payload: %w", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("id_token signature: %w", err)
	}

	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return nil, fmt.Errorf("id_token header: %w", err)
	}

	key, err := p.signingKey(ctx, header.Kid)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))

	switch header.Alg {
	case "RS256":
		pub, ok := key.(*rsa.PublicKey)
		if !ok {
			return nil, errors.New("id_token: key type does not match RS256")
		}
		if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
			return nil, errors.New("id_token: invalid signature")
		}
	case "ES256":
		pub, ok := key.(*ecdsa.PublicKey)
		if !ok {
			return nil, errors.New("id_token: key type does not match ES256")
		}
		if len(sig) != 64 {
			return nil, errors.New("id_token: invalid ES256 signature length")
		}
		r := new(big.Int).SetBytes(sig[:32])
		s := new(big.Int).SetBytes(sig[32:])
		if !ecdsa.Verify(pub, digest[:], r, s) {
			return nil, errors.New("id_token: invalid signature")
		}
	default:
		return nil, fmt.Errorf("id_token: unsupported algorithm %q", header.Alg)
	}

	var claims Claims
	if err := json.Unmarshal(payloadJSON, &claims); err != nil {
		return nil, fmt.Errorf("id_token claims: %w", err)
	}
	if strings.TrimRight(claims.Issuer, "/") != p.Issuer {
		return nil, fmt.Errorf("id_token: issuer mismatch %q", claims.Issuer)
	}
	audOK := false
	for _, aud := range claims.Audience {
		audOK = audOK || aud == p.ClientID
	}
	if !audOK {
		return nil, errors.New("id_token: audience mismatch")
	}
	if time.Now().Unix() >= claims.Expiry {
		return nil, errors.New("id_token: expired")
	}
	if claims.Nonce != nonce {
		return nil, errors.New("id_token: nonce mismatch")
	}
	if claims.Subject == "" {
		return nil, errors.New("id_token: missing sub claim")
	}
	return &claims, nil
}

// signingKey returns the provider key with the given id, refreshing the
// cached JWKS when the id is unknown (at most once per minute).
func (p *Provider) signingKey(ctx context.Context, kid string) (crypto.PublicKey, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if key, ok := p.keys[kid]; ok {
		return key, nil
	}
	if time.Since(p.lastJWKS) < time.Minute {
		return nil, fmt.Errorf("id_token: unknown key id %q", kid)
	}
	if err := p.refreshJWKSLocked(ctx); err != nil {
		return nil, err
	}
	if key, ok := p.keys[kid]; ok {
		return key, nil
	}
	// A JWKS with a single key and no kid is allowed to match any token.
	if kid == "" && len(p.keys) == 1 {
		for _, key := range p.keys {
			return key, nil
		}
	}
	return nil, fmt.Errorf("id_token: unknown key id %q", kid)
}

func (p *Provider) refreshJWKSLocked(ctx context.Context) error {
	if p.endpoints == nil {
		return errors.New("oidc: provider not discovered")
	}
	var jwks struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			Use string `json:"use"`
			Crv string `json:"crv"`
			N   string `json:"n"`
			E   string `json:"e"`
			X   string `json:"x"`
			Y   string `json:"y"`
		} `json:"keys"`
	}
	if err := p.getJSON(ctx, p.endpoints.JWKSURI, &jwks); err != nil {
		return fmt.Errorf("oidc jwks: %w", err)
	}
	keys := make(map[string]crypto.PublicKey, len(jwks.Keys))
	for _, k := range jwks.Keys {
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		switch k.Kty {
		case "RSA":
			n, err1 := base64.RawURLEncoding.DecodeString(k.N)
			e, err2 := base64.RawURLEncoding.DecodeString(k.E)
			if err1 != nil || err2 != nil {
				continue
			}
			keys[k.Kid] = &rsa.PublicKey{
				N: new(big.Int).SetBytes(n),
				E: int(new(big.Int).SetBytes(e).Int64()),
			}
		case "EC":
			if k.Crv != "P-256" {
				continue
			}
			x, err1 := base64.RawURLEncoding.DecodeString(k.X)
			y, err2 := base64.RawURLEncoding.DecodeString(k.Y)
			if err1 != nil || err2 != nil {
				continue
			}
			keys[k.Kid] = &ecdsa.PublicKey{
				Curve: elliptic.P256(),
				X:     new(big.Int).SetBytes(x),
				Y:     new(big.Int).SetBytes(y),
			}
		}
	}
	p.keys = keys
	p.lastJWKS = time.Now()
	return nil
}

func (p *Provider) getJSON(ctx context.Context, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned %s", u, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
