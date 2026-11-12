package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/config"
	"github.com/LowGok/my-chef/internal/email"
	"github.com/LowGok/my-chef/internal/email/mailqueue"
	httpapi "github.com/LowGok/my-chef/internal/http"
	"github.com/LowGok/my-chef/internal/imports"
	"github.com/LowGok/my-chef/internal/menus"
	"github.com/LowGok/my-chef/internal/providers"
	"github.com/LowGok/my-chef/internal/store"
	webui "github.com/LowGok/my-chef/web"
	"gorm.io/gorm"
)

var version = "dev"

func main() {
	syscall.Umask(0o077)
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version":
			fmt.Println(version)
			return
		case "account-cleanup":
			os.Exit(runAccountCleanupStatus(os.Args[2:]))
		case "import-photo-normalize":
			os.Exit(runImportPhotoNormalize(os.Args[2:]))
		case "photo-normalize":
			os.Exit(runPhotoNormalize(os.Args[2:]))
		case "feedback-failures":
			os.Exit(runFeedbackFailures(os.Args[2:]))
		case "email-smoke":
			os.Exit(runEmailSmoke(os.Args[2:]))
		case "healthcheck":
			address := os.Getenv("MYCHEF_ADDR")
			if address == "" {
				address = "0.0.0.0:8080"
			}
			_, port, err := net.SplitHostPort(address)
			if err != nil {
				os.Exit(1)
			}
			client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			response, err := client.Get("http://127.0.0.1:" + port + "/readyz")
			if err != nil {
				os.Exit(1)
			}
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				os.Exit(1)
			}
			return
		default:
			os.Exit(2)
		}
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		if a.Key == slog.TimeKey {
			a.Value = slog.TimeValue(a.Value.Time().UTC())
		}
		return a
	}}))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	emailCfg, err := config.LoadEmail()
	if err != nil {
		logger.Error("invalid email configuration", "error", err)
		os.Exit(1)
	}
	inboxSettings, err := config.LoadFixtureInbox(cfg, emailCfg, time.Now())
	if err != nil {
		logger.Error("invalid fixture inbox configuration")
		os.Exit(1)
	}
	fixturePhotoDirectory, err := config.LoadFixturePhotoDirectory(cfg, inboxSettings)
	if err != nil {
		logger.Error("invalid fixture photo configuration")
		os.Exit(1)
	}
	var fixtureInbox *email.FixtureInbox
	if inboxSettings != nil {
		fixtureInbox = email.NewFixtureInbox(*inboxSettings, nil)
	}
	proofSettings, err := config.LoadEmailProofKeys()
	if err != nil {
		logger.Error("invalid email proof configuration")
		os.Exit(1)
	}
	var proofKeys *mailqueue.ProofKeys
	if proofSettings.Active != "" {
		proofKeys, err = mailqueue.NewProofKeys(proofSettings.Active, proofSettings.Keys)
		if err != nil {
			logger.Error("invalid email proof configuration")
			os.Exit(1)
		}
	}
	host, _, _ := net.SplitHostPort(cfg.Addr)
	if emailCfg.Fake && host != "localhost" && !net.ParseIP(host).IsLoopback() {
		logger.Error("fake email delivery requires a loopback listener")
		os.Exit(1)
	}
	s, err := store.Open(cfg.Database, false)
	if err != nil {
		logger.Error("database unavailable; run the explicit migration first")
		os.Exit(1)
	}
	defer func() { _ = s.Close() }()
	unlock, err := store.LockApplication(s.Path)
	if err != nil {
		logger.Error("another application or migration holds the database")
		os.Exit(1)
	}
	defer unlock()
	if err = s.CheckSchema(context.Background()); err != nil {
		logger.Error("database schema is not ready")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	journal, err := s.OpenLifecycleJournal(ctx, cfg.LifecycleDatabase)
	if err != nil {
		logger.Error("account lifecycle journal unavailable; initialization or reconciliation required")
		os.Exit(1)
	}
	defer func() { _ = journal.Close() }()
	unlockJournal, err := store.LockApplication(journal.Path)
	if err != nil {
		logger.Error("another application holds the lifecycle journal")
		os.Exit(1)
	}
	defer unlockJournal()
	accountManager := auth.New(s)
	accountManager.RecoveryRateKey = proofSettings.RateKey
	deletion := store.AccountDeletionOptions{Cleanup: func(tx *gorm.DB, u store.User) error {
		return accountManager.EraseAccountLinks(ctx, tx, u, time.Now().UTC())
	}, Capture: func(tx *gorm.DB, u store.User, inc string) error {
		return journal.CaptureAccountTraces(ctx, tx, u.ID, inc, os.Getenv("MYCHEF_TRACE_DIR"))
	}}
	if err := journal.ReconcileAccountDeletions(ctx, deletion); err != nil {
		logger.Error("account deletion reconciliation required before serving")
		os.Exit(1)
	}
	if err := s.SweepDeletionMail(ctx, time.Now().UTC()); err != nil {
		logger.Error("account notice cleanup unavailable")
		os.Exit(1)
	}
	var cleaner store.AccountAssetCleaner
	var personalPhotos interface {
		menus.PersonalPhotoUploader
		httpapi.PersonalPhotoReader
	}
	assetBinding := "fixture"
	var generator providers.Generator
	var images providers.Images
	model := "fixture"
	if cfg.Provider == "fixture" {
		generator = providers.Fixture{}
		images = providers.Fixture{}
		if fixturePhotoDirectory != "" {
			photos, e := providers.NewFixturePersonalPhotoFiles(fixturePhotoDirectory)
			if e != nil {
				logger.Error("fixture photo storage unavailable")
				os.Exit(1)
			}
			defer func() { _ = photos.Close() }()
			personalPhotos, cleaner = photos, photos
		} else {
			photos := providers.NewFixturePersonalPhotos()
			personalPhotos, cleaner = photos, photos
		}
	} else {
		credentials, e := providers.LoadCredentials()
		if e != nil {
			logger.Error("provider credentials are unavailable")
			os.Exit(1)
		}
		remote, e := providers.NewRemote(credentials, providers.RemoteOptions{TraceDir: os.Getenv("MYCHEF_TRACE_DIR"), Logger: logger, TraceRetention: cfg.TraceRetention})
		if e != nil {
			logger.Error("provider configuration is incomplete")
			os.Exit(1)
		}
		generator = remote
		images = remote
		cleaner = remote
		personalPhotos = remote
		assetBinding = remote.AssetBinding()
		model = credentials.RecipeModel
	}
	personalReader := store.EntitlementReader(store.FixtureEntitlementLease{}, nil)
	var personalFixture http.Handler
	if inboxSettings != nil {
		fixture := httpapi.NewPersonalFixture(s, *inboxSettings, nil)
		personalReader, personalFixture = fixture.ReadEntitlement, fixture
	}
	executable, err := os.Executable()
	if err != nil {
		logger.Error("photo normalizer unavailable")
		os.Exit(1)
	}
	var recipeImports *httpapi.RecipeImportService
	importSettings, err := config.LoadRecipeImportClient()
	if err != nil {
		logger.Error("invalid private import configuration")
		os.Exit(1)
	}
	if err := s.SweepRecipeImports(ctx, time.Now().UTC(), true); err != nil {
		logger.Error("recipe import cleanup unavailable")
		os.Exit(1)
	}
	if importSettings != nil {
		vault, e := imports.NewDraftVault(proofSettings.RateKey)
		if e != nil {
			logger.Error("private import draft keys unavailable")
			os.Exit(1)
		}
		client, e := imports.NewWorkerClient(importSettings.Socket, importSettings.ChannelKey)
		clear(importSettings.ChannelKey)
		if e != nil {
			logger.Error("private import worker configuration unavailable")
			os.Exit(1)
		}
		spool, e := imports.NewImageSpool(importSettings.TempDir, imports.DefaultImageLimits())
		if e != nil {
			logger.Error("private import temporary storage unavailable")
			os.Exit(1)
		}
		defer func() { _ = spool.Close() }()
		normalizer := providers.NewImportPhotoNormalizer(executable)
		recipeImports, e = httpapi.NewRecipeImportService(ctx, s, journal, personalReader, vault, client, spool, func(call context.Context, raw []byte) ([]byte, error) {
			photo, e := normalizer.Normalize(call, raw)
			if e != nil {
				return nil, e
			}
			return photo.JPEG(), nil
		}, cfg.Provider == "fixture" && inboxSettings != nil)
		if e != nil {
			logger.Error("private recipe import service unavailable")
			os.Exit(1)
		}
		defer recipeImports.Close()
	}
	personalRecipes := menus.NewPersonalRecipes(s, journal, personalReader, providers.NewPersonalPhotoNormalizer(executable), personalPhotos)
	service, err := menus.New(s, generator, images, menus.Options{Logger: logger, Model: model, Lifecycle: journal, AssetProvider: assetBinding, TraceDir: os.Getenv("MYCHEF_TRACE_DIR"), PersonalEntitlements: personalReader})
	if err != nil {
		logger.Error("generation service unavailable")
		os.Exit(1)
	}
	defer service.Close()
	signUp := &auth.SignUp{Store: s, Keys: proofKeys, RateKey: proofSettings.RateKey, Origin: cfg.Origin,
		AllowLoopbackLink: inboxSettings != nil && inboxSettings.AllowLoopbackLinks}
	recoveryWork := &store.RecoveryWork{}
	recovery := &auth.Recovery{SignUp: signUp, Work: recoveryWork}
	if err := recovery.Sweep(ctx); err != nil {
		logger.Error("password recovery startup cleanup failed")
		os.Exit(1)
	}
	mailService := email.NewWithFixtureInbox(email.Queue{Store: s, ProofKeys: proofKeys, RecoveryWork: recoveryWork}, emailCfg, fixtureInbox)
	emailCtx, cancelEmail := context.WithCancel(ctx)
	emailDone := make(chan struct{})
	go func() {
		defer close(emailDone)
		mailService.Run(emailCtx)
		if emailCtx.Err() == nil {
			logger.Error("email worker stopped")
		}
	}()
	var signUpFailed atomic.Bool
	signUpDone := make(chan struct{})
	if proofKeys != nil {
		go func() {
			defer close(signUpDone)
			if err := signUp.Run(emailCtx); err != nil && emailCtx.Err() == nil {
				signUpFailed.Store(true)
				logger.Error("sign-up worker stopped")
			}
		}()
	} else {
		close(signUpDone)
	}
	var recoveryFailed atomic.Bool
	var recoveryWorkers sync.WaitGroup
	startRecovery := func(name string, run func(context.Context) error) {
		recoveryWorkers.Go(func() {
			if err := run(emailCtx); err != nil && emailCtx.Err() == nil {
				recoveryFailed.Store(true)
				logger.Error(name)
			}
		})
	}
	if proofKeys != nil {
		startRecovery("password recovery worker stopped", recovery.Run)
	}
	startRecovery("password recovery cleanup stopped", recovery.RunSweep)
	var lifecycleFailed atomic.Bool
	startLifecycle := func(name string, run func(context.Context) error) {
		recoveryWorkers.Go(func() {
			if err := run(emailCtx); err != nil && emailCtx.Err() == nil {
				lifecycleFailed.Store(true)
				logger.Error(name)
			}
		})
	}
	startLifecycle("account asset cleanup stopped", func(ctx context.Context) error { return journal.RunCleanup(ctx, cleaner) })
	startLifecycle("account notice cleanup stopped", s.RunDeletionMailSweep)
	if recipeImports != nil {
		recoveryWorkers.Go(func() {
			if err := recipeImports.RunSweep(emailCtx); err != nil && emailCtx.Err() == nil {
				logger.Error("recipe import cleanup stopped")
			}
		})
	} else {
		recoveryWorkers.Go(func() {
			if err := s.RunRecipeImportExpiry(emailCtx); err != nil && emailCtx.Err() == nil {
				logger.Error("recipe import cleanup stopped")
			}
		})
	}
	defer func() {
		cancelEmail()
		<-emailDone
		<-signUpDone
		recoveryWorkers.Wait()
	}()
	api := httpapi.New(s, service, httpapi.Config{
		Origin: cfg.Origin, Secure: cfg.Secure, Version: version,
		Lifecycle: journal, TraceDir: os.Getenv("MYCHEF_TRACE_DIR"), AccountMail: !emailCfg.Disabled,
		AccountReady: func(ctx context.Context) bool { return !lifecycleFailed.Load() && journal.CheckAvailable(ctx) == nil },
		EmailWebhook: mailService.Webhook(), EmailReady: func(ctx context.Context) bool {
			return !lifecycleFailed.Load() && journal.CheckAvailable(ctx) == nil && !signUpFailed.Load() && !recoveryFailed.Load() && mailService.Ready(ctx)
		},
		FixtureInbox: mailService.FixtureInbox(), TrustedProxies: cfg.TrustedProxies, EmailRateKey: proofSettings.RateKey, SignUp: signUp,
		Verification:    &auth.EmailVerification{SignUp: signUp},
		EmailChange:     &auth.EmailChange{SignUp: signUp},
		Recovery:        recovery,
		FeedbackTo:      config.LoadFeedbackRecipient(),
		PersonalFixture: personalFixture, PersonalEntitlements: personalReader,
		PersonalRecipes: personalRecipes, PersonalPhotos: personalPhotos, RecipeImports: recipeImports,
	}, logger)
	srv := &http.Server{Addr: cfg.Addr, Handler: api.Handler(webui.Handler()), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	shutdownDone := make(chan struct{})
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(c)
		close(shutdownDone)
	}()
	logger.Info("mychef starting", "address", cfg.Addr, "version", version)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
	if ctx.Err() != nil {
		<-shutdownDone
	}
}
