// Package web wires the HTTP routes, handlers and templates together.
package web

import (
	"context"
	"net/http"
	"net/url"

	"github.com/webflo-dev/lurker/internal/config"
	"github.com/webflo-dev/lurker/internal/oidc"
	"github.com/webflo-dev/lurker/internal/reddit"
	"github.com/webflo-dev/lurker/internal/session"
	"github.com/webflo-dev/lurker/internal/store"
)

// Server holds the application's shared dependencies.
type Server struct {
	cfg      *config.Config
	store    *store.Store
	reddit   *reddit.Client
	sessions *session.Manager
	oidc     *oidc.Provider
	tmpl     templates
}

// New builds a Server. It returns an error when templates fail to parse.
func New(cfg *config.Config, st *store.Store, rd *reddit.Client, sm *session.Manager, op *oidc.Provider) (*Server, error) {
	tmpl, err := parseTemplates()
	if err != nil {
		return nil, err
	}
	return &Server{cfg: cfg, store: st, reddit: rd, sessions: sm, oidc: op, tmpl: tmpl}, nil
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.Handle("GET /static/", http.FileServerFS(assetsFS))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	mux.HandleFunc("GET /login", s.login)
	mux.HandleFunc("GET /oidc/login", s.oidcLogin)
	mux.HandleFunc("GET /oidc/callback", s.oidcCallback)
	mux.HandleFunc("GET /logout", s.logout)

	mux.Handle("GET /{$}", s.auth(s.home))
	mux.Handle("GET /r/{subreddit}", s.auth(s.subreddit))
	mux.Handle("GET /comments/{id}", s.auth(s.comments))
	mux.Handle("GET /comments/{id}/comment/{commentID}", s.auth(s.thread))
	mux.Handle("GET /subs", s.auth(s.subs))
	mux.Handle("POST /subscribe", s.auth(s.subscribe))
	mux.Handle("POST /unsubscribe", s.auth(s.unsubscribe))
	mux.Handle("GET /search", s.auth(s.search))
	mux.Handle("GET /sub-search", s.auth(s.subSearch))
	mux.Handle("GET /post-search", s.auth(s.postSearch))
	mux.Handle("GET /media", s.auth(s.media))

	return mux
}

type ctxKey int

const sessionKey ctxKey = 0

// auth redirects anonymous visitors to the login page and stores the
// session in the request context otherwise.
func (s *Server) auth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess := s.sessions.Get(r)
		if sess == nil {
			target := "/login"
			if r.Method == http.MethodGet && r.URL.Path != "/" {
				target += "?next=" + url.QueryEscape(r.URL.RequestURI())
			}
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), sessionKey, sess)))
	})
}

// user returns the session stored by the auth middleware.
func user(r *http.Request) *session.Session {
	sess, _ := r.Context().Value(sessionKey).(*session.Session)
	return sess
}

// localPath sanitizes a user-provided redirect target, only allowing
// site-local absolute paths.
func localPath(p string) string {
	if p == "" || p[0] != '/' || (len(p) > 1 && (p[1] == '/' || p[1] == '\\')) {
		return "/"
	}
	return p
}
