package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/config"
	"github.com/LowGok/my-chef/internal/importer"
	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

func main() {
	syscall.Umask(0o077)
	if err := run(); err != nil {
		var detail *importer.Error
		if errors.As(err, &detail) {
			_ = json.NewEncoder(os.Stderr).Encode(detail)
		}
		fmt.Fprintln(os.Stderr, "Migration failed; database was not approved for service. Check the local database and migration state.")
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: mychef-migrate up|import|backup|restore --db PATH")
	}
	flags := flag.NewFlagSet(os.Args[1], flag.ContinueOnError)
	path := flags.String("db", "", "SQLite path")
	sourceEnv := flags.String("source-env", "MYCHEF_POSTGRES_URL", "environment variable containing the PostgreSQL URL")
	schema := flags.String("schema", "public", "source schema")
	file := flags.String("file", "", "backup destination or restore source")
	lifecycle := flags.String("lifecycle-db", os.Getenv("MYCHEF_LIFECYCLE_DB"), "independent lifecycle journal path")
	if err := flags.Parse(os.Args[2:]); err != nil {
		return err
	}
	if *path == "" {
		return fmt.Errorf("--db is required")
	}
	if *lifecycle == "" {
		*lifecycle = *path + ".lifecycle"
	}
	if os.Args[1] == "backup" || os.Args[1] == "restore" {
		if *file == "" {
			return fmt.Errorf("--file is required")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if os.Args[1] == "restore" {
			settings, err := config.LoadEmailProofKeys()
			if err != nil {
				return err
			}
			options := store.AccountRestoreOptions{JournalPath: *lifecycle, TraceRoot: os.Getenv("MYCHEF_TRACE_DIR"), Cleanup: func(ctx context.Context, tx *gorm.DB, u store.User) error {
				manager := &auth.Manager{RecoveryRateKey: settings.RateKey}
				return manager.EraseAccountLinks(ctx, tx, u, time.Now().UTC())
			}}
			if err := store.Restore(ctx, *file, *path, options); err != nil {
				return err
			}
		} else {
			s, err := store.OpenReadOnly(*path)
			if err != nil {
				return err
			}
			defer func() { _ = s.Close() }()
			journal, err := s.OpenLifecycleJournalReadOnly(ctx, *lifecycle)
			if err != nil {
				return err
			}
			_ = journal.Close()
			if err = s.Backup(ctx, *file); err != nil {
				return err
			}
		}
		fmt.Println("Verified database snapshot published; existing files were not replaced.")
		return nil
	}
	if os.Args[1] == "import" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		source := os.Getenv(*sourceEnv)
		if source == "" {
			return fmt.Errorf("source environment variable is required")
		}
		report, err := importer.Import(ctx, source, *path, *schema)
		if err != nil {
			return err
		}
		s, err := store.Open(*path, false)
		if err != nil {
			return err
		}
		defer func() { _ = s.Close() }()
		if err = prepareLifecycle(ctx, s, *lifecycle); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(report)
	}
	if os.Args[1] != "up" {
		return fmt.Errorf("unsupported command")
	}
	s, err := store.Open(*path, true)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()
	unlock, err := store.LockApplication(s.Path)
	if err != nil {
		return err
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err = s.Migrate(ctx); err != nil {
		return err
	}
	if err = prepareLifecycle(ctx, s, *lifecycle); err != nil {
		return err
	}
	fmt.Println("Database schema and lifecycle journal are initialized.")
	return nil
}

func prepareLifecycle(ctx context.Context, s *store.Store, path string) error {
	journal, err := s.InitializeLifecycleJournal(ctx, path)
	if err != nil {
		return err
	}
	defer func() { _ = journal.Close() }()
	settings, err := config.LoadEmailProofKeys()
	if err != nil {
		return err
	}
	manager := &auth.Manager{RecoveryRateKey: settings.RateKey}
	opts := store.AccountDeletionOptions{Cleanup: func(tx *gorm.DB, u store.User) error { return manager.EraseAccountLinks(ctx, tx, u, time.Now().UTC()) }, Capture: func(tx *gorm.DB, u store.User, inc string) error {
		return journal.CaptureAccountTraces(ctx, tx, u.ID, inc, os.Getenv("MYCHEF_TRACE_DIR"))
	}}
	return journal.ReconcileAccountDeletions(ctx, opts)
}
