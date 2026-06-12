// Command lurker is a self-hosted, read-only Reddit reader with OIDC
// authentication. See README.md for configuration.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/webflo-dev/lurker/internal/config"
	"github.com/webflo-dev/lurker/internal/oidc"
	"github.com/webflo-dev/lurker/internal/reddit"
	"github.com/webflo-dev/lurker/internal/session"
	"github.com/webflo-dev/lurker/internal/store"
	"github.com/webflo-dev/lurker/internal/web"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	rd := reddit.NewClient(cfg.UserAgent)
	rd.BaseURL = cfg.RedditURL

	srv, err := web.New(
		cfg,
		st,
		rd,
		session.NewManager(cfg.SessionSecret, cfg.CookiesSecure()),
		oidc.New(cfg.OIDC.Issuer, cfg.OIDC.ClientID, cfg.OIDC.ClientSecret),
	)
	if err != nil {
		return err
	}

	httpServer := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		slog.Info("lurker listening", "addr", httpServer.Addr, "base_url", cfg.BaseURL.String())
		errCh <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		slog.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return nil
	}
}
