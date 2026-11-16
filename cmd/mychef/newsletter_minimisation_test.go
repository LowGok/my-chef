package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/config"
	"github.com/LowGok/my-chef/internal/email/mailqueue"
	"github.com/LowGok/my-chef/internal/newsletter"
	"github.com/LowGok/my-chef/internal/store"
)

func prepareNewsletterMinimisationRole(t *testing.T, path string, seed bool) {
	t.Helper()
	db, err := store.Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if seed {
		user := store.User{Email: "legacy-role@example.test", Username: "legacy-opaque", IsNewsletterOnly: true}
		if err := db.DB.Create(&user).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.DB.Exec("INSERT INTO newsletter_subscriptions(user_id,email_key,status,locale,consent_version,source,requested_unix) VALUES(?,'legacy-role@example.test','pending','en','newsletter-role-test','/blog/',?)", user.ID, time.Now().Add(-48*time.Hour).Unix()).Error; err != nil {
			t.Fatal(err)
		}
	}
	j, err := db.OpenLifecycleJournal(t.Context(), path+".lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	key, err := store.DeriveNewsletterIdentityKey(bytes.Repeat([]byte{8}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if err := j.BackfillNewsletterIdentities(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	if err := j.BackfillNewsletterIdentities(t.Context(), key); err != nil {
		t.Fatal("explicit migration retry failed", err)
	}
	keys, err := mailqueue.NewProofKeys("fixture", map[string][]byte{"fixture": bytes.Repeat([]byte{7}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	n := &newsletter.Service{Store: db, Keys: keys, RateKey: bytes.Repeat([]byte{8}, 32), Origin: "https://mychef.dev", ConsentVersion: "newsletter-role-test", Lifecycle: j}
	none := config.NewsletterRetention{NoExpiry: true}
	if err := n.Maintain(t.Context(), j, none, none, store.AccountDeletionOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := j.NewsletterFilesReady(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := j.ValidateNewsletterIdentities(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestNewsletterMinimisationRuntimeNeedsExplicitBinding(t *testing.T) {
	path := newsletterServeFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if err := serveNewsletter(ctx); !errors.Is(err, errNewsletterRole) {
		t.Fatal("runtime silently initialized an unbound matching namespace", err)
	}
	db, err := store.Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var count int
	if err := db.SQL.QueryRowContext(t.Context(), "SELECT count(*) FROM newsletter_identity_namespace").Scan(&count); err != nil || count != 0 {
		t.Fatal("refused runtime changed namespace binding", err)
	}
}

func TestNewsletterMinimisationExplicitBackfillAndMaintenance(t *testing.T) {
	path := newsletterServeFixture(t)
	prepareNewsletterMinimisationRole(t, path, true)
	db, err := store.Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var id int64
	var reduced bool
	var raw *string
	if err := db.SQL.QueryRowContext(t.Context(), "SELECT user_id,newsletter_minimised,email FROM users").Scan(&id, &reduced, &raw); err != nil || id != 1 || !reduced || raw != nil {
		t.Fatal("explicit backfill did not retain/minimise legacy contact", err)
	}
}

func TestNewsletterMinimisationRuntimeRejectsChangedKeyAndRetention(t *testing.T) {
	path := newsletterServeFixture(t)
	prepareNewsletterMinimisationRole(t, path, false)
	var profile map[string]any
	keyFile := os.Getenv("MYCHEF_EMAIL_PROOF_KEYS_FILE")
	original, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(original, &profile); err != nil {
		t.Fatal(err)
	}
	profile["rate_key"] = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))
	changed, _ := json.Marshal(profile)
	temporary := filepath.Join(t.TempDir(), "changed.json")
	if err := os.WriteFile(temporary, changed, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MYCHEF_EMAIL_PROOF_KEYS_FILE", temporary)
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	if err := serveNewsletter(ctx); !errors.Is(err, errNewsletterRole) {
		t.Fatal("changed purpose key served the existing namespace", err)
	}
	cancel()
	t.Setenv("MYCHEF_EMAIL_PROOF_KEYS_FILE", keyFile)
	for _, field := range []string{"MYCHEF_NEWSLETTER_PENDING_DAYS", "MYCHEF_NEWSLETTER_WITHDRAWN_DAYS"} {
		t.Run(field, func(t *testing.T) {
			t.Setenv(field, "30")
			if _, err := loadNewsletterRole(); err == nil {
				t.Fatal("newsletter-only minimized runtime accepted incompatible finite retention")
			}
		})
	}
}

func TestNewsletterMinimisationPausedRoleStillConfirmsAndWithdraws(t *testing.T) {
	path := newsletterServeFixture(t)
	prepareNewsletterMinimisationRole(t, path, false)
	db, err := store.Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	key, err := store.DeriveNewsletterIdentityKey(bytes.Repeat([]byte{8}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ConfigureNewsletterIdentity(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	j, err := db.OpenLifecycleJournal(t.Context(), path+".lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	keys, err := mailqueue.NewProofKeys("fixture", map[string][]byte{"fixture": bytes.Repeat([]byte{7}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	service := &newsletter.Service{Store: db, Keys: keys, RateKey: bytes.Repeat([]byte{8}, 32), Origin: "https://mychef.dev", ConsentVersion: "newsletter-role-test", Lifecycle: j}
	if _, err := service.Request(t.Context(), "192.0.2.95", newsletter.Input{Email: "paused-role@example.test", Locale: "en", ConsentVersion: service.ConsentVersion}); err != nil {
		t.Fatal(err)
	}
	if worked, err := service.Step(t.Context()); err != nil || !worked {
		t.Fatal(err)
	}
	var mail mailqueue.Record
	if err := db.DB.Where("key LIKE 'newsletter/%'").Take(&mail).Error; err != nil {
		t.Fatal(err)
	}
	fields, _, err := mailqueue.OpenProtected(keys, mail, time.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	confirm, err := url.Parse(fields["confirm"])
	if err != nil {
		t.Fatal(err)
	}
	withdraw, err := url.Parse(fields["unsubscribe"])
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MYCHEF_ADDR", address)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serveNewsletter(ctx) }()
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	deadline := time.Now().Add(15 * time.Second)
	for {
		response, err := client.Get("http://" + address + "/readyz")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == 200 {
				break
			}
		}
		select {
		case err := <-done:
			t.Fatal("paused runtime exited", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("paused role did not become operational")
		}
		time.Sleep(20 * time.Millisecond)
	}
	call := func(action, body string, want int) {
		request, err := http.NewRequestWithContext(t.Context(), "POST", "http://"+address+"/api/v1/newsletter/"+action, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+strings.Repeat("s", 64))
		request.Header.Set("Origin", "https://mychef.dev")
		request.Header.Set("X-MyChef-Newsletter-Source", "192.0.2.95")
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != want {
			t.Fatalf("paused %s returned %d, wanted %d", action, response.StatusCode, want)
		}
	}
	call("subscribe", `{"email":"another@example.test","locale":"en","consentVersion":"newsletter-role-test"}`, 503)
	body, _ := json.Marshal(map[string]string{"proof": confirm.Fragment})
	call("confirm", string(body), 200)
	body, _ = json.Marshal(map[string]string{"proof": withdraw.Fragment})
	call("unsubscribe", string(body), 200)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("paused role did not shut down")
	}
}
