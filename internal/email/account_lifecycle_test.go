package email

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/email/mailqueue"
	"github.com/LowGok/my-chef/internal/store"
)

func TestDeletionNoticeErasesSnapshotsAndTracksCreatingAccount(t *testing.T) {
	ctx := t.Context()
	s, err := store.Open(filepath.Join(t.TempDir(), "notice.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	user := store.User{FirstName: "Fixture", LastName: "User", Email: "old-address@example.test", Username: "owner", Password: "credential"}
	if err = s.DB.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	q := Queue{Store: s}
	req := Request{OwnerID: user.ID, Key: "deleted/fixture", Recipient: user.Email, Template: mailqueue.AccountDeletedTemplate, Fields: map[string]string{"timestamp": "2026-11-07T12:00:00Z"}}
	id, err := q.Enqueue(ctx, nil, req)
	if err != nil {
		t.Fatal(err)
	}
	var owner string
	if err = s.SQL.QueryRowContext(ctx, "SELECT incarnation FROM account_mail_owners WHERE outbox_id=?", id).Scan(&owner); err != nil || owner == "" {
		t.Fatal("notice lacks creating account", err)
	}
	provider := &noticeCapture{}
	w := Worker{onlyID: id, Queue: q, sender: provider, From: "MyChef <customer@example.test>", ReplyTo: "help@example.test", FingerprintKey: bytes.Repeat([]byte{7}, 32)}
	if worked, err := w.Step(ctx); err != nil || !worked {
		t.Fatal(err)
	}
	if !strings.Contains(string(provider.body), "account was deleted") || strings.Contains(string(provider.body), "reset-password") {
		t.Fatal("incorrect deletion notice")
	}
	var recipient, data string
	if err = s.SQL.QueryRowContext(ctx, "SELECT recipient,data FROM email_outbox WHERE id=?", id).Scan(&recipient, &data); err != nil || recipient != "" || data != "{}" {
		t.Fatal("final notice retained personal snapshot", err)
	}
	// Reuse of a delivery address cannot reassign an old notice's ownership.
	if err = s.DB.Model(&user).Updates(map[string]any{"email": "changed@example.test", "email_key": "changed@example.test"}).Error; err != nil {
		t.Fatal(err)
	}
	other := store.User{FirstName: "Other", LastName: "User", Email: "old-address@example.test", Username: "other", Password: "different"}
	if err = s.DB.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if err = store.LinkAccountMail(s.DB, other.ID, id); err == nil {
		t.Fatal("reused address reassigned old notice")
	}
}

func TestDeletionNoticeExpiryDoesNotNeedConfiguredSender(t *testing.T) {
	ctx := t.Context()
	s, err := store.Open(filepath.Join(t.TempDir(), "expiry.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	q := Queue{Store: s, Now: func() time.Time { return now }}
	id, err := q.Enqueue(ctx, nil, Request{Key: "deleted/expired", Recipient: "owner@example.test", Template: mailqueue.AccountDeletedTemplate, Fields: map[string]string{"timestamp": now.Format(time.RFC3339)}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SweepDeletionMail(ctx, now.Add(23*time.Hour-time.Second)); err != nil {
		t.Fatal(err)
	}
	var recipient, data, state string
	if err = s.SQL.QueryRowContext(ctx, "SELECT recipient FROM email_outbox WHERE id=?", id).Scan(&recipient); err != nil || recipient == "" {
		t.Fatal("unexpired queued notice erased", err)
	}
	if err = s.SweepDeletionMail(ctx, now.Add(23*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err = s.SQL.QueryRowContext(ctx, "SELECT recipient,data,state FROM email_outbox WHERE id=?", id).Scan(&recipient, &data, &state); err != nil || recipient != "" || data != "{}" || state != "failed" {
		t.Fatal("disabled sender retained expired snapshot", err)
	}
}
