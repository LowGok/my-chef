package newsletter_test

import (
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/email/mailqueue"
	"github.com/LowGok/my-chef/internal/newsletter"
	"github.com/LowGok/my-chef/internal/store"
)

func minimisationRestoreFixture(t *testing.T) (*newsletter.Service, *store.LifecycleJournal, *store.NewsletterIdentityKey, *time.Time) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "app.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	j, err := s.InitializeLifecycleJournal(t.Context(), s.Path+".lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	keys, err := mailqueue.NewProofKeys("test", map[string][]byte{"test": bytes.Repeat([]byte{3}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	root := bytes.Repeat([]byte{8}, 32)
	key, err := store.DeriveNewsletterIdentityKey(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.InitializeNewsletterIdentity(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	if err := j.CompleteNewsletterIdentityBootstrap(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := j.CleanupNewsletterFiles(t.Context()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-10 * 24 * time.Hour)
	n := &newsletter.Service{Store: s, Keys: keys, RateKey: root, Origin: "https://mychef.dev", ConsentVersion: "newsletter-v1", Now: func() time.Time { return now }, Lifecycle: j}
	return n, j, key, &now
}

func minimisationSnapshot(t *testing.T, s *store.Store) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "snapshot.db")
	if err := s.Backup(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	return path
}
func restoreMinimised(t *testing.T, source string, j *store.LifecycleJournal, key *store.NewsletterIdentityKey) (*store.Store, *store.LifecycleJournal) {
	t.Helper()
	before, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "restored.db")
	if err := store.Restore(t.Context(), source, destination, store.AccountRestoreOptions{JournalPath: j.Path, NewsletterIdentityKey: key}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(source)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("retained source snapshot changed", err)
	}
	s, err := store.Open(destination, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.ConfigureNewsletterIdentity(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	paired, err := s.OpenLifecycleJournal(t.Context(), j.Path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = paired.Close() })
	if err := paired.NewsletterFilesReady(t.Context()); err != nil {
		t.Fatal("restored working copy was not sanitized before publication", err)
	}
	if err := s.Validate(t.Context()); err != nil {
		t.Fatal(err)
	}
	return s, paired
}

func TestMinimisationRestoreCurrentWithdrawalOverridesEveryOlderCopy(t *testing.T) {
	n, j, key, now := minimisationRestoreFixture(t)
	snapshots := []string{minimisationSnapshot(t, n.Store)}
	_, oldWithdrawal := restoreCapture(t, n, "obsolete-restore-canary@example.test")
	var id int64
	if err := n.Store.SQL.QueryRowContext(t.Context(), "SELECT user_id FROM users").Scan(&id); err != nil {
		t.Fatal(err)
	}
	snapshots = append(snapshots, minimisationSnapshot(t, n.Store))
	*now = now.Add(25 * time.Hour)
	if err := n.Minimise(t.Context(), j); err != nil {
		t.Fatal(err)
	}
	if err := j.CleanupNewsletterFiles(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshots = append(snapshots, minimisationSnapshot(t, n.Store))
	*now = now.Add(time.Hour)
	confirm, withdraw := restoreCapture(t, n, "obsolete-restore-canary@example.test")
	if err := n.Confirm(t.Context(), confirm); err != nil {
		t.Fatal(err)
	}
	snapshots = append(snapshots, minimisationSnapshot(t, n.Store))
	if err := n.Unsubscribe(t.Context(), withdraw); err != nil {
		t.Fatal(err)
	}
	if err := n.Minimise(t.Context(), j); err != nil {
		t.Fatal(err)
	}
	if err := j.CleanupNewsletterFiles(t.Context()); err != nil {
		t.Fatal(err)
	}
	for i, snapshot := range snapshots {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			restored, paired := restoreMinimised(t, snapshot, j, key)
			owner, found, err := restored.ResolveEmailOwner(t.Context(), "OBSOLETE-RESTORE-CANARY@example.test")
			if err != nil || !found || owner.ID != id || !owner.NewsletterMinimised || owner.Email != "" {
				t.Fatal("current reduced ID was not reconstructed", err)
			}
			var status string
			if err := restored.SQL.QueryRowContext(t.Context(), "SELECT status FROM newsletter_subscriptions WHERE user_id=?", id).Scan(&status); err != nil || status != "unsubscribed" {
				t.Fatal("old copy revived or forgot withdrawal", err)
			}
			for _, suffix := range []string{"", "-wal", "-shm"} {
				data, err := os.ReadFile(restored.Path + suffix)
				if err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				if bytes.Contains(data, []byte("obsolete-restore-canary@example.test")) {
					t.Fatal("old backup plaintext published active")
				}
			}
			active := &newsletter.Service{Store: restored, Keys: n.Keys, RateKey: n.RateKey, Origin: n.Origin, ConsentVersion: n.ConsentVersion, Now: n.Now, Lifecycle: paired}
			if err := active.Confirm(t.Context(), confirm); err == nil {
				t.Fatal("restored old proof regained authority")
			}
			if err := active.Unsubscribe(t.Context(), oldWithdrawal); err != nil && err != newsletter.ErrProof {
				t.Fatal(err)
			}
		})
	}
}

func TestMinimisationRestoreRequiresCorrectKeyAndRejectsUnknownPlaceholder(t *testing.T) {
	n, j, key, _ := minimisationRestoreFixture(t)
	restoreCapture(t, n, "known@example.test")
	snapshot := minimisationSnapshot(t, n.Store)
	wrong, err := store.DeriveNewsletterIdentityKey(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []*store.NewsletterIdentityKey{nil, wrong} {
		target := filepath.Join(t.TempDir(), "refused.db")
		if err := store.Restore(t.Context(), snapshot, target, store.AccountRestoreOptions{JournalPath: j.Path, NewsletterIdentityKey: candidate}); err == nil {
			t.Fatal("missing/replaced key accepted")
		}
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Fatal("refused restore was published")
		}
	}
	unknown, err := store.Open(snapshot, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := unknown.DB.Exec("INSERT INTO users(user_id,email,email_key,username,password,firstname,lastname,isNewsletterOnly) VALUES(99,'unknown@example.test','unknown@example.test','unknown','','','',1)").Error; err != nil {
		t.Fatal(err)
	}
	if err := unknown.Close(); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "unknown.db")
	if err := store.Restore(t.Context(), snapshot, target, store.AccountRestoreOptions{JournalPath: j.Path, NewsletterIdentityKey: key}); err == nil {
		t.Fatal("unknown old placeholder was adopted after registry completeness")
	}
}

func promoteMinimisationContact(t *testing.T, n *newsletter.Service, j *store.LifecycleJournal, address string) store.User {
	t.Helper()
	signup := &auth.SignUp{Store: n.Store, Keys: n.Keys, RateKey: n.RateKey, Origin: n.Origin, Now: n.Now, Lifecycle: j, Newsletter: n}
	manager := auth.New(n.Store)
	anonymous, err := manager.Rotate(t.Context(), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	details := auth.SignUpDetails{FirstName: "Restore", LastName: "Owner", Username: "restored-owner"}
	if _, err := signup.Request(t.Context(), "192.0.2.80", auth.SignUpRequest{Email: address, SignUpDetails: details}); err != nil {
		t.Fatal(err)
	}
	if worked, err := signup.Step(t.Context()); err != nil || !worked {
		t.Fatal(err)
	}
	token := minimisationMailToken(t, n, mailqueue.SignUpProofTemplate, "link")
	if err := signup.Complete(t.Context(), anonymous, auth.SignUpCompletion{Proof: token, Password: "restore account password fixture", SignUpDetails: details}); err != nil {
		t.Fatal(err)
	}
	account, err := manager.Login(t.Context(), address, "restore account password fixture")
	if err != nil {
		t.Fatal(err)
	}
	return account
}
func minimisationMailToken(t *testing.T, n *newsletter.Service, template, field string) string {
	t.Helper()
	var row mailqueue.Record
	if err := n.Store.DB.Where("template=?", template).Order("id DESC").Take(&row).Error; err != nil {
		t.Fatal(err)
	}
	fields, _, err := mailqueue.OpenProtected(n.Keys, row, n.Now(), false)
	if err != nil {
		t.Fatal(err)
	}
	link, err := url.Parse(fields[field])
	if err != nil || link.Fragment == "" {
		t.Fatal("missing synthetic proof")
	}
	return link.Fragment
}

func TestMinimisationRestoreRefusesPromotionAndOlderAppOwnership(t *testing.T) {
	n, j, key, now := minimisationRestoreFixture(t)
	restoreCapture(t, n, "old-owner@example.test")
	*now = now.Add(25 * time.Hour)
	if err := n.Minimise(t.Context(), j); err != nil {
		t.Fatal(err)
	}
	if err := j.CleanupNewsletterFiles(t.Context()); err != nil {
		t.Fatal(err)
	}
	placeholder := minimisationSnapshot(t, n.Store)
	account := promoteMinimisationContact(t, n, j, "old-owner@example.test")
	appSnapshot := minimisationSnapshot(t, n.Store)
	manager := auth.New(n.Store)
	cookie, err := manager.Rotate(t.Context(), &account.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	signup := &auth.SignUp{Store: n.Store, Keys: n.Keys, RateKey: n.RateKey, Origin: n.Origin, Now: n.Now, Lifecycle: j, Newsletter: n}
	change := &auth.EmailChange{SignUp: signup}
	if _, err := change.Start(t.Context(), account.ID, cookie, "192.0.2.81", "current-owner@example.test", "restore account password fixture"); err != nil {
		t.Fatal(err)
	}
	first := minimisationMailToken(t, n, mailqueue.EmailChangeCurrentTemplate, "approve")
	second := minimisationMailToken(t, n, mailqueue.EmailChangeProposedTemplate, "link")
	if _, err := change.Decide(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if _, err := change.Decide(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	for _, snapshot := range []string{placeholder, appSnapshot} {
		target := filepath.Join(t.TempDir(), "old-owner.db")
		if err := store.Restore(t.Context(), snapshot, target, store.AccountRestoreOptions{JournalPath: j.Path, NewsletterIdentityKey: key}); err == nil {
			t.Fatal("old contact/classification/mailbox epoch restored")
		}
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Fatal("stale app ownership published")
		}
	}
}

func TestMinimisationReducedExportAndErasureUseFreshVerifiedAddress(t *testing.T) {
	n, j, _, now := minimisationRestoreFixture(t)
	restoreCapture(t, n, "rights@example.test")
	*now = now.Add(25 * time.Hour)
	if err := n.Minimise(t.Context(), j); err != nil {
		t.Fatal(err)
	}
	if err := j.CleanupNewsletterFiles(t.Context()); err != nil {
		t.Fatal(err)
	}
	owner, found, err := n.Store.ResolveEmailOwner(t.Context(), "rights@example.test")
	if err != nil || !found {
		t.Fatal(err)
	}
	var incarnation string
	if err := n.Store.SQL.QueryRowContext(t.Context(), "SELECT incarnation FROM account_incarnations WHERE user_id=?", owner.ID).Scan(&incarnation); err != nil {
		t.Fatal(err)
	}
	actor := store.NewsletterContactAuthority{UserID: owner.ID, EmailKey: "rights@example.test", Incarnation: incarnation, RequestReference: "verified-mailbox-request"}
	body, err := n.Store.ExportNewsletterContact(t.Context(), actor)
	if err != nil {
		t.Fatal(err)
	}
	var exported map[string]any
	if err := json.Unmarshal(body, &exported); err != nil {
		t.Fatal(err)
	}
	if exported["email"] != nil || exported["newsletterMinimised"] != true {
		t.Fatal("export did not describe absent stored mailbox")
	}
	verification, ok := exported["verification"].(map[string]any)
	if !ok || verification["suppliedEmail"] != "rights@example.test" {
		t.Fatal("newly supplied verification address was not distinguished")
	}
	bad := actor
	bad.EmailKey = "other@example.test"
	if _, err := n.Store.ExportNewsletterContact(t.Context(), bad); err == nil {
		t.Fatal("wrong supplied address authorized reduced export")
	}
	if _, err := j.EraseNewsletterContact(t.Context(), actor, n.RateKey, *now, store.AccountDeletionOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := j.NewsletterIdentity(t.Context(), owner.ID); err != nil || found {
		t.Fatal("erased active match retained", err)
	}
	*now = now.Add(time.Hour)
	restoreCapture(t, n, "rights@example.test")
	replacement, found, err := n.Store.ResolveEmailOwner(t.Context(), "rights@example.test")
	if err != nil || !found || replacement.ID <= owner.ID {
		t.Fatal("fresh rights request reused erased ID", err)
	}
	if strings.Contains(string(body), "ciphertext") {
		t.Fatal("export included delivery secrets")
	}
}
