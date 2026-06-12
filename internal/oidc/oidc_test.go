package oidc_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/webflo-dev/lurker/internal/oidc"
	"github.com/webflo-dev/lurker/internal/oidc/oidctest"
)

func setup(t *testing.T) (*oidctest.Server, *oidc.Provider) {
	t.Helper()
	idp := oidctest.New("lurker", "s3cret")
	t.Cleanup(idp.Close)
	return idp, oidc.New(idp.URL, "lurker", "s3cret")
}

// authorize drives the fake provider's authorization endpoint and
// returns the code it issues.
func authorize(t *testing.T, idp *oidctest.Server, state, nonce string) string {
	t.Helper()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	q := url.Values{
		"response_type": {"code"},
		"client_id":     {idp.ClientID},
		"redirect_uri":  {"http://app.example/oidc/callback"},
		"state":         {state},
		"nonce":         {nonce},
	}
	resp, err := client.Get(idp.URL + "/authorize?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || loc.Query().Get("code") == "" {
		t.Fatalf("no code in redirect %q", resp.Header.Get("Location"))
	}
	return loc.Query().Get("code")
}

func TestExchange(t *testing.T) {
	idp, provider := setup(t)
	code := authorize(t, idp, "state-1", "nonce-1")

	claims, err := provider.Exchange(context.Background(), code, "http://app.example/oidc/callback", "nonce-1")
	if err != nil {
		t.Fatal(err)
	}
	if claims.Subject != "user-1" || claims.DisplayName() != "alice" {
		t.Fatalf("unexpected claims: %+v", claims)
	}
}

func TestExchangeNonceMismatch(t *testing.T) {
	idp, provider := setup(t)
	code := authorize(t, idp, "state-1", "nonce-1")

	_, err := provider.Exchange(context.Background(), code, "http://app.example/oidc/callback", "different-nonce")
	if err == nil || !strings.Contains(err.Error(), "nonce") {
		t.Fatalf("expected nonce error, got %v", err)
	}
}

func TestExchangeWrongSecret(t *testing.T) {
	idp, _ := setup(t)
	bad := oidc.New(idp.URL, "lurker", "wrong")
	code := authorize(t, idp, "state-1", "nonce-1")

	if _, err := bad.Exchange(context.Background(), code, "http://app.example/oidc/callback", "nonce-1"); err == nil {
		t.Fatal("expected error with wrong client secret")
	}
}

func TestExchangeInvalidCode(t *testing.T) {
	_, provider := setup(t)
	if _, err := provider.Exchange(context.Background(), "bogus", "http://app.example/oidc/callback", "n"); err == nil {
		t.Fatal("expected error for invalid code")
	}
}

func TestAuthURL(t *testing.T) {
	idp, provider := setup(t)
	authURL, err := provider.AuthURL(context.Background(), "http://app.example/cb", "st", "no", []string{"openid", "email"})
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(authURL, idp.URL+"/authorize?") {
		t.Fatalf("unexpected auth URL %q", authURL)
	}
	q := u.Query()
	if q.Get("state") != "st" || q.Get("nonce") != "no" || q.Get("scope") != "openid email" {
		t.Fatalf("unexpected query %v", q)
	}
}

func TestTamperedToken(t *testing.T) {
	idp, provider := setup(t)

	// Forge: take a valid token and alter its payload.
	valid := idp.MintIDToken("nonce-1", time.Hour)
	parts := strings.Split(valid, ".")
	forged := parts[0] + "." + parts[1] + "x." + parts[2]

	if _, err := provider.ExchangeTokenForTest(context.Background(), forged, "nonce-1"); err == nil {
		t.Fatal("expected forged token to be rejected")
	}
	if _, err := provider.ExchangeTokenForTest(context.Background(), valid, "nonce-1"); err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	if _, err := provider.ExchangeTokenForTest(context.Background(), idp.MintIDToken("nonce-1", -time.Minute), "nonce-1"); err == nil {
		t.Fatal("expected expired token to be rejected")
	}
}
