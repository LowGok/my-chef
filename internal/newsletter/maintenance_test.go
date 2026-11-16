package newsletter

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/config"
	"github.com/LowGok/my-chef/internal/email/mailqueue"
	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

func TestMaintenanceRemovesExpiredAccountWorkAndKeepsAuthorizedMail(t *testing.T) {
	s, now := fixture(t)
	request(t, s, "expiry@example.test", "en")
	step(t, s)
	j := minimisationJournal(t, s)
	var user store.User
	if err := s.Store.DB.First(&user).Error; err != nil {
		t.Fatal(err)
	}
	*now = now.Add(25 * time.Hour)
	for _, query := range []string{
		"INSERT INTO signup_requests VALUES('expired-job',1,2,'expired-signup-body-canary')",
		"INSERT INTO signup_proofs VALUES('expired-proof','hash','tag',2,NULL,'expired-proof-body-canary')",
		"INSERT INTO email_verification_proofs VALUES('expired-verification',1,'verify-hash','tag',2,NULL)",
		"INSERT INTO pending_email_changes(id,user_id,original_email,original_key,proposed_email,proposed_key,expires) VALUES('expired-change',1,'expired-change-canary@example.test','expired-change-canary@example.test','next-expired@example.test','next-expired@example.test',2)",
		"INSERT INTO recovery_jobs VALUES(1,'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',2,'expired-recovery-body-canary')",
		"INSERT INTO recovery_proofs VALUES('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',1,1,'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',0,1,2,NULL)",
	} {
		if err := s.Store.DB.Exec(query).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := mailqueue.Enqueue(t.Context(), s.Store, nil, *now, mailqueue.Request{Key: "ordinary-control", Recipient: "control@example.test", Template: "provider-smoke-test/v1", Fields: map[string]string{"testID": "keep", "timestamp": now.Format(time.RFC3339)}}); err != nil {
		t.Fatal(err)
	}
	none := config.NewsletterRetention{NoExpiry: true}
	if err := s.Maintain(t.Context(), j, none, none, store.AccountDeletionOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"signup_requests", "signup_proofs", "email_verification_proofs", "pending_email_changes", "recovery_jobs", "recovery_proofs"} {
		if count(t, s, table) != 0 {
			t.Fatal("expired purpose-owned payload retained", table)
		}
	}
	var controls int
	if err := s.Store.SQL.QueryRowContext(t.Context(), "SELECT count(*) FROM email_outbox WHERE key='ordinary-control' AND recipient='control@example.test'").Scan(&controls); err != nil || controls != 1 {
		t.Fatal("authorized ordinary control changed", err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		body, err := os.ReadFile(s.Store.Path + suffix)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		for _, canary := range []string{"expired-signup-body-canary", "expired-proof-body-canary", "expired-change-canary@example.test", "expired-recovery-body-canary"} {
			if bytes.Contains(body, []byte(canary)) {
				t.Fatal("expired canary remains active", suffix)
			}
		}
	}
}

func TestMaintenanceDrainsBeyondFirstBatchAndSupportsNoSubscriptionContact(t *testing.T) {
	s, now := fixture(t)
	j := minimisationJournal(t, s)
	if err := j.CleanupNewsletterFiles(t.Context()); err != nil {
		t.Fatal(err)
	}
	var users []store.User
	if err := s.Store.NewsletterTransaction(t.Context(), j, func(tx *gorm.DB) error {
		for i := range 105 {
			u := store.User{Email: fmt.Sprintf("contact-%d@example.test", i), Username: fmt.Sprintf("opaque-%d", i), IsNewsletterOnly: true}
			if err := tx.Create(&u).Error; err != nil {
				return err
			}
			users = append(users, u)
		}
		return tx.Exec("INSERT INTO email_verification_proofs VALUES('live',?,'live-hash','live-tag',?,NULL)", users[0].ID, now.Add(time.Hour).Unix()).Error
	}); err != nil {
		t.Fatal(err)
	}
	none := config.NewsletterRetention{NoExpiry: true}
	if err := s.Maintain(t.Context(), j, none, none, store.AccountDeletionOptions{}); err != nil {
		t.Fatal(err)
	}
	var reduced int
	if err := s.Store.SQL.QueryRowContext(t.Context(), "SELECT count(*) FROM users WHERE newsletter_minimised=1").Scan(&reduced); err != nil || reduced != 104 {
		t.Fatal("live first row or first batch starved later contacts", reduced, err)
	}
	address := users[104].Email
	request(t, s, address, "en")
	step(t, s)
	var rawKeys int
	if err := s.Store.SQL.QueryRowContext(t.Context(), "SELECT count(*) FROM newsletter_proofs WHERE user_id=? AND purpose='unsubscribe' AND email_key IS NOT NULL", users[104].ID).Scan(&rawKeys); err != nil || rawKeys != 0 {
		t.Fatal("new reduced withdrawal retained clear mailbox", err)
	}
	var rawSub int
	if err := s.Store.SQL.QueryRowContext(t.Context(), "SELECT count(*) FROM newsletter_subscriptions WHERE user_id=? AND (email_key IS NOT NULL OR locale IS NOT NULL OR source IS NOT NULL OR requested_unix IS NOT NULL)", users[104].ID).Scan(&rawSub); err != nil || rawSub != 0 {
		t.Fatal("temporary request recreated permanent clear subscription metadata", err)
	}
	*now = now.Add(25 * time.Hour)
	if err := s.Maintain(t.Context(), j, none, none, store.AccountDeletionOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		body, err := os.ReadFile(s.Store.Path + suffix)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if strings.Contains(string(body), address) {
			t.Fatal("expired re-request address still active", suffix)
		}
	}
}
