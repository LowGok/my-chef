package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/LowGok/my-chef/internal/store"
)

func runAccountCleanupStatus(args []string) int {
	flags := flag.NewFlagSet("account-cleanup", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	db := flags.String("db", "", "path to the app SQLite database")
	journal := flags.String("lifecycle-db", "", "path to the independent lifecycle journal")
	limit := flags.Int("limit", 100, "maximum results, 1 to 100")
	after := flags.String("after", "", "only return opaque references after this reference")
	if flags.Parse(args) != nil || *db == "" || *journal == "" || *limit < 1 || *limit > 100 || flags.NArg() != 0 {
		return 2
	}
	fail := func() int { fmt.Fprintln(os.Stderr, "Could not read account cleanup status."); return 1 }
	s, err := store.OpenReadOnly(*db)
	if err != nil {
		return fail()
	}
	defer func() { _ = s.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	j, err := s.OpenLifecycleJournalReadOnly(ctx, *journal)
	if err != nil {
		return fail()
	}
	defer func() { _ = j.Close() }()
	rows, err := j.CleanupStatus(ctx, *limit, *after)
	if err != nil {
		return fail()
	}
	if err := json.NewEncoder(os.Stdout).Encode(rows); err != nil {
		return fail()
	}
	return 0
}
