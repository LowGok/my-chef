package store

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func newsletterContactFixture(t *testing.T) (*Store, *LifecycleJournal, NewsletterContactAuthority) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "contact.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	u := User{Email: "privacy@example.test", Username: "internal-placeholder", IsNewsletterOnly: true}
	if err := s.DB.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	j, err := s.InitializeLifecycleJournal(t.Context(), s.Path+".lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	var incarnation string
	if err := s.SQL.QueryRow("SELECT incarnation FROM account_incarnations WHERE user_id=?", u.ID).Scan(&incarnation); err != nil {
		t.Fatal(err)
	}
	return s, j, NewsletterContactAuthority{UserID: u.ID, EmailKey: u.EmailKey, Incarnation: incarnation, RequestReference: "verified-test-request"}
}

func TestNewsletterOperatorExportAndErasure(t *testing.T) {
	s, j, actor := newsletterContactFixture(t)
	data, err := s.ExportNewsletterContact(t.Context(), actor)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if json.Unmarshal(data, &document) != nil || document["email"] != "privacy@example.test" {
		t.Fatal("wrong contact export")
	}
	if bytes.Contains(data, []byte("internal-placeholder")) || bytes.Contains(data, []byte("password")) {
		t.Fatal("credential fields in contact export")
	}
	result, err := j.EraseNewsletterContact(t.Context(), actor, bytes.Repeat([]byte{8}, 32), time.Now(), AccountDeletionOptions{})
	if err != nil || result.Operation == "" {
		t.Fatal("contact erasure failed", err)
	}
	var count int
	if err := s.SQL.QueryRow("SELECT count(*) FROM users").Scan(&count); err != nil || count != 0 {
		t.Fatal("contact survived", err)
	}
	if err := s.SQL.QueryRow("SELECT count(*) FROM newsletter_erasures").Scan(&count); err != nil || count != 1 {
		t.Fatal("missing stale-job cutoff", err)
	}
	var state string
	if err := j.store.SQL.QueryRow("SELECT state FROM lifecycle_deletions WHERE operation=?", result.Operation).Scan(&state); err != nil || state != "committed" {
		t.Fatal("missing independent deletion receipt", err)
	}
}

func TestNewsletterOperatorRejectsStaleOrPromotedIdentity(t *testing.T) {
	for _, scenario := range []string{"email", "incarnation", "promoted", "unverified-request"} {
		t.Run(scenario, func(t *testing.T) {
			s, j, actor := newsletterContactFixture(t)
			switch scenario {
			case "email":
				actor.EmailKey = "other@example.test"
			case "incarnation":
				actor.Incarnation = "00000000000000000000000000000000"
			case "promoted":
				if err := s.DB.Exec("UPDATE users SET isNewsletterOnly=0 WHERE user_id=?", actor.UserID).Error; err != nil {
					t.Fatal(err)
				}
			case "unverified-request":
				actor.RequestReference = ""
			}
			if _, err := s.ExportNewsletterContact(t.Context(), actor); err == nil {
				t.Fatal("invalid export authority accepted")
			}
			if _, err := j.EraseNewsletterContact(t.Context(), actor, bytes.Repeat([]byte{8}, 32), time.Now(), AccountDeletionOptions{}); err == nil {
				t.Fatal("invalid erase authority accepted")
			}
			var count int
			if err := s.SQL.QueryRow("SELECT count(*) FROM users").Scan(&count); err != nil || count != 1 {
				t.Fatal("refused request removed an identity")
			}
		})
	}
}
