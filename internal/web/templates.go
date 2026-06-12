package web

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/webflo-dev/lurker/internal/reddit"
	"github.com/webflo-dev/lurker/internal/session"
)

//go:embed templates static
var assetsFS embed.FS

// page carries the fields every view needs; page data structs embed it.
type page struct {
	User  *session.Session
	Title string
}

type templates map[string]*template.Template

var funcMap = template.FuncMap{
	"ago":      timeAgo,
	"num":      humanizeNumber,
	"raw":      func(s string) template.HTML { return template.HTML(s) },
	"postLink": postLink,
}

var pageNames = []string{
	"subreddit", "comments", "subs", "search",
	"subsearch", "postsearch", "login", "media", "error",
}

// parseTemplates builds one template set per page: the shared layout
// and partials plus the page file, which overrides the layout's blocks.
func parseTemplates() (templates, error) {
	tmpl := templates{}
	for _, name := range pageNames {
		t, err := template.New("layout.html").Funcs(funcMap).ParseFS(assetsFS,
			"templates/layout.html",
			"templates/partials/*.html",
			"templates/"+name+".html",
		)
		if err != nil {
			return nil, fmt.Errorf("parsing template %s: %w", name, err)
		}
		tmpl[name] = t
	}
	return tmpl, nil
}

// render executes a page template, buffering so a template failure does
// not produce a half-written page.
func (s *Server) render(w http.ResponseWriter, name string, data any) {
	var buf bytes.Buffer
	if err := s.tmpl[name].ExecuteTemplate(&buf, "layout.html", data); err != nil {
		slog.Error("template execution failed", "template", name, "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

// postLink is where a post's title should send the reader: the comment
// page for self posts, the media viewer for Reddit-hosted images and
// videos, and the external URL otherwise.
func postLink(p *reddit.Post) string {
	switch {
	case p.IsSelf:
		return "/comments/" + p.ID
	case p.VideoURL() != "":
		return "/media?url=" + url.QueryEscape(p.VideoURL())
	case p.PostHint == "image":
		return "/media?url=" + url.QueryEscape(p.URL)
	default:
		return p.URL
	}
}

func timeAgo(unixSeconds float64) string {
	d := time.Since(time.Unix(int64(unixSeconds), 0))
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo ago", int(d.Hours()/(24*30)))
	default:
		return fmt.Sprintf("%dy ago", int(d.Hours()/(24*365)))
	}
}

func humanizeNumber(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fm", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}
