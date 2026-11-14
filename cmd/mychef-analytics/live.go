package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"slices"
	"time"

	"github.com/LowGok/my-chef/internal/analytics"
	"github.com/LowGok/my-chef/internal/auth"
	appconfig "github.com/LowGok/my-chef/internal/config"
	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

func liveCommand(ctx context.Context, args []string, out, errOut io.Writer) error {
	if len(args) == 0 {
		return errors.New("live operation required")
	}
	op := args[0]
	if !slices.Contains([]string{"init", "validate", "maintenance", "refresh", "report", "backup", "restore", "backup-app", "restore-app"}, op) {
		return analytics.ErrInput
	}
	flags := flag.NewFlagSet("live "+op, flag.ContinueOnError)
	flags.SetOutput(errOut)
	config := flags.String("config", "", "private operator configuration")
	appPath := flags.String("app", "", "migrated app SQLite source")
	copyPath := flags.String("copy", "", "registered extract path")
	lifecycle := flags.String("lifecycle-db", os.Getenv("MYCHEF_LIFECYCLE_DB"), "independent account lifecycle journal")
	destination := flags.String("destination", "", "fresh registered backup or restore destination")
	monthly := flags.Bool("monthly", false, "thirteen-month aggregate output")
	start := flags.Int64("start", 0, "UTC start second")
	end := flags.Int64("end", 0, "UTC exclusive end second")
	if e := flags.Parse(args[1:]); e != nil {
		return e
	}
	if flags.NArg() != 0 || *config == "" || *appPath == "" {
		return analytics.ErrInput
	}
	opts, e := analytics.LoadRuntime(*config)
	if e != nil {
		return e
	}
	if op == "init" {
		opts.Create = true
	}
	if op == "restore-app" {
		if *lifecycle == "" || *copyPath == "" {
			return analytics.ErrInput
		}
		settings, e := appconfig.LoadEmailProofKeys()
		if e != nil {
			return e
		}
		return store.Restore(ctx, *copyPath, *appPath, store.AccountRestoreOptions{JournalPath: *lifecycle, TraceRoot: os.Getenv("MYCHEF_TRACE_DIR"), Analytics: &opts, Cleanup: func(ctx context.Context, tx *gorm.DB, u store.User) error {
			manager := auth.Manager{RecoveryRateKey: settings.RateKey}
			return manager.EraseAccountLinks(ctx, tx, u, time.Now().UTC())
		}})
	}
	app, e := store.Open(*appPath, false)
	if e != nil {
		return analytics.ErrPrivacy
	}
	defer func() { _ = app.Close() }()
	if e = app.CheckSchema(ctx); e != nil {
		return e
	}
	s, e := analytics.Open(ctx, app.DB, opts)
	if e != nil {
		return e
	}
	defer func() { _ = s.Close() }()
	app.Analytics = s
	switch op {
	case "init":
		return json.NewEncoder(out).Encode(map[string]string{"status": "initialized", "collection": "disabled until assessed configuration and activation approval"})
	case "validate":
		if e = s.Maintain(ctx); e != nil {
			return e
		}
		return json.NewEncoder(out).Encode(map[string]string{"status": "valid", "environment": "production"})
	case "maintenance":
		return s.Maintain(ctx)
	case "refresh":
		if e = s.Deliver(ctx, nil); e != nil {
			return e
		}
		path, e := s.Refresh(ctx)
		if e != nil {
			return e
		}
		return json.NewEncoder(out).Encode(map[string]string{"extract": path})
	case "backup-app":
		if *lifecycle == "" {
			*lifecycle = *appPath + ".lifecycle"
		}
		journal, e := app.OpenLifecycleJournalReadOnly(ctx, *lifecycle)
		if e != nil {
			return e
		}
		if e = journal.Close(); e != nil {
			return e
		}
		return app.Backup(ctx, *destination)
	case "backup":
		return s.Backup(ctx, *copyPath, *destination)
	case "restore":
		return s.Restore(ctx, *copyPath, *destination)
	case "report":
		if *copyPath == "" {
			*copyPath, e = s.Current(ctx)
			if e != nil {
				return e
			}
		}
		if *monthly {
			return s.WithMonthlyReport(ctx, *copyPath, out)
		}
		return s.WithReport(ctx, *copyPath, *start, *end, out)
	default:
		return analytics.ErrInput
	}
}
