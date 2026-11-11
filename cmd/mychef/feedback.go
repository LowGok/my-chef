package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/LowGok/my-chef/internal/store"
)

func runFeedbackFailures(args []string) int {
	flags := flag.NewFlagSet("feedback-failures", flag.ContinueOnError)
	db := flags.String("db", "", "path to the app SQLite database")
	before := flags.Int64("before-id", 0, "only return feedback IDs before this ID")
	if flags.Parse(args) != nil || *db == "" || *before < 0 || flags.NArg() != 0 {
		return 2
	}
	s, err := store.OpenReadOnly(*db)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Could not read feedback delivery status.")
		return 1
	}
	defer func() { _ = s.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.CheckSchema(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "Could not read feedback delivery status.")
		return 1
	}
	rows, err := s.FeedbackDeliveryFailures(ctx, *before)
	if err != nil || json.NewEncoder(os.Stdout).Encode(rows) != nil {
		fmt.Fprintln(os.Stderr, "Could not read feedback delivery status.")
		return 1
	}
	return 0
}
