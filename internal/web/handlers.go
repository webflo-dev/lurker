package web

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/webflo-dev/lurker/internal/reddit"
)

var (
	validSorts      = []string{"hot", "new", "top", "rising", "controversial"}
	validTimes      = []string{"hour", "day", "week", "month", "year", "all"}
	subredditNameRe = regexp.MustCompile(`^[A-Za-z0-9_\-]+(\+[A-Za-z0-9_\-]+)*$`)
)

// home redirects to the combined feed of the user's subscriptions, or
// to r/all for new users.
func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	subs, err := s.store.Subscriptions(user(r).Sub)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err)
		return
	}
	if len(subs) == 0 {
		http.Redirect(w, r, "/r/all", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/r/"+strings.Join(subs, "+"), http.StatusSeeOther)
}

type subredditData struct {
	page
	Subreddit    string
	About        *reddit.Subreddit
	Sort         string
	T            string
	Listing      *reddit.Listing
	Subscribed   bool
	CanSubscribe bool
	CurrentURL   string
	NextURL      string
}

func (s *Server) subreddit(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("subreddit")
	if !subredditNameRe.MatchString(name) {
		s.renderError(w, r, http.StatusNotFound, fmt.Errorf("invalid subreddit name"))
		return
	}

	q := r.URL.Query()
	sort := q.Get("sort")
	if !slices.Contains(validSorts, sort) {
		sort = "hot"
	}
	t := q.Get("t")
	if !slices.Contains(validTimes, t) || (sort != "top" && sort != "controversial") {
		t = ""
	}

	listing, err := s.reddit.Posts(r.Context(), name, sort, reddit.ListingOptions{After: q.Get("after"), T: t})
	if err != nil {
		s.renderError(w, r, http.StatusBadGateway, err)
		return
	}

	data := subredditData{
		page:      page{User: user(r), Title: "r/" + name},
		Subreddit: name,
		Sort:      sort,
		T:         t,
		Listing:   listing,
	}

	single := !strings.Contains(name, "+") && name != "all" && name != "popular"
	if single {
		data.CanSubscribe = true
		data.Subscribed, _ = s.store.IsSubscribed(user(r).Sub, name)
		// Metadata is decorative: ignore failures rather than 502 the page.
		data.About, _ = s.reddit.About(r.Context(), name)
	}

	data.CurrentURL = listingURL(name, sort, t, "")
	if listing.After != "" {
		data.NextURL = listingURL(name, sort, t, listing.After)
	}
	s.render(w, "subreddit", data)
}

func listingURL(name, sort, t, after string) string {
	v := url.Values{}
	if sort != "hot" {
		v.Set("sort", sort)
	}
	if t != "" {
		v.Set("t", t)
	}
	if after != "" {
		v.Set("after", after)
	}
	u := "/r/" + name
	if len(v) > 0 {
		u += "?" + v.Encode()
	}
	return u
}

type commentsData struct {
	page
	Post     *reddit.Post
	Comments []*reddit.Comment
	// ThreadRoot is set on the single-thread view to link back to the
	// full discussion.
	ThreadRoot string
}

func (s *Server) comments(w http.ResponseWriter, r *http.Request) {
	post, comments, err := s.reddit.Comments(r.Context(), r.PathValue("id"))
	if err != nil {
		s.renderError(w, r, http.StatusBadGateway, err)
		return
	}
	s.render(w, "comments", commentsData{
		page:     page{User: user(r), Title: post.Title},
		Post:     post,
		Comments: comments,
	})
}

func (s *Server) thread(w http.ResponseWriter, r *http.Request) {
	post, comments, err := s.reddit.CommentThread(r.Context(), r.PathValue("id"), r.PathValue("commentID"))
	if err != nil {
		s.renderError(w, r, http.StatusBadGateway, err)
		return
	}
	s.render(w, "comments", commentsData{
		page:       page{User: user(r), Title: post.Title},
		Post:       post,
		Comments:   comments,
		ThreadRoot: "/comments/" + post.ID,
	})
}

type subsData struct {
	page
	Subs []string
}

func (s *Server) subs(w http.ResponseWriter, r *http.Request) {
	subs, err := s.store.Subscriptions(user(r).Sub)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err)
		return
	}
	s.render(w, "subs", subsData{page: page{User: user(r), Title: "subscriptions"}, Subs: subs})
}

func (s *Server) subscribe(w http.ResponseWriter, r *http.Request) {
	s.setSubscription(w, r, s.store.Subscribe)
}

func (s *Server) unsubscribe(w http.ResponseWriter, r *http.Request) {
	s.setSubscription(w, r, s.store.Unsubscribe)
}

func (s *Server) setSubscription(w http.ResponseWriter, r *http.Request, op func(userID, subreddit string) error) {
	if err := op(user(r).Sub, r.FormValue("subreddit")); err != nil {
		s.renderError(w, r, http.StatusBadRequest, err)
		return
	}
	http.Redirect(w, r, localPath(r.FormValue("next")), http.StatusSeeOther)
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	s.render(w, "search", page{User: user(r), Title: "search"})
}

type subSearchData struct {
	page
	Query      string
	Results    []*reddit.Subreddit
	Subscribed map[string]bool
}

