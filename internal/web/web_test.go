package web_test

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/webflo-dev/lurker/internal/config"
	"github.com/webflo-dev/lurker/internal/oidc"
	"github.com/webflo-dev/lurker/internal/oidc/oidctest"
	"github.com/webflo-dev/lurker/internal/reddit"
	"github.com/webflo-dev/lurker/internal/session"
	"github.com/webflo-dev/lurker/internal/store"
	"github.com/webflo-dev/lurker/internal/web"
)

const listingFixture = `{
  "kind": "Listing",
  "data": {
    "after": "",
    "children": [
      {"kind": "t3", "data": {
        "id": "abc", "title": "Go 1.24 released", "author": "gopher",
        "subreddit": "golang", "url": "https://go.dev/blog", "domain": "go.dev",
        "thumbnail": "self", "score": 1234, "num_comments": 56,
        "created_utc": 1700000000, "is_self": true
      }}
    ]
  }
}`

const commentsFixture = `[
  {"kind": "Listing", "data": {"children": [
    {"kind": "t3", "data": {"id": "abc", "title": "Go 1.24 released", "author": "gopher", "subreddit": "golang", "score": 1234, "num_comments": 1, "created_utc": 1700000000, "is_self": true}}
  ]}},
  {"kind": "Listing", "data": {"children": [
    {"kind": "t1", "data": {"id": "c1", "author": "alice", "body_html": "<p>great release</p>", "score": 10, "created_utc": 1700000200, "replies": ""}}
  ]}}
]`

type app struct {
	url    string
	client *http.Client
	idp    *oidctest.Server
}

func newTestApp(t *testing.T, opts ...func(*config.Config)) *app {
	t.Helper()

	idp := oidctest.New("lurker", "s3cret")
	t.Cleanup(idp.Close)

	fakeReddit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/about.json"):
			w.Write([]byte(`{"kind": "t5", "data": {"display_name": "golang", "title": "The Go Programming Language", "subscribers": 200000}}`))
		case strings.HasPrefix(r.URL.Path, "/comments/"):
			w.Write([]byte(commentsFixture))
		default:
			w.Write([]byte(listingFixture))
		}
	}))
	t.Cleanup(fakeReddit.Close)

	// The app's base URL must be known before the handler exists, so
	// serve through an indirection.
	var handler http.Handler
	appServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(appServer.Close)

	baseURL, err := url.Parse(appServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Port:          0,
		BaseURL:       baseURL,
		DBPath:        filepath.Join(t.TempDir(), "test.db"),
		SessionSecret: []byte("0123456789abcdef0123456789abcdef"),
		UserAgent:     "test-agent",
		OIDC: config.OIDC{
			Issuer:       idp.URL,
			ClientID:     "lurker",
			ClientSecret: "s3cret",
			Scopes:       []string{"openid", "profile", "email"},
		},
	}
	for _, opt := range opts {
		opt(cfg)
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	rd := reddit.NewClient(cfg.UserAgent)
	rd.BaseURL = fakeReddit.URL

	srv, err := web.New(cfg, st, rd,
		session.NewManager(cfg.SessionSecret, false),
		oidc.New(cfg.OIDC.Issuer, cfg.OIDC.ClientID, cfg.OIDC.ClientSecret),
	)
	if err != nil {
		t.Fatal(err)
	}
	handler = srv.Handler()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &app{url: appServer.URL, client: &http.Client{Jar: jar}, idp: idp}
}

