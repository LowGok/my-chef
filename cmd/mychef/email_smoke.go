package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/LowGok/my-chef/internal/config"
	"github.com/LowGok/my-chef/internal/email"
	"github.com/LowGok/my-chef/internal/store"
)

func runEmailSmoke(args []string) int {
	if len(args) != 1 || args[0] != "--tracking-off-verified" {
		fmt.Fprintln(os.Stderr, "usage: mychef email-smoke --tracking-off-verified")
		return 2
	}
	cfg, err := config.LoadEmail()
	if err != nil {
		fmt.Fprintln(os.Stderr, "email configuration is unavailable")
		return 1
	}
	dir, err := os.MkdirTemp("", "mychef-email-smoke-")
	if err != nil {
		return 1
	}
	defer func() { _ = os.RemoveAll(dir) }()
	s, err := store.Open(filepath.Join(dir, "smoke.db"), true)
	if err != nil {
		return 1
	}
	defer func() { _ = s.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := s.Migrate(ctx); err != nil {
		return 1
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return 1
	}
	id := hex.EncodeToString(token[:])
	req := email.Request{Key: "mychef-smoke/" + id, Recipient: "delivered@resend.dev", Template: "provider-smoke-test/v1",
		Fields: map[string]string{"testID": id, "timestamp": time.Now().UTC().Format(time.RFC3339)}}
	if err := email.New(email.Queue{Store: s}, cfg).Smoke(ctx, req); err != nil {
		fmt.Fprintln(os.Stderr, "email smoke submission failed")
		return 1
	}
	fmt.Println("email smoke submission accepted")
	return 0
}