func (s *Server) subSearch(w http.ResponseWriter, r *http.Request) {
	data := subSearchData{
		page:       page{User: user(r), Title: "subreddit search"},
		Query:      strings.TrimSpace(r.URL.Query().Get("q")),
		Subscribed: map[string]bool{},
	}
	if data.Query != "" {
		results, err := s.reddit.SearchSubreddits(r.Context(), data.Query)
		if err != nil {
			s.renderError(w, r, http.StatusBadGateway, err)
			return
		}
		data.Results = results
		subs, _ := s.store.Subscriptions(user(r).Sub)
		for _, sub := range subs {
			data.Subscribed[sub] = true
		}
	}
	s.render(w, "subsearch", data)
}

type postSearchData struct {
	page
	Query   string
	Listing *reddit.Listing
	NextURL string
}

func (s *Server) postSearch(w http.ResponseWriter, r *http.Request) {
	data := postSearchData{
		page:  page{User: user(r), Title: "post search"},
		Query: strings.TrimSpace(r.URL.Query().Get("q")),
	}
	if data.Query != "" {
		listing, err := s.reddit.SearchPosts(r.Context(), data.Query, reddit.ListingOptions{After: r.URL.Query().Get("after")})
		if err != nil {
			s.renderError(w, r, http.StatusBadGateway, err)
			return
		}
		data.Listing = listing
		if listing.After != "" {
			data.NextURL = "/post-search?" + url.Values{"q": {data.Query}, "after": {listing.After}}.Encode()
		}
	}
	s.render(w, "postsearch", data)
}

type mediaData struct {
	page
	URL     string
	IsVideo bool
}

// mediaHosts are the only origins the media viewer will embed.
var mediaHosts = map[string]bool{
	"i.redd.it":                true,
	"v.redd.it":                true,
	"preview.redd.it":          true,
	"external-preview.redd.it": true,
	"i.imgur.com":              true,
}

func (s *Server) media(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("url")
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !mediaHosts[u.Hostname()] {
		s.renderError(w, r, http.StatusBadRequest, fmt.Errorf("unsupported media URL"))
		return
	}
	s.render(w, "media", mediaData{
		page:    page{User: user(r), Title: "media"},
		URL:     u.String(),
		IsVideo: u.Hostname() == "v.redd.it" || strings.HasSuffix(u.Path, ".mp4"),
	})
}

type loginData struct {
	page
	Next string
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if s.cfg.OIDC.Disabled || s.sessions.Get(r) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.render(w, "login", loginData{
		page: page{Title: "login"},
		Next: localPath(r.URL.Query().Get("next")),
	})
}

// oidcState is the payload of the short-lived cookie that carries the
// OIDC request context across the redirect to the identity provider.
type oidcState struct {
	State string `json:"state"`
	Nonce string `json:"nonce"`
	Next  string `json:"next"`
}

const oidcCookie = "lurker_oidc"

func (s *Server) oidcLogin(w http.ResponseWriter, r *http.Request) {
	if s.cfg.OIDC.Disabled {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	st := oidcState{
		State: randomToken(),
		Nonce: randomToken(),
		Next:  localPath(r.URL.Query().Get("next")),
	}
	authURL, err := s.oidc.AuthURL(r.Context(), s.cfg.RedirectURL(), st.State, st.Nonce, s.cfg.OIDC.Scopes)
	if err != nil {
		s.renderError(w, r, http.StatusBadGateway, err)
		return
	}
	if err := s.sessions.SetTemp(w, oidcCookie, st, 10*time.Minute); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err)
		return
	}
	http.Redirect(w, r, authURL, http.StatusSeeOther)
}

func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	if s.cfg.OIDC.Disabled {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	var st oidcState
	if err := s.sessions.PopTemp(w, r, oidcCookie, &st); err != nil {
		s.renderError(w, r, http.StatusBadRequest, fmt.Errorf("login session expired, please retry"))
		return
	}
	q := r.URL.Query()
	if errCode := q.Get("error"); errCode != "" {
		s.renderError(w, r, http.StatusBadGateway, fmt.Errorf("identity provider error: %s (%s)", errCode, q.Get("error_description")))
		return
	}
	if q.Get("state") == "" || q.Get("state") != st.State {
		s.renderError(w, r, http.StatusBadRequest, fmt.Errorf("state mismatch, please retry"))
		return
	}
	claims, err := s.oidc.Exchange(r.Context(), q.Get("code"), s.cfg.RedirectURL(), st.Nonce)
	if err != nil {
		slog.Error("oidc exchange failed", "error", err)
		s.renderError(w, r, http.StatusBadGateway, fmt.Errorf("login failed: %w", err))
		return
	}
	if err := s.sessions.Issue(w, claims.Subject, claims.DisplayName()); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err)
		return
	}
	slog.Info("user logged in", "sub", claims.Subject, "name", claims.DisplayName())
	http.Redirect(w, r, localPath(st.Next), http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if s.cfg.OIDC.Disabled {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.sessions.Clear(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func randomToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

type errorData struct {
	page
	Status  int
	Message string
}

func (s *Server) renderError(w http.ResponseWriter, r *http.Request, status int, err error) {
	if status >= 500 {
		slog.Error("request failed", "path", r.URL.Path, "error", err)
	}
	w.WriteHeader(status)
	s.render(w, "error", errorData{
		page:    page{User: s.sessions.Get(r), Title: "error"},
		Status:  status,
		Message: err.Error(),
	})
}
