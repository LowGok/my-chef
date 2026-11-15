package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/config"
	"github.com/LowGok/my-chef/internal/email"
	"github.com/LowGok/my-chef/internal/email/mailqueue"
	httpapi "github.com/LowGok/my-chef/internal/http"
	"github.com/LowGok/my-chef/internal/newsletter"
	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

var errNewsletterRole = errors.New("newsletter service unavailable")

type newsletterRoleConfig struct {
	app      config.Config
	mail     config.Email
	settings config.Newsletter
	keys     *mailqueue.ProofKeys
	rateKey  []byte
}

func loadNewsletterRole() (newsletterRoleConfig, error) {
	invalid := func() (newsletterRoleConfig, error) { return newsletterRoleConfig{}, errNewsletterRole }
	cfg, err := config.Load()
	if err != nil || os.Getenv("MYCHEF_ADDR") == "" || os.Getenv("MYCHEF_DB") == "" || os.Getenv("MYCHEF_LIFECYCLE_DB") == "" {
		return invalid()
	}
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil || !net.ParseIP(host).IsLoopback() {
		return invalid()
	}
	mail, err := config.LoadNewsletterEmail()
	if err != nil {
		return invalid()
	}
	settings, err := config.LoadNewsletter(cfg, mail)
	if err != nil || settings.Mode == "disabled" || settings.Origin != cfg.Origin || !cfg.Secure || os.Getenv("MYCHEF_NEWSLETTER_CONSENT_VERSION") == "" {
		return invalid()
	}
	collect := os.Getenv("MYCHEF_NEWSLETTER_COLLECT")
	if collect != "0" && collect != "1" {
		return invalid()
	}
	if settings.Mode == "enabled" && (os.Getenv("MYCHEF_DEV") != "" && os.Getenv("MYCHEF_DEV") != "0" || cfg.Provider == "fixture" || mail.Fake || mail.NewsletterFrom != "MyChef <newsletter@mychef.dev>" || mail.NewsletterReplyTo != "feedback@gkldevelopment.com" || !settings.PendingDays.NoExpiry || !settings.WithdrawnDays.NoExpiry) {
		return invalid()
	}
	proof, err := config.LoadEmailProofKeys()
	if err != nil || len(proof.RateKey) != 32 {
		return invalid()
	}
	keys, err := mailqueue.NewProofKeys(proof.Active, proof.Keys)
	if err != nil {
		return invalid()
	}
	for key, ready := range settings.Readiness(true, !mail.Disabled, true, true) {
		if key != "collectionEnabled" && !ready {
			return invalid()
		}
	}
	return newsletterRoleConfig{app: cfg, mail: mail, settings: settings, keys: keys, rateKey: proof.RateKey}, nil
}

// newsletterRoleChecks belongs only to this reviewed role. The full app's
// compatibility guard is deliberately unchanged.
func newsletterRoleChecks(settings config.Newsletter, storage, mail, keys, failed bool) map[string]bool {
	checks := settings.Readiness(storage, mail, keys, true)
	checks["worker"] = !failed
	return checks
}

func newsletterWorker(ctx context.Context, failed *atomic.Bool, run func(context.Context) error) {
	defer func() {
		if recover() != nil || ctx.Err() == nil {
			failed.Store(true)
		}
	}()
	_ = run(ctx)
}

func serveNewsletter(parent context.Context) error {
	cfg, err := loadNewsletterRole()
	if err != nil {
		return errNewsletterRole
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	db, err := store.Open(cfg.app.Database, false)
	if err != nil {
		return errNewsletterRole
	}
	defer func() { _ = db.Close() }()
	unlock, err := store.LockApplication(db.Path)
	if err != nil {
		return errNewsletterRole
	}
	defer unlock()
	if db.CheckSchema(ctx) != nil {
		return errNewsletterRole
	}
	journal, err := db.OpenLifecycleJournal(ctx, cfg.app.LifecycleDatabase)
	if err != nil {
		return errNewsletterRole
	}
	defer func() { _ = journal.Close() }()
	unlockJournal, err := store.LockApplication(journal.Path)
	if err != nil {
		return errNewsletterRole
	}
	defer unlockJournal()
	manager := auth.New(db)
	manager.RecoveryRateKey = cfg.rateKey
	deletion := store.AccountDeletionOptions{Cleanup: func(tx *gorm.DB, u store.User) error {
		return manager.EraseAccountLinks(ctx, tx, u, time.Now().UTC())
	}}
	if journal.ReconcileAccountDeletions(ctx, deletion) != nil {
		return errNewsletterRole
	}
	service := &newsletter.Service{Store: db, Keys: cfg.keys, RateKey: cfg.rateKey, Origin: cfg.settings.Origin, ConsentVersion: cfg.settings.ConsentVersion, Lifecycle: journal}
	if service.Sweep(ctx, journal, cfg.settings.PendingDays, cfg.settings.WithdrawnDays, deletion) != nil {
		return errNewsletterRole
	}
	// The purpose-scoped worker rejects unrelated app/security queue rows.
	mailService := email.NewNewsletter(email.Queue{Store: db, ProofKeys: cfg.keys, Lifecycle: journal}, cfg.mail)
	var failed atomic.Bool
	var workers sync.WaitGroup
	for _, run := range []func(context.Context) error{
		service.Run,
		func(ctx context.Context) error {
			return service.RunSweep(ctx, journal, cfg.settings.PendingDays, cfg.settings.WithdrawnDays, deletion)
		},
		func(ctx context.Context) error { mailService.Run(ctx); return nil },
	} {
		workers.Go(func() { newsletterWorker(ctx, &failed, run) })
	}
	defer func() { cancel(); workers.Wait() }()
	checks := func(ctx context.Context) map[string]bool {
		return newsletterRoleChecks(cfg.settings, journal.CheckReady(ctx) == nil && db.SQL.PingContext(ctx) == nil, mailService.Ready(ctx), true, failed.Load())
	}
	ready := func(ctx context.Context) bool {
		for _, value := range checks(ctx) {
			if !value {
				return false
			}
		}
		return true
	}
	operational := func(ctx context.Context) bool {
		for key, value := range checks(ctx) {
			if key != "collectionEnabled" && !value {
				return false
			}
		}
		return true
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	api := httpapi.New(db, nil, httpapi.Config{Newsletter: service, NewsletterSettings: cfg.settings, NewsletterReady: ready, NewsletterChecks: checks, Lifecycle: journal, AccountReady: func(ctx context.Context) bool { return journal.CheckReady(ctx) == nil }, EmailReady: operational, EmailRateKey: cfg.rateKey, Origin: cfg.app.Origin, Secure: true, Version: version}, logger)
	server := &http.Server{Addr: cfg.app.Addr, Handler: api.NewsletterHandler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	listener, err := net.Listen("tcp", cfg.app.Addr)
	if err != nil {
		return errNewsletterRole
	}
	defer func() { _ = listener.Close() }()
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		shutdown, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if server.Shutdown(shutdown) != nil {
			_ = server.Close()
		}
	}()
	logger.Info("newsletter service starting", "version", version)
	err = server.Serve(listener)
	cancel()
	<-done
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return errNewsletterRole
	}
	return nil
}
