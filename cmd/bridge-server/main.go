package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/mc373906408/codex-mobile-bridge-server/internal/api"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/config"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/push"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/store"
	"github.com/mc373906408/codex-mobile-bridge-server/internal/worker"
)

func main() {
	if err := run(); err != nil {
		slog.Error("bridge server stopped", "reason", err.Error())
		os.Exit(1)
	}
}
func run() error {
	command := "serve"
	if len(os.Args) > 2 {
		return apiStartupError("usage: bridge-server [serve|migrate|healthcheck]")
	}
	if len(os.Args) == 2 {
		command = os.Args[1]
	}
	if command != "serve" && command != "migrate" && command != "healthcheck" {
		return apiStartupError("usage: bridge-server [serve|migrate|healthcheck]")
	}
	c, err := config.Load()
	if err != nil {
		return err
	}
	if command == "healthcheck" {
		return checkHealth(c)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	s, err := store.Open(ctx, c.DatabaseURL, c.DataKey)
	if err != nil {
		return apiStartupError("database connection failed")
	}
	defer s.Pool.Close()
	if err = s.Migrate(ctx); err != nil {
		slog.Error("database migration failed")
		return apiStartupError("database migration failed")
	}
	if command == "migrate" {
		slog.Info("database migrations applied")
		return nil
	}
	if c.AdminLogin != "" {
		login, err := api.NormalizeLogin(c.AdminLogin, c.Paid())
		if err != nil {
			return apiStartupError("invalid bootstrap login")
		}
		if err = s.Bootstrap(ctx, login, c.AdminPassword); err != nil {
			return apiStartupError("bootstrap administrator failed")
		}
	}
	providers, err := push.NewProviders(c)
	if err != nil {
		return err
	}
	app, err := api.New(c, s)
	if err != nil {
		return err
	}
	w := &worker.Worker{Store: s, Config: c, Push: providers}
	if c.MailEnabled() {
		w.Mail = worker.SMTP{Config: c.SMTP}
	}
	server := &http.Server{Addr: c.Listen, Handler: app, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); w.Run(ctx) }()
	wg.Add(1)
	go func() { defer wg.Done(); s.RunCleanup(ctx) }()
	go func() {
		<-ctx.Done()
		app.Hub.Shutdown()
		shutdown, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = server.Shutdown(shutdown)
	}()
	slog.Info("bridge server listening", "mode", c.Mode, "address", c.Listen)
	err = server.ListenAndServe()
	cancel()
	wg.Wait()
	if err == http.ErrServerClosed {
		return nil
	}
	return apiStartupError("HTTP listener failed")
}

type apiStartupError string

func (e apiStartupError) Error() string { return string(e) }
