package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/LowGok/my-chef/internal/email/mailqueue"
	"github.com/LowGok/my-chef/internal/newsletter"
	"github.com/LowGok/my-chef/internal/store"
)

func TestNewsletterCommandRejectsMissingAuthority(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"export"}, {"erase", "--db", "missing.db", "--user-id", "1"}} {
		var output, diagnostics bytes.Buffer
		if code := runNewsletter(args, strings.NewReader(`{}`), &output, &diagnostics); code != 2 {
			t.Fatalf("invalid command returned %d", code)
		}
		if output.Len() != 0 {
			t.Fatal("invalid request emitted contact data")
		}
	}
}

func TestNewsletterCommandExportsProtectedFileAndErasesContact(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "contact.db")
	db, err := store.Open(dbPath, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	rateKey := bytes.Repeat([]byte{8}, 32)
	proofKey := bytes.Repeat([]byte{7}, 32)
	keys, err := mailqueue.NewProofKeys("test", map[string][]byte{"test": proofKey})
	if err != nil {
		t.Fatal(err)
	}
	service := &newsletter.Service{Store: db, Keys: keys, RateKey: rateKey, Origin: "https://mychef.dev", ConsentVersion: "newsletter-v1"}
	if _, err := service.Request(t.Context(), "192.0.2.8", newsletter.Input{Email: "operator@example.test", Locale: "en", ConsentVersion: "newsletter-v1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	journal, err := db.InitializeLifecycleJournal(t.Context(), dbPath+".lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	var actor store.NewsletterContactAuthority
	if err := db.DB.Raw("SELECT u.user_id,u.email_key,a.incarnation FROM users u JOIN account_incarnations a ON a.user_id=u.user_id").Scan(&actor).Error; err != nil {
		t.Fatal(err)
	}
	actor.RequestReference = "verified-command-request"
	input, err := json.Marshal(actor)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	settings, _ := json.Marshal(map[string]any{"active": "test", "keys": map[string]string{"test": base64.StdEncoding.EncodeToString(proofKey)}, "rate_key": base64.StdEncoding.EncodeToString(rateKey)})
	keyFile := filepath.Join(dir, "proof-keys.json")
	if err := os.WriteFile(keyFile, settings, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MYCHEF_EMAIL_PROOF_KEYS_FILE", keyFile)
	flags := []string{"--db", dbPath, "--lifecycle-db", dbPath + ".lifecycle", "--user-id", strconv.FormatInt(actor.UserID, 10)}
	destination := filepath.Join(dir, "contact-export.json")
	var output, diagnostics bytes.Buffer
	args := append([]string{"export"}, flags...)
	args = append(args, "--output", destination)
	if code := runNewsletter(args, bytes.NewReader(input), &output, &diagnostics); code != 0 {
		t.Fatalf("export code %d: %s", code, diagnostics.String())
	}
	file, err := os.Stat(destination)
	if err != nil || file.Mode().Perm() != 0o600 {
		t.Fatal("unprotected export file", err)
	}
	data, err := os.ReadFile(destination)
	if err != nil || !bytes.Contains(data, []byte("operator@example.test")) {
		t.Fatal("missing owned contact data", err)
	}
	if strings.Contains(output.String()+diagnostics.String(), "operator@example.test") {
		t.Fatal("email in operator diagnostics")
	}
	if code := runNewsletter(args, bytes.NewReader(input), &output, &diagnostics); code == 0 {
		t.Fatal("existing export was overwritten")
	}
	output.Reset()
	diagnostics.Reset()
	if code := runNewsletter(append([]string{"erase"}, flags...), bytes.NewReader(input), &output, &diagnostics); code != 0 {
		t.Fatalf("erase code %d: %s", code, diagnostics.String())
	}
	check, err := store.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = check.Close() }()
	var remaining int
	if err := check.SQL.QueryRow("SELECT count(*) FROM users").Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal("CLI left the contact behind", err)
	}
}
