package newsletter

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/email/mailqueue"
	"github.com/LowGok/my-chef/internal/store"
)

func fixture(t *testing.T) (*Service, *time.Time) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "newsletter.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	keys, err := mailqueue.NewProofKeys("test", map[string][]byte{"test": bytes.Repeat([]byte{7}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	s := &Service{Store: db, Keys: keys, RateKey: bytes.Repeat([]byte{8}, 32), Origin: "https://mychef.dev", ConsentVersion: "newsletter-v1", Now: func() time.Time { return now }}
	t.Cleanup(func() { _ = s.Store.Close() })
	return s, &now
}

func request(t *testing.T, s *Service, address, locale string) {
	t.Helper()
	if wait, err := s.Request(t.Context(), "192.0.2.1", Input{Email: address, Locale: locale, ConsentVersion: s.ConsentVersion}); err != nil || wait != 0 {
		t.Fatalf("request: %d %v", wait, err)
	}
}

func step(t *testing.T, s *Service) {
	t.Helper()
	if worked, err := s.Step(t.Context()); !worked || err != nil {
		t.Fatalf("worker: %v %v", worked, err)
	}
}

func count(t *testing.T, s *Service, table string) int {
	t.Helper()
	var value int
	if err := s.Store.SQL.QueryRow("SELECT count(*) FROM " + table).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestCaptureDurablePrivateRequestAndRestart(t *testing.T) {
	s, _ := fixture(t)
	request(t, s, "  New@Example.Test  ", "en")
	if count(t, s, "users") != 0 || count(t, s, "newsletter_requests") != 1 {
		t.Fatal("identity lookup happened during acknowledgement")
	}
	var payload string
	if err := s.Store.SQL.QueryRow("SELECT payload FROM newsletter_requests").Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(payload), "new@example.test") {
		t.Fatal("request address was not protected")
	}
	path := s.Store.Path
	if err := s.Store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	s.Store = db
	step(t, s)
	var u store.User
	if err := s.Store.DB.First(&u).Error; err != nil {
		t.Fatal(err)
	}
	if !u.IsNewsletterOnly || u.ID == 0 || u.EmailKey != "new@example.test" || u.Password != "" || u.EmailVerifiedAt != nil {
		t.Fatal("invalid contact identity")
	}
	if count(t, s, "newsletter_subscriptions") != 1 || count(t, s, "email_outbox") != 1 {
		t.Fatal("capture and outbox did not commit together")
	}
}

func TestCaptureExistingUserAndUnsolicitedRepeats(t *testing.T) {
	s, now := fixture(t)
	u := store.User{Email: "existing@example.test", Username: "existing", Password: "preserved", FirstName: "Existing", LastName: "Account"}
	if err := s.Store.DB.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	request(t, s, u.Email, "en")
	step(t, s)
	*now = now.Add(61 * time.Second)
	request(t, s, strings.ToUpper(u.Email), "nl-BE")
	step(t, s)
	if count(t, s, "users") != 1 || count(t, s, "email_outbox") != 1 || count(t, s, "newsletter_proofs") != 2 {
		t.Fatal("repeat changed identity or issued replacement proofs")
	}
	if err := s.Store.DB.Exec("UPDATE newsletter_subscriptions SET status='confirmed',proved_order=100 WHERE user_id=?", u.ID).Error; err != nil {
		t.Fatal(err)
	}
	*now = now.Add(61 * time.Second)
	request(t, s, u.Email, "fr-BE")
	step(t, s)
	var locale, status string
	if err := s.Store.SQL.QueryRow("SELECT locale,status FROM newsletter_subscriptions WHERE user_id=?", u.ID).Scan(&locale, &status); err != nil || locale != "en" || status != "confirmed" {
		t.Fatal("anonymous repeat changed owned state", err)
	}
	var kept store.User
	if err := s.Store.DB.First(&kept, u.ID).Error; err != nil || kept.IsNewsletterOnly || kept.Password != u.Password || kept.Username != u.Username {
		t.Fatal("existing account overwritten", err)
	}
}

func TestCaptureTransactionRollbackAndConcurrentDuplicates(t *testing.T) {
	s, _ := fixture(t)
	request(t, s, "race@example.test", "en")
	if err := s.Store.DB.Exec("CREATE TRIGGER fail_newsletter_mail BEFORE INSERT ON email_outbox BEGIN SELECT RAISE(ABORT,'injected'); END").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.Step(t.Context()); err == nil {
		t.Fatal("injected outbox failure accepted")
	}
	if count(t, s, "users") != 0 || count(t, s, "newsletter_proofs") != 0 || count(t, s, "newsletter_requests") != 1 {
		t.Fatal("partial capture survived rollback")
	}
	if err := s.Store.DB.Exec("DROP TRIGGER fail_newsletter_mail").Error; err != nil {
		t.Fatal(err)
	}
	step(t, s)
	var wg sync.WaitGroup
	errors := make(chan error, 20)
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Request(context.Background(), "192.0.2.2", Input{Email: "RACE@example.test", Locale: "en", ConsentVersion: s.ConsentVersion})
			errors <- err
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	for {
		worked, err := s.Step(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			break
		}
	}
	if count(t, s, "users") != 1 || count(t, s, "newsletter_subscriptions") != 1 || count(t, s, "email_outbox") != 1 {
		t.Fatal("concurrent duplicate identity or delivery")
	}
}

func TestCaptureRejectsInvalidConsentAndInput(t *testing.T) {
	s, _ := fixture(t)
	for _, input := range []Input{{Email: "bad", Locale: "en", ConsentVersion: s.ConsentVersion}, {Email: "a@example.test", Locale: "de", ConsentVersion: s.ConsentVersion}, {Email: "a@example.test", Locale: "en", ConsentVersion: "old"}} {
		if _, err := s.Request(t.Context(), "192.0.2.1", input); err == nil {
			t.Fatal("invalid request accepted")
		}
	}
	if count(t, s, "newsletter_requests") != 0 {
		t.Fatal("invalid request wrote data")
	}
}
