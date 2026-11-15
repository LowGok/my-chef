package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/email/mailqueue"
	"github.com/LowGok/my-chef/internal/newsletter"
	"github.com/LowGok/my-chef/internal/store"
)

// TestNewsletterRehearsalSeed creates private synthetic proof authority while
// the executable is stopped. It adds no runtime fixture or inbox route.
func TestNewsletterRehearsalSeed(t *testing.T) {
	dir := os.Getenv("MYCHEF_NEWSLETTER_REHEARSAL_DIR")
	if dir == "" {
		t.Skip("invoked only by scripts/test_newsletter_runtime.py")
	}
	if !filepath.IsAbs(dir) || !strings.HasPrefix(filepath.Base(dir), "mychef-newsletter-runtime-") {
		t.Fatal("invalid rehearsal directory")
	}
	cfg, err := loadNewsletterRole()
	if err != nil || cfg.settings.Mode != "fixture" || cfg.app.Database != filepath.Join(dir, "app.db") {
		t.Fatal("fixture boundary", err)
	}
	db, err := store.Open(cfg.app.Database, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	unlock, err := store.LockApplication(db.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	journal, err := db.OpenLifecycleJournal(t.Context(), cfg.app.LifecycleDatabase)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	unlockJ, err := store.LockApplication(journal.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer unlockJ()
	n := &newsletter.Service{Store: db, Keys: cfg.keys, RateKey: cfg.rateKey, Origin: cfg.settings.Origin, ConsentVersion: cfg.settings.ConsentVersion, Lifecycle: journal}
	output := map[string]any{}
	for _, name := range []string{"pending", "withdrawn", "erased"} {
		address := name + "@runtime.example.test"
		if wait, err := n.Request(t.Context(), "192.0.2.144", newsletter.Input{Email: address, Locale: "en", ConsentVersion: cfg.settings.ConsentVersion}); err != nil || wait != 0 {
			t.Fatal("seed request", err)
		}
		if worked, err := n.Step(t.Context()); err != nil || !worked {
			t.Fatal("seed processing", err)
		}
		var row mailqueue.Record
		if err := db.DB.Where("recipient=?", address).Take(&row).Error; err != nil {
			t.Fatal(err)
		}
		fields, _, err := mailqueue.OpenProtected(cfg.keys, row, time.Now(), false)
		if err != nil {
			t.Fatal(err)
		}
		var actor store.NewsletterContactAuthority
		if err := db.DB.Raw("SELECT u.user_id,u.email_key,a.incarnation FROM users u JOIN account_incarnations a ON a.user_id=u.user_id WHERE u.email_key=?", address).Scan(&actor).Error; err != nil {
			t.Fatal(err)
		}
		actor.RequestReference = "synthetic-process-rehearsal"
		item := map[string]any{"userID": actor.UserID, "authority": actor}
		for _, purpose := range []string{"confirm", "unsubscribe"} {
			_, token, ok := strings.Cut(fields[purpose], "#")
			if !ok {
				t.Fatal("no synthetic proof")
			}
			item[purpose] = token
		}
		// Hold seed delivery to inspect genuine protected proof actions separately
		// from the new HTTP capture's actual fake-provider dispatch.
		if err := db.DB.Model(&row).Update("next_at", time.Now().Add(time.Hour).Unix()).Error; err != nil {
			t.Fatal(err)
		}
		if name == "withdrawn" {
			if err := n.Unsubscribe(t.Context(), item["unsubscribe"].(string)); err != nil {
				t.Fatal(err)
			}
		}
		output[name] = item
	}
	raw, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("resend_api_key")) {
		t.Fatal("unexpected provider credential")
	}
	file, err := os.OpenFile(filepath.Join(dir, "authority.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Write(raw); err != nil {
		t.Fatal(err)
	}
}
