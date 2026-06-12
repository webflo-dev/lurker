// Package config loads application configuration from environment variables.
package config

import (
	"crypto/rand"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Config holds the full application configuration. Every field is
// controlled by a LURKER_* environment variable.
type Config struct {
	// Port the HTTP server listens on. LURKER_PORT, default 3000.
	Port int
	// BaseURL is the public URL of the instance, used to build the OIDC
	// redirect URI and to decide whether cookies are marked Secure.
	// LURKER_BASE_URL, default http://localhost:<port>.
	BaseURL *url.URL
	// DBPath is the path of the bbolt database file. LURKER_DB_PATH,
	// default ./lurker.db.
	DBPath string
	// SessionSecret signs session cookies. LURKER_SESSION_SECRET; when
	// empty a random secret is generated (sessions reset on restart).
	SessionSecret []byte
	// UserAgent sent to the Reddit API. LURKER_USER_AGENT.
	UserAgent string

	OIDC OIDC
}

// OIDC holds the OpenID Connect client settings.
type OIDC struct {
	// Issuer URL, e.g. https://auth.example.com/realms/main.
	// Discovery is performed at <issuer>/.well-known/openid-configuration.
	Issuer       string
	ClientID     string
	ClientSecret string
	Scopes       []string
}

const defaultUserAgent = "Mozilla/5.0 (X11; Linux x86_64) lurker/1.0 (self-hosted reddit reader)"

// FromEnv builds a Config from the process environment, returning an
// error describing the first invalid or missing required variable.
func FromEnv() (*Config, error) {
	cfg := &Config{
		Port:      3000,
		DBPath:    "lurker.db",
		UserAgent: defaultUserAgent,
	}

	if v := os.Getenv("LURKER_PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil || p < 1 || p > 65535 {
			return nil, fmt.Errorf("LURKER_PORT: invalid port %q", v)
		}
		cfg.Port = p
	}

	if v := os.Getenv("LURKER_DB_PATH"); v != "" {
		cfg.DBPath = v
	}
	if v := os.Getenv("LURKER_USER_AGENT"); v != "" {
		cfg.UserAgent = v
	}

	base := os.Getenv("LURKER_BASE_URL")
	if base == "" {
		base = fmt.Sprintf("http://localhost:%d", cfg.Port)
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("LURKER_BASE_URL: invalid URL %q", base)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	cfg.BaseURL = u

	if v := os.Getenv("LURKER_SESSION_SECRET"); v != "" {
		if len(v) < 32 {
			return nil, fmt.Errorf("LURKER_SESSION_SECRET: must be at least 32 characters")
		}
		cfg.SessionSecret = []byte(v)
	} else {
		cfg.SessionSecret = make([]byte, 32)
		if _, err := rand.Read(cfg.SessionSecret); err != nil {
			return nil, fmt.Errorf("generating session secret: %w", err)
		}
		slog.Warn("LURKER_SESSION_SECRET not set, using a random secret; sessions will not survive restarts")
	}

	cfg.OIDC = OIDC{
		Issuer:       strings.TrimRight(os.Getenv("LURKER_OIDC_ISSUER"), "/"),
		ClientID:     os.Getenv("LURKER_OIDC_CLIENT_ID"),
		ClientSecret: os.Getenv("LURKER_OIDC_CLIENT_SECRET"),
		Scopes:       []string{"openid", "profile", "email"},
	}
	if v := os.Getenv("LURKER_OIDC_SCOPES"); v != "" {
		cfg.OIDC.Scopes = strings.Fields(v)
	}
	for name, v := range map[string]string{
		"LURKER_OIDC_ISSUER":    cfg.OIDC.Issuer,
		"LURKER_OIDC_CLIENT_ID": cfg.OIDC.ClientID,
	} {
		if v == "" {
			return nil, fmt.Errorf("%s is required", name)
		}
	}

	return cfg, nil
}

// RedirectURL is the OIDC callback URL derived from BaseURL.
func (c *Config) RedirectURL() string {
	return c.BaseURL.String() + "/oidc/callback"
}

// CookiesSecure reports whether cookies should carry the Secure flag.
func (c *Config) CookiesSecure() bool {
	return c.BaseURL.Scheme == "https"
}
