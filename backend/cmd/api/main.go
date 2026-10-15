package main

import (
	"context"
	"errors"
	"github.com/PlatvaBestHorse/hr-testing/backend/internal/config"
	"github.com/PlatvaBestHorse/hr-testing/backend/internal/database"
	"github.com/PlatvaBestHorse/hr-testing/backend/internal/httpapi"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	c, err := config.Load()
	if err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	p, err := database.Open(ctx, c.DatabaseURL)
	if err != nil {
		slog.Error("invalid database configuration")
		os.Exit(1)
	}
	defer p.Close()
	app := httpapi.NewApp(p, c.SecureCookie, c.Origins)
	app.TrustedProxies = c.TrustedProxies
	app.CandidateIPLimit = c.CandidateIPLimit
	app.AuthKey = c.AuthKey
	app.PublicURL = c.PublicURL
	app.MailMode = c.MailMode
	if c.MailMode != "disabled" {
		app.SendAuthMail = c.MailSender.Send
	}
	workerCtx, cancelWorker := context.WithCancel(ctx)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		nextCleanup := time.Time{}
		for {
			if time.Now().After(nextCleanup) {
				if err := app.CleanupRateLimits(workerCtx); err != nil && workerCtx.Err() == nil {
					slog.Error("rate limit cleanup failed")
				}
				nextCleanup = time.Now().Add(time.Minute)
			}
			if err := app.ExpireDue(workerCtx); err != nil && workerCtx.Err() == nil {
				slog.Error("attempt expiry processing failed")
			}
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	defer func() { cancelWorker(); <-workerDone }()
	mailDone := make(chan struct{})
	go func() {
		defer close(mailDone)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			if err := app.DeliverAuthMail(workerCtx); err != nil && workerCtx.Err() == nil {
				slog.Error("account mail processing failed")
			}
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	defer func() { cancelWorker(); <-mailDone }()
	s := &http.Server{Addr: c.Address, Handler: httpapi.Router(database.Readiness{Pool: p}, app), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	done := make(chan error, 1)
	go func() { done <- s.ListenAndServe() }()
	slog.Info("API listening", "address", c.Address)
	select {
	case err = <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			slog.Error("HTTP server failed")
			os.Exit(1)
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err = s.Shutdown(shutdown); err != nil {
			slog.Error("HTTP shutdown failed")
		}
	}
}
