package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/config"
	"github.com/LowGok/my-chef/internal/store"
)

func newsletterServeFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "role.db")
	db, err := store.Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	j, err := db.InitializeLifecycleJournal(t.Context(), path+".lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	key := func(v byte) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{v}, 32)) }
	for name, doc := range map[string]any{
		"credentials.json": map[string]string{"newsletter_service_token": strings.Repeat("s", 64), "newsletter_email_from": "MyChef <newsletter@mychef.dev>", "newsletter_email_reply_to": "feedback@gkldevelopment.com"},
		"keys.json":        map[string]any{"active": "fixture", "keys": map[string]string{"fixture": key(7)}, "rate_key": key(8)},
	} {
		raw, _ := json.Marshal(doc)
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for name, value := range map[string]string{
		"MYCHEF_ADDR": "127.0.0.1:0", "MYCHEF_DB": path, "MYCHEF_LIFECYCLE_DB": path + ".lifecycle", "MYCHEF_ORIGIN": "https://mychef.dev", "MYCHEF_DEV": "1", "MYCHEF_E2E": "1", "MYCHEF_PROVIDER": "fixture", "MYCHEF_EMAIL_MODE": "fake", "MYCHEF_CREDENTIALS": filepath.Join(dir, "credentials.json"), "MYCHEF_EMAIL_PROOF_KEYS_FILE": filepath.Join(dir, "keys.json"), "MYCHEF_NEWSLETTER_MODE": "fixture", "MYCHEF_NEWSLETTER_ORIGIN": "https://mychef.dev", "MYCHEF_NEWSLETTER_CONSENT_VERSION": "newsletter-role-test", "MYCHEF_NEWSLETTER_CONTACT": "feedback@gkldevelopment.com", "MYCHEF_NEWSLETTER_PENDING_DAYS": "none", "MYCHEF_NEWSLETTER_WITHDRAWN_DAYS": "none", "MYCHEF_NEWSLETTER_OPERATIONS_READY": "1", "MYCHEF_NEWSLETTER_COLLECT": "0",
	} {
		t.Setenv(name, value)
	}
	return path
}

func TestNewsletterRoleRefusesUnsafeConfiguration(t *testing.T) {
	newsletterServeFixture(t)
	if _, err := loadNewsletterRole(); err != nil {
		t.Fatal("valid role rejected", err)
	}
	for _, input := range []struct{ name, value string }{
		{"MYCHEF_ADDR", "0.0.0.0:8080"}, {"MYCHEF_ADDR", ":8080"}, {"MYCHEF_NEWSLETTER_MODE", "enabled"}, {"MYCHEF_NEWSLETTER_MODE", "disabled"}, {"MYCHEF_EMAIL_MODE", "disabled"}, {"MYCHEF_EMAIL_PROOF_KEYS_FILE", ""}, {"MYCHEF_NEWSLETTER_ORIGIN", "http://mychef.dev"}, {"MYCHEF_NEWSLETTER_ORIGIN", "https://mychef.dev/"}, {"MYCHEF_NEWSLETTER_PENDING_DAYS", ""}, {"MYCHEF_NEWSLETTER_CONTACT", ""}, {"MYCHEF_NEWSLETTER_OPERATIONS_READY", "0"},
	} {
		t.Run(input.name+input.value, func(t *testing.T) {
			t.Setenv(input.name, input.value)
			if _, err := loadNewsletterRole(); err == nil {
				t.Fatal("unsafe startup accepted")
			}
		})
	}
}

func TestNewsletterRoleReadinessAndWorkerFailure(t *testing.T) {
	settings := config.Newsletter{Mode: "enabled", ServiceToken: strings.Repeat("s", 64), Origin: "https://mychef.dev", ConsentVersion: "newsletter-role-test", Contact: "feedback@gkldevelopment.com", PendingDays: config.NewsletterRetention{NoExpiry: true}, WithdrawnDays: config.NewsletterRetention{NoExpiry: true}, OperationsReady: true}
	checks := newsletterRoleChecks(settings, true, true, true, false)
	for key, ready := range checks {
		if ready != (key != "collectionEnabled") {
			t.Fatalf("paused %s: %v", key, ready)
		}
	}
	settings.CollectionEnabled = true
	for key, ready := range newsletterRoleChecks(settings, true, true, true, false) {
		if !ready {
			t.Fatal("backend cannot become ready:", key)
		}
	}
	if newsletterChecks(settings, true, true, true, false)["identityCompatibility"] {
		t.Fatal("role weakened full app guard")
	}
	for _, run := range []func(context.Context) error{func(context.Context) error { return nil }, func(context.Context) error { panic("fixture") }} {
		var failed atomic.Bool
		newsletterWorker(t.Context(), &failed, run)
		if !failed.Load() {
			t.Fatal("unexpected worker exit not fenced")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var failed atomic.Bool
	newsletterWorker(ctx, &failed, func(context.Context) error { return nil })
	if failed.Load() {
		t.Fatal("normal shutdown failed")
	}
	if newsletterRoleChecks(settings, true, true, true, true)["worker"] {
		t.Fatal("worker failure accepted")
	}
}

func TestNewsletterRoleServesWithoutRecipeCredentialsAndReleasesLocks(t *testing.T) {
	path := newsletterServeFixture(t)
	// Reserve an available address for the actual HTTP runtime; retry-free local fixture.
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
	t.Cleanup(client.CloseIdleConnections)
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
			t.Fatal("runtime exited", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("runtime did not become operational")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if unlock, err := store.LockApplication(path); err == nil {
		unlock()
		t.Fatal("running database not exclusively held")
	}
	request, _ := http.NewRequestWithContext(t.Context(), "GET", "http://"+address+"/internal/newsletter/readiness", nil)
	request.Header.Set("Authorization", "Bearer "+strings.Repeat("s", 64))
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Ready  bool
		Checks map[string]bool
	}
	err = json.NewDecoder(response.Body).Decode(&body)
	_ = response.Body.Close()
	if err != nil || body.Ready || !body.Checks["identityCompatibility"] || body.Checks["collectionEnabled"] {
		t.Fatal("invalid paused preflight", err, body)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("shutdown not bounded")
	}
	for _, lockPath := range []string{path, path + ".lifecycle"} {
		unlock, err := store.LockApplication(lockPath)
		if err != nil {
			t.Fatal("shutdown held lock", err)
		}
		unlock()
	}
}

func TestNewsletterProductionRoleNeedsNoRecipeOrWebhookCredentials(t *testing.T) {
	newsletterServeFixture(t)
	var profile map[string]string
	if raw, err := os.ReadFile(os.Getenv("MYCHEF_CREDENTIALS")); err != nil {
		t.Fatal(err)
	} else if json.Unmarshal(raw, &profile) != nil {
		t.Fatal("profile")
	}
	profile["resend_api_key"] = "re_fake_sending_only_role_key"
	profile["email_fingerprint_key"] = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))
	raw, _ := json.Marshal(profile)
	if err := os.WriteFile(os.Getenv("MYCHEF_CREDENTIALS"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MYCHEF_NEWSLETTER_MODE", "enabled")
	t.Setenv("MYCHEF_EMAIL_MODE", "resend")
	t.Setenv("MYCHEF_PROVIDER", "")
	t.Setenv("MYCHEF_DEV", "")
	t.Setenv("MYCHEF_E2E", "")
	cfg, err := loadNewsletterRole()
	if err != nil || cfg.mail.Fake || len(cfg.mail.WebhookKey) != 0 || cfg.app.Provider != "gemini" {
		t.Fatal("sending-only production profile rejected", err)
	}
	for _, field := range []string{"newsletter_email_from", "newsletter_email_reply_to"} {
		t.Run(field, func(t *testing.T) {
			changed := map[string]string{}
			for key, value := range profile {
				changed[key] = value
			}
			changed[field] = "Other <other@example.test>"
			path := filepath.Join(t.TempDir(), "changed.json")
			raw, _ := json.Marshal(changed)
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("MYCHEF_CREDENTIALS", path)
			if _, err := loadNewsletterRole(); err == nil {
				t.Fatal("unapproved production profile accepted")
			}
		})
	}
}
