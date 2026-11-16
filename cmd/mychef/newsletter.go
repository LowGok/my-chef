package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/config"
	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

func runNewsletter(args []string, input io.Reader, output, diagnostics io.Writer) int {
	if len(args) == 1 && args[0] == "serve" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := serveNewsletter(ctx); err != nil {
			_, _ = fmt.Fprintln(diagnostics, "Newsletter service unavailable.")
			return 1
		}
		return 0
	}
	if len(args) == 0 || args[0] != "export" && args[0] != "erase" {
		return 2
	}
	flags := flag.NewFlagSet("newsletter", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	database := flags.String("db", "", "application database")
	journalPath := flags.String("lifecycle-db", "", "lifecycle journal")
	userID := flags.Int64("user-id", 0, "verified contact user ID")
	destination := flags.String("output", "", "protected export file")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || *database == "" || *journalPath == "" || *userID <= 0 || args[0] == "export" && *destination == "" || args[0] == "erase" && *destination != "" {
		return 2
	}
	fail := func() int {
		_, _ = fmt.Fprintln(diagnostics, "Newsletter contact operation unavailable.")
		return 1
	}
	raw, err := io.ReadAll(io.LimitReader(input, 4097))
	if err != nil || len(raw) > 4096 {
		return fail()
	}
	var actor store.NewsletterContactAuthority
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&actor) != nil || decoder.Decode(new(any)) != io.EOF {
		return fail()
	}
	actor.UserID = *userID
	keys, err := config.LoadEmailProofKeys()
	if err != nil || len(keys.RateKey) != 32 {
		return fail()
	}
	db, err := store.Open(*database, false)
	if err != nil {
		return fail()
	}
	defer func() { _ = db.Close() }()
	unlock, err := store.LockApplication(db.Path)
	if err != nil {
		return fail()
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.CheckSchema(ctx); err != nil {
		return fail()
	}
	identityKey, err := store.DeriveNewsletterIdentityKey(keys.RateKey)
	if err != nil {
		return fail()
	}
	bound, err := db.ConfigureNewsletterIdentityIfBound(ctx, identityKey)
	if err != nil {
		return fail()
	}
	journal, err := db.OpenLifecycleJournal(ctx, *journalPath)
	if err != nil {
		return fail()
	}
	defer func() { _ = journal.Close() }()
	unlockJournal, err := store.LockApplication(journal.Path)
	if err != nil {
		return fail()
	}
	defer unlockJournal()
	manager := auth.New(db)
	manager.RecoveryRateKey = keys.RateKey
	opts := store.AccountDeletionOptions{Cleanup: func(tx *gorm.DB, user store.User) error {
		return manager.EraseAccountLinks(ctx, tx, user, time.Now().UTC())
	}}
	if err := journal.ReconcileAccountDeletions(ctx, opts); err != nil {
		return fail()
	}
	if err := journal.ReconcileNewsletterIdentityIntents(ctx); err != nil {
		return fail()
	}
	if err := journal.ValidateNewsletterIdentities(ctx); err != nil {
		return fail()
	}
	if bound {
		if err := journal.CleanupNewsletterFiles(ctx); err != nil {
			return fail()
		}
	}
	result := map[string]string{"userID": strconv.FormatInt(*userID, 10)}
	if args[0] == "export" {
		body, err := db.ExportNewsletterContact(ctx, actor)
		if err != nil {
			return fail()
		}
		file, err := os.OpenFile(*destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return fail()
		}
		written := false
		defer func() {
			_ = file.Close()
			if !written {
				_ = os.Remove(*destination)
			}
		}()
		if _, err := file.Write(append(body, '\n')); err != nil {
			return fail()
		}
		if file.Sync() != nil || file.Close() != nil {
			return fail()
		}
		written = true
		result["status"] = "exported"
	} else {
		deleted, err := journal.EraseNewsletterContact(ctx, actor, keys.RateKey, time.Now().UTC(), opts)
		if err != nil {
			return fail()
		}
		result["status"] = "erased"
		result["operation"] = deleted.Operation
	}
	if json.NewEncoder(output).Encode(result) != nil {
		return fail()
	}
	return 0
}
