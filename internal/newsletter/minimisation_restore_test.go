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

	"fmt"
	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/config"
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
	*now = now.Add(time.Hour)
	restoreCapture(t, n, "old-owner@example.test")
	freshA, found, err := n.Store.ResolveEmailOwner(t.Context(), "old-owner@example.test")
	if err != nil || !found {
		t.Fatal(err)
	}
	current, err := manager.Login(t.Context(), "current-owner@example.test", "restore account password fixture")
	if err != nil {
		t.Fatal(err)
	}
	session, err := manager.Rotate(t.Context(), &current.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.DeleteAccount(t.Context(), store.AccountActor{UserID: current.ID, SessionHash: auth.Hash(session), PasswordHash: current.Password}, strings.Repeat("d", 32), "DELETE", *now, store.AccountDeletionOptions{}); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Hour)
	restoreCapture(t, n, "current-owner@example.test")
	freshB, found, err := n.Store.ResolveEmailOwner(t.Context(), "current-owner@example.test")
	if err != nil || !found {
		t.Fatal(err)
	}
	for _, snapshot := range []string{placeholder, appSnapshot} {
		restored, _ := restoreMinimised(t, snapshot, j, key)
		for address, want := range map[string]int64{"old-owner@example.test": freshA.ID, "current-owner@example.test": freshB.ID} {
			owner, found, err := restored.ResolveEmailOwner(t.Context(), address)
			if err != nil || !found || owner.ID != want || owner.ID == account.ID || !owner.IsNewsletterOnly {
				t.Fatal("older copy revived erased ownership or lost new contact", err)
			}
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

func TestMinimisationRestoreKeepsDeletionAndInvalidatesOlderSignupQueue(t *testing.T) {
	n, j, key, now := minimisationRestoreFixture(t)
	*now = time.Now().UTC().Add(-time.Hour)
	restoreCapture(t, n, "erased-restore@example.test")
	owner, found, err := n.Store.ResolveEmailOwner(t.Context(), "erased-restore@example.test")
	if err != nil || !found {
		t.Fatal(err)
	}
	signup := &auth.SignUp{Store: n.Store, Keys: n.Keys, RateKey: n.RateKey, Origin: n.Origin, Now: n.Now, Lifecycle: j, Newsletter: n}
	if _, err := signup.Request(t.Context(), "192.0.2.83", auth.SignUpRequest{Email: owner.Email, SignUpDetails: auth.SignUpDetails{FirstName: "Queued", LastName: "Owner", Username: "queued-owner"}}); err != nil {
		t.Fatal(err)
	}
	snapshot := minimisationSnapshot(t, n.Store)
	var incarnation string
	if err := n.Store.SQL.QueryRowContext(t.Context(), "SELECT incarnation FROM account_incarnations WHERE user_id=?", owner.ID).Scan(&incarnation); err != nil {
		t.Fatal(err)
	}
	if _, err := j.EraseNewsletterContact(t.Context(), store.NewsletterContactAuthority{UserID: owner.ID, EmailKey: owner.EmailKey, Incarnation: incarnation, RequestReference: "verified-before-restore"}, n.RateKey, *now, store.AccountDeletionOptions{}); err != nil {
		t.Fatal(err)
	}
	restored, paired := restoreMinimised(t, snapshot, j, key)
	var total int
	if err := restored.SQL.QueryRowContext(t.Context(), "SELECT count(*) FROM users WHERE user_id=?", owner.ID).Scan(&total); err != nil || total != 0 {
		t.Fatal("erased identity restored", err)
	}
	signup.Store, signup.Lifecycle = restored, paired
	if worked, err := signup.Step(t.Context()); err != nil || worked {
		t.Fatal("old unverified signup request replayed after erasure/restore", err)
	}
	active := &newsletter.Service{Store: restored, Keys: n.Keys, RateKey: n.RateKey, Origin: n.Origin, ConsentVersion: n.ConsentVersion, Now: n.Now, Lifecycle: paired}
	*now = now.Add(time.Hour)
	restoreCapture(t, active, "erased-restore@example.test")
	replacement, found, err := restored.ResolveEmailOwner(t.Context(), "erased-restore@example.test")
	if err != nil || !found || replacement.ID <= owner.ID {
		t.Fatal("restored allocator reused erased ID", err)
	}
}

func TestMinimisationRestoreConfirmedChoiceNeedsFreshProofAndKeepsApp(t *testing.T) {
	for _, app := range []bool{false, true} {
		name := "contact"
		if app {
			name = "app"
		}
		t.Run(name, func(t *testing.T) {
			n, j, key, now := minimisationRestoreFixture(t)
			confirmation, _ := restoreCapture(t, n, "confirmed-restore@example.test")
			if err := n.Confirm(t.Context(), confirmation); err != nil {
				t.Fatal(err)
			}
			owner, found, err := n.Store.ResolveEmailOwner(t.Context(), "confirmed-restore@example.test")
			if err != nil || !found {
				t.Fatal(err)
			}
			if app {
				owner = promoteMinimisationContact(t, n, j, owner.Email)
			}
			snapshot := minimisationSnapshot(t, n.Store)
			restored, paired := restoreMinimised(t, snapshot, j, key)
			var status string
			if err := restored.SQL.QueryRowContext(t.Context(), "SELECT status FROM newsletter_subscriptions WHERE user_id=?", owner.ID).Scan(&status); err != nil || status != "pending" {
				t.Fatal("restored confirmation remained active", err)
			}
			active := &newsletter.Service{Store: restored, Keys: n.Keys, RateKey: n.RateKey, Origin: n.Origin, ConsentVersion: n.ConsentVersion, Now: n.Now, Lifecycle: paired}
			if err := active.Confirm(t.Context(), confirmation); err == nil {
				t.Fatal("snapshot's old proof revived consent")
			}
			if app {
				account, err := auth.New(restored).Login(t.Context(), owner.Email, "restore account password fixture")
				if err != nil || account.ID != owner.ID || account.IsNewsletterOnly {
					t.Fatal("restore changed app login/profile", err)
				}
			}
			*now = now.Add(time.Hour)
			fresh, _ := restoreCapture(t, active, "confirmed-restore@example.test")
			if err := active.Confirm(t.Context(), fresh); err != nil {
				t.Fatal("fresh request/confirmation did not work", err)
			}
		})
	}
}

func TestMinimisationDirectOlderCopyCannotServe(t *testing.T) {
	n, j, key, _ := minimisationRestoreFixture(t)
	_, withdrawal := restoreCapture(t, n, "direct-copy@example.test")
	snapshot := minimisationSnapshot(t, n.Store)
	if err := n.Unsubscribe(t.Context(), withdrawal); err != nil {
		t.Fatal(err)
	}
	copied, err := store.Open(snapshot, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = copied.Close() }()
	if err := copied.ConfigureNewsletterIdentity(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	paired, err := copied.OpenLifecycleJournal(t.Context(), j.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = paired.Close() }()
	if err := paired.ValidateNewsletterIdentities(t.Context()); err == nil {
		t.Fatal("direct old SQL copy was allowed to serve stale consent")
	}
}

func TestMinimisationRestorePreservesKnownOutstandingWithdrawal(t *testing.T) {
	for _, confirmed := range []bool{false, true} {
		name := "pending"
		if confirmed {
			name = "confirmed"
		}
		t.Run(name, func(t *testing.T) {
			n, j, key, _ := minimisationRestoreFixture(t)
			confirmation, withdrawal := restoreCapture(t, n, "surviving-link@example.test")
			if confirmed {
				if err := n.Confirm(t.Context(), confirmation); err != nil {
					t.Fatal(err)
				}
			}
			snapshot := minimisationSnapshot(t, n.Store)
			restored, paired := restoreMinimised(t, snapshot, j, key)
			active := &newsletter.Service{Store: restored, Keys: n.Keys, RateKey: n.RateKey, Origin: n.Origin, ConsentVersion: n.ConsentVersion, Now: n.Now, Lifecycle: paired}
			if err := active.Unsubscribe(t.Context(), withdrawal); err != nil {
				t.Fatal("known unchanged-mailbox withdrawal lost during restore", err)
			}
			var status string
			if err := restored.SQL.QueryRowContext(t.Context(), "SELECT status FROM newsletter_subscriptions").Scan(&status); err != nil || status != "unsubscribed" {
				t.Fatal("surviving withdrawal had no effect", err)
			}
		})
	}
}

func TestRestoreExpiresIssuedSignupBeforePublishing(t *testing.T) {
	n, j, key, now := minimisationRestoreFixture(t)
	address := "expired-restored-signup-canary@example.test"
	restoreCapture(t, n, address)
	signup := &auth.SignUp{Store: n.Store, Keys: n.Keys, RateKey: n.RateKey, Origin: n.Origin, Now: n.Now, Lifecycle: j, Newsletter: n}
	details := auth.SignUpDetails{FirstName: "Synthetic", LastName: "Review", Username: "independent-review"}
	if _, err := signup.Request(t.Context(), "192.0.2.210", auth.SignUpRequest{Email: address, SignUpDetails: details}); err != nil {
		t.Fatal(err)
	}
	if worked, err := signup.Step(t.Context()); err != nil || !worked {
		t.Fatal(err)
	}
	snapshot := minimisationSnapshot(t, n.Store)
	*now = now.Add(48 * time.Hour)
	none := config.NewsletterRetention{NoExpiry: true}
	if err := n.Maintain(t.Context(), j, none, none, store.AccountDeletionOptions{}); err != nil {
		t.Fatal(err)
	}
	var beforeProofs int64
	if err := n.Store.SQL.QueryRowContext(t.Context(), "SELECT count(*) FROM signup_proofs").Scan(&beforeProofs); err != nil || beforeProofs != 0 {
		t.Fatal("current namespace was not cleaned", err, beforeProofs)
	}
	if err := j.CleanupNewsletterFiles(t.Context()); err != nil {
		t.Fatal(err)
	}
	restored, _ := restoreMinimised(t, snapshot, j, key)
	var count int64
	if err := restored.SQL.QueryRowContext(t.Context(), "SELECT count(*) FROM email_outbox WHERE recipient=?", address).Scan(&count); err != nil {
		t.Fatal(err)
	}
	var proofs int64
	if err := restored.SQL.QueryRowContext(t.Context(), "SELECT count(*) FROM signup_proofs").Scan(&proofs); err != nil {
		t.Fatal(err)
	}
	var raw bool
	for _, suffix := range []string{"", "-wal", "-shm"} {
		data, err := os.ReadFile(restored.Path + suffix)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		raw = raw || bytes.Contains(data, []byte(address))
	}
	t.Logf("after explicit restore and certified cleanup: expired signup outbox=%d signup_proofs=%d plaintext_canary_present=%v", count, proofs, raw)
	if count != 0 || proofs != 0 || raw {
		t.Fatal("expired signup payload resurrected into active reduced namespace")
	}
}

func TestRestoreExpiresAccountWorkAndKeepsCurrentControls(t *testing.T) {
	n, j, key, _ := minimisationRestoreFixture(t)
	restoreCapture(t, n, "expired-contact-restore@example.test")
	var contact store.User
	if err := n.Store.DB.First(&contact).Error; err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Unix()
	full := store.User{Email: "live-account@example.test", Username: "restore-live-control", FirstName: "Live", LastName: "Control", Password: "keep-credential", EmailVerifiedAt: &stamp}
	if err := n.Store.DB.Create(&full).Error; err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	queries := []string{
		"INSERT INTO signup_proofs VALUES('expired-proof','old-hash','old-tag',2,NULL,'expired-signup-draft-canary')",
		"INSERT INTO email_verification_proofs VALUES('expired-verify'," + fmt.Sprint(contact.ID) + ",'expired-vhash','tag',2,NULL)",
		"INSERT INTO pending_email_changes(id,user_id,original_email,original_key,proposed_email,proposed_key,expires) VALUES('expired-change'," + fmt.Sprint(contact.ID) + ",'expired-change-restore-canary@example.test','expired-change-restore-canary@example.test','expired-proposed@example.test','expired-proposed@example.test',2)",
		"INSERT INTO recovery_jobs VALUES(1,'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',2,'expired-recovery-draft-canary')",
		"INSERT INTO recovery_proofs VALUES('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',1," + fmt.Sprint(contact.ID) + ",'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',0,1,2,NULL)",
		fmt.Sprintf("INSERT INTO signup_proofs VALUES('live-proof','live-hash','live-tag',%d,NULL,'live-signup-control')", future.Unix()),
		fmt.Sprintf("INSERT INTO email_verification_proofs VALUES('live-verify',%d,'live-vhash','live-tag',%d,NULL)", full.ID, future.Unix()),
		fmt.Sprintf("INSERT INTO pending_email_changes(id,user_id,original_email,original_key,proposed_email,proposed_key,expires) VALUES('live-change',%d,'live-account@example.test','live-account@example.test','live-next@example.test','live-next@example.test',%d)", full.ID, future.Unix()),
		fmt.Sprintf("INSERT INTO recovery_jobs VALUES(2,'cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc',%d,'live-recovery-control')", future.UnixNano()),
		fmt.Sprintf("INSERT INTO recovery_proofs VALUES('dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd',2,%d,'cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc',0,%d,%d,NULL)", full.ID, stamp*int64(time.Second), future.UnixNano()),
	}
	for _, q := range queries {
		if err := n.Store.DB.Exec(q).Error; err != nil {
			t.Fatal(err)
		}
	}
	expired := []struct{ key, template string }{
		{"signup/expired-proof", mailqueue.SignUpProofTemplate},
		{"verify-email/expired-verify", mailqueue.EmailVerificationTemplate},
		{"email-change/expired-change/current", mailqueue.EmailChangeCurrentTemplate},
		{"email-change/expired-change/proposed", mailqueue.EmailChangeProposedTemplate},
		{"password-recovery/1", mailqueue.RecoveryProofTemplate},
	}
	for i, item := range expired {
		row := mailqueue.Record{Key: item.key, Recipient: fmt.Sprintf("expired-mail-%d-restore-canary@example.test", i), Template: item.template, Data: `{"expires":2}`, State: "sent", Fingerprint: "expired-fingerprint-canary", CreatedAt: 1, UpdatedAt: 1}
		if err := n.Store.DB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct{ key, template string }{
		{"signup/live-proof", mailqueue.SignUpProofTemplate}, {"verify-email/live-verify", mailqueue.EmailVerificationTemplate},
		{"email-change/live-change/current", mailqueue.EmailChangeCurrentTemplate}, {"email-change/live-change/proposed", mailqueue.EmailChangeProposedTemplate},
		{"password-recovery/2", mailqueue.RecoveryProofTemplate},
	} {
		row := mailqueue.Record{Key: item.key, Recipient: full.Email, Template: item.template, Data: fmt.Sprintf(`{"expires":%d}`, future.Unix()), State: "queued", CreatedAt: stamp, UpdatedAt: stamp}
		if err := n.Store.DB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := mailqueue.Enqueue(t.Context(), n.Store, nil, time.Now(), mailqueue.Request{Key: "ordinary-restore-control", Recipient: full.Email, Template: "provider-smoke-test/v1", Fields: map[string]string{"testID": "keep", "timestamp": time.Now().Format(time.RFC3339)}}); err != nil {
		t.Fatal(err)
	}
	snapshot := minimisationSnapshot(t, n.Store)
	restored, paired := restoreMinimised(t, snapshot, j, key)
	if err := paired.NewsletterFilesReady(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"signup_proofs", "email_verification_proofs", "pending_email_changes", "recovery_jobs", "recovery_proofs"} {
		var count int
		if err := restored.SQL.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 1 {
			t.Errorf("%s has %d rows, expected only live control: %v", table, count, err)
		}
	}
	var mail int
	if err := restored.SQL.QueryRowContext(t.Context(), "SELECT count(*) FROM email_outbox").Scan(&mail); err != nil || mail != 6 {
		t.Errorf("expected five live purpose mails and ordinary control, got %d: %v", mail, err)
	}
	var actual store.User
	if err := restored.DB.First(&actual, "user_id=?", full.ID).Error; err != nil || actual.Email != full.Email || actual.Password != full.Password || actual.FirstName != full.FirstName || actual.EmailVerifiedAt == nil {
		t.Fatal("full account changed", err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		body, err := os.ReadFile(restored.Path + suffix)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		for _, canary := range []string{"expired-signup-draft-canary", "expired-change-restore-canary@example.test", "expired-recovery-draft-canary", "expired-fingerprint-canary", "-restore-canary@example.test"} {
			if bytes.Contains(body, []byte(canary)) {
				t.Errorf("expired canary %q remains in active%s", canary, suffix)
			}
		}
	}
}
