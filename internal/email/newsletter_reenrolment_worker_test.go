package email

import (
	"bytes"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/email/mailqueue"
	"github.com/LowGok/my-chef/internal/newsletter"
	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

func TestMinimisedNewsletterReenrolmentDispatch(t *testing.T) {
	ctx := t.Context()
	db, _, now, keys := protectedFixture(t)
	rateKey := bytes.Repeat([]byte{8}, 32)
	n := &newsletter.Service{
		Store: db, Keys: keys, RateKey: rateKey, Origin: "https://mychef.dev",
		ConsentVersion: "newsletter-v1", Now: func() time.Time { return now },
	}
	input := newsletter.Input{Email: "rejoin@example.test", Locale: "en", ConsentVersion: n.ConsentVersion}
	issue := func() mailqueue.Record {
		t.Helper()
		if wait, err := n.Request(ctx, "192.0.2.10", input); err != nil || wait != 0 {
			t.Fatalf("request: retry=%d err=%v", wait, err)
		}
		if worked, err := n.Step(ctx); err != nil || !worked {
			t.Fatalf("capture worker did not issue proof: worked=%v err=%v", worked, err)
		}
		var row mailqueue.Record
		if err := db.DB.Where("key LIKE 'newsletter/%'").Order("id DESC").Take(&row).Error; err != nil {
			t.Fatal(err)
		}
		return row
	}
	link := func(row mailqueue.Record, action string) string {
		t.Helper()
		fields, _, err := mailqueue.OpenProtected(keys, row, now, false)
		if err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(fields[action])
		if err != nil || u.Fragment == "" {
			t.Fatalf("missing %s proof: %v", action, err)
		}
		return u.Fragment
	}

	first := issue()
	if err := n.Unsubscribe(ctx, link(first, "unsubscribe")); err != nil {
		t.Fatal(err)
	}
	var user store.User
	if err := db.DB.First(&user).Error; err != nil {
		t.Fatal(err)
	}
	journal, err := db.InitializeLifecycleJournal(ctx, db.Path+".lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	identityKey, err := store.DeriveNewsletterIdentityKey(rateKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.InitializeNewsletterIdentity(ctx, identityKey); err != nil {
		t.Fatal(err)
	}
	tag, err := identityKey.Tag(input.Email)
	if err != nil {
		t.Fatal(err)
	}
	record := store.NewsletterIdentityRecord{
		UserID: user.ID, Class: "contact", KeyVersion: identityKey.Version(), AddressTag: tag,
		Status: "unsubscribed", NoticeVersion: store.Ptr(n.ConsentVersion),
	}
	var withdrawn int64
	if err := db.SQL.QueryRowContext(ctx, `
		SELECT a.incarnation, n.revision, n.proved_order, n.withdrawn_unix
		FROM account_incarnations a JOIN newsletter_subscriptions n ON n.user_id=a.user_id
		WHERE a.user_id=?`, user.ID).Scan(&record.Incarnation, &record.Revision, &record.DecisionOrder, &withdrawn); err != nil {
		t.Fatal(err)
	}
	record.DecisionUnix = store.Ptr(withdrawn)
	operation := strings.Repeat("a", 32)
	intent, err := journal.PrepareNewsletterIdentity(ctx, operation, record)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.DB.Transaction(func(tx *gorm.DB) error {
		return journal.MarkNewsletterIdentityCommit(ctx, tx, intent)
	}); err != nil {
		t.Fatal(err)
	}
	if err := journal.CommitNewsletterIdentity(ctx, operation); err != nil {
		t.Fatal(err)
	}
	if err := journal.CompleteNewsletterIdentityBootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	n.Lifecycle = journal
	now = now.Add(25 * time.Hour)
	if err := n.Minimise(ctx, journal); err != nil {
		t.Fatal(err)
	}
	// Same logical-fixture boundary as TestMinimisationFreshRequestKeepsReducedIDAndOptOut.
	if err := db.DB.Exec("UPDATE newsletter_identity_namespace SET physical_cleanup_pending=0,legacy_vacuum_pending=0").Error; err != nil {
		t.Fatal(err)
	}

	fresh := issue()
	confirmation := link(fresh, "confirm")
	withdrawal := link(fresh, "unsubscribe")
	assertStatus := func(want string, minimised bool) {
		t.Helper()
		var status string
		var reduced, keysAbsent bool
		if err := db.SQL.QueryRowContext(ctx, `
			SELECT n.status, u.newsletter_minimised, u.email_key IS NULL AND n.email_key IS NULL
			FROM users u JOIN newsletter_subscriptions n ON n.user_id=u.user_id
			WHERE u.user_id=? AND (SELECT count(*) FROM users)=1`, user.ID).Scan(&status, &reduced, &keysAbsent); err != nil {
			t.Fatal(err)
		}
		if status != want || reduced != minimised || keysAbsent != minimised {
			t.Fatalf("same-ID contact: status=%s minimised=%v keys-absent=%v", status, reduced, keysAbsent)
		}
	}
	assertStatus("unsubscribed", true)
	proofHash := strings.TrimPrefix(fresh.Key, "newsletter/")
	cases := []struct {
		name      string
		statement string
		args      []any
		recipient string
		template  string
		at        time.Time
		wantError error
	}{
		{
			name:      "different recipient despite matching clear proof key",
			statement: "UPDATE newsletter_proofs SET email_key=? WHERE hash=?",
			args:      []any{"other@example.test", proofHash},
			recipient: "other@example.test",
		},
		{
			name:      "wrong protected address tag",
			statement: "UPDATE newsletter_proofs SET identity_address_tag=? WHERE hash=?",
			args:      []any{strings.Repeat("b", 64), proofHash},
		},
		{
			name: "proof without protected incarnation",
			statement: "UPDATE newsletter_proofs SET identity_incarnation=NULL," +
				"identity_key_version=NULL,identity_address_tag=NULL WHERE hash=?",
			args: []any{proofHash},
		},
		{
			name:      "outbox belongs to a different incarnation",
			statement: "UPDATE account_mail_owners SET incarnation=? WHERE outbox_id=?",
			args:      []any{strings.Repeat("c", 32), fresh.ID},
		},
		{
			name:      "consumed proof",
			statement: "UPDATE newsletter_proofs SET consumed_unix=? WHERE hash=?",
			args:      []any{now.Unix(), proofHash},
		},
		{
			name: "expired at the exact boundary",
			at:   now.Add(24 * time.Hour),
		},
		{
			name:      "missing expiry",
			statement: "UPDATE newsletter_proofs SET expires_unix=NULL WHERE hash=?",
			args:      []any{proofHash},
		},
		{
			name:      "withdrawal advanced consent revision",
			statement: "UPDATE newsletter_subscriptions SET revision=revision+1 WHERE user_id=?",
			args:      []any{user.ID},
		},
		{
			name: "decision supersedes request order",
			statement: "UPDATE newsletter_subscriptions SET proved_order=" +
				"(SELECT request_order FROM newsletter_proofs WHERE hash=?) WHERE user_id=?",
			args: []any{proofHash, user.ID},
		},
		{
			name:     "withdrawal template cannot use a confirmation",
			template: mailqueue.NewsletterWithdrawalENTemplate,
		},
		{
			name:      "protected key does not match bound namespace",
			statement: "UPDATE newsletter_identity_namespace SET key_id=? WHERE singleton=1",
			args:      []any{strings.Repeat("d", 64)},
			wantError: store.ErrNewsletterIdentityUnavailable,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tx := db.DB.WithContext(ctx).Begin()
			if tx.Error != nil {
				t.Fatal(tx.Error)
			}
			t.Cleanup(func() {
				if err := tx.Rollback().Error; err != nil {
					t.Error(err)
				}
			})
			if tc.statement != "" {
				update := tx.Exec(tc.statement, tc.args...)
				if update.Error != nil || update.RowsAffected != 1 {
					t.Fatalf("fixture modification: count=%d err=%v", update.RowsAffected, update.Error)
				}
			}
			row, at := fresh, now
			if tc.recipient != "" {
				row.Recipient = tc.recipient
			}
			if tc.template != "" {
				row.Template = tc.template
			}
			if !tc.at.IsZero() {
				at = tc.at
			}
			allowed, err := mailqueue.NewsletterDispatchAllowed(ctx, db, tx, row, at)
			if allowed || !errors.Is(err, tc.wantError) {
				t.Fatalf("dispatch authority: allowed=%v err=%v, want refusal with err=%v", allowed, err, tc.wantError)
			}
		})
	}
	provider := &proofSender{result: deliveryResult{id: "fake-reenrolment"}}
	worker := Worker{
		Queue:  Queue{Store: db, ProofKeys: keys, Lifecycle: journal, Now: func() time.Time { return now }},
		sender: provider, From: "mychef@example.test", FingerprintKey: bytes.Repeat([]byte{3}, 32), newsletterOnly: true,
	}
	if worked, err := worker.Step(ctx); !worked || err != nil {
		t.Fatalf("mail worker: worked=%v err=%v", worked, err)
	}
	var sent outbox
	if err := db.DB.First(&sent, fresh.ID).Error; err != nil {
		t.Fatal(err)
	}
	if len(provider.bodies) != 1 || sent.State != "accepted" || sent.Attempts != 1 {
		t.Fatalf("fresh confirmation missed fake provider: deliveries=%d state=%s reason=%s attempts=%d",
			len(provider.bodies), sent.State, sent.Reason, sent.Attempts)
	}
	assertStatus("unsubscribed", true)
	if err := n.Confirm(ctx, confirmation); err != nil {
		t.Fatal(err)
	}
	assertStatus("confirmed", false)
	if err := n.Unsubscribe(ctx, withdrawal); err != nil {
		t.Fatal(err)
	}
	assertStatus("unsubscribed", false)
}