func (a *app) get(t *testing.T, path string) (int, string) {
	t.Helper()
	resp, err := a.client.Get(a.url + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func (a *app) post(t *testing.T, path string, form url.Values) (int, string) {
	t.Helper()
	resp, err := a.client.PostForm(a.url+path, form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func (a *app) login(t *testing.T) {
	t.Helper()
	status, body := a.get(t, "/oidc/login")
	if status != http.StatusOK || !strings.Contains(body, "Go 1.24 released") {
		t.Fatalf("login flow did not land on the feed: status=%d body=%.300s", status, body)
	}
}

func TestAnonymousRedirectsToLogin(t *testing.T) {
	a := newTestApp(t)
	status, body := a.get(t, "/")
	if status != http.StatusOK || !strings.Contains(body, "sign in with SSO") {
		t.Fatalf("expected login page, got status=%d body=%.300s", status, body)
	}
}

func TestLoginFlowAndBrowsing(t *testing.T) {
	a := newTestApp(t)
	// Full round trip: /oidc/login → IdP authorize → callback → home
	// → /r/all rendered with the fake Reddit data.
	a.login(t)

	status, body := a.get(t, "/r/golang")
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	for _, want := range []string{"Go 1.24 released", "The Go Programming Language", "subscribe"} {
		if !strings.Contains(body, want) {
			t.Errorf("subreddit page missing %q", want)
		}
	}

	status, body = a.get(t, "/comments/abc")
	if status != http.StatusOK || !strings.Contains(body, "<p>great release</p>") {
		t.Fatalf("comments page wrong: status=%d body=%.300s", status, body)
	}
}

func TestSubscriptionLifecycle(t *testing.T) {
	a := newTestApp(t)
	a.login(t)

	status, body := a.post(t, "/subscribe", url.Values{"subreddit": {"golang"}, "next": {"/subs"}})
	if status != http.StatusOK || !strings.Contains(body, `href="/r/golang"`) {
		t.Fatalf("subscribe failed: status=%d body=%.300s", status, body)
	}

	// Home now redirects to the combined feed of subscriptions.
	resp, err := a.client.Get(a.url + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := resp.Request.URL.Path; got != "/r/golang" {
		t.Fatalf("home landed on %q, want /r/golang", got)
	}

	status, body = a.post(t, "/unsubscribe", url.Values{"subreddit": {"golang"}, "next": {"/subs"}})
	if status != http.StatusOK || strings.Contains(body, `href="/r/golang"`) {
		t.Fatalf("unsubscribe failed: status=%d body=%.300s", status, body)
	}
}

func TestCallbackStateMismatch(t *testing.T) {
	a := newTestApp(t)
	// No prior /oidc/login: the state cookie is missing entirely.
	status, _ := a.get(t, "/oidc/callback?code=x&state=y")
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", status)
	}
}

func TestLogout(t *testing.T) {
	a := newTestApp(t)
	a.login(t)
	a.get(t, "/logout")
	status, body := a.get(t, "/subs")
	if status != http.StatusOK || !strings.Contains(body, "sign in with SSO") {
		t.Fatalf("expected to be logged out, got status=%d", status)
	}
}

func TestOpenRedirectBlocked(t *testing.T) {
	a := newTestApp(t)
	a.login(t)
	resp, err := a.client.Get(a.url + "/logout")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// next must be a site-local path; anything else falls back to /.
	resp, err = a.client.Get(a.url + "/oidc/login?next=" + url.QueryEscape("https://evil.example/"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Request.URL.Host; !strings.Contains(a.url, got) {
		t.Fatalf("redirected off-site to %q", resp.Request.URL)
	}
}

func TestOIDCDisabled(t *testing.T) {
	a := newTestApp(t, func(cfg *config.Config) {
		cfg.OIDC = config.OIDC{Disabled: true}
	})

	// No login required: / goes straight to the feed.
	status, body := a.get(t, "/")
	if status != http.StatusOK || !strings.Contains(body, "Go 1.24 released") {
		t.Fatalf("expected feed without login, got status=%d body=%.300s", status, body)
	}
	if strings.Contains(body, "/logout") {
		t.Error("logout link should be hidden when OIDC is disabled")
	}

	// Subscriptions work under the shared local user.
	status, body = a.post(t, "/subscribe", url.Values{"subreddit": {"golang"}, "next": {"/subs"}})
	if status != http.StatusOK || !strings.Contains(body, `href="/r/golang"`) {
		t.Fatalf("subscribe failed: status=%d body=%.300s", status, body)
	}

	// Auth pages collapse to the home page.
	for _, path := range []string{"/login", "/oidc/login", "/oidc/callback", "/logout"} {
		resp, err := a.client.Get(a.url + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.Request.URL.Path == path {
			t.Errorf("%s should redirect away when OIDC is disabled", path)
		}
	}
}

func TestMediaHostAllowlist(t *testing.T) {
	a := newTestApp(t)
	a.login(t)
	status, _ := a.get(t, "/media?url="+url.QueryEscape("https://evil.example/x.png"))
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400 for disallowed media host, got %d", status)
	}
	status, body := a.get(t, "/media?url="+url.QueryEscape("https://i.redd.it/x.png"))
	if status != http.StatusOK || !strings.Contains(body, "https://i.redd.it/x.png") {
		t.Fatalf("expected media page, got %d", status)
	}
}
