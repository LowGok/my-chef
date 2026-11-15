package store

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gorm.io/gorm"
)

func newsletterIdentityJournalFixture(t *testing.T) (*Store, *LifecycleJournal, *NewsletterIdentityKey, NewsletterIdentityRecord) {
	t.Helper()
	s := minimisationSchemaStore(t)
	u := User{Email: "alice@example.test", Username: "opaque", IsNewsletterOnly: true}
	if err := s.DB.WithContext(t.Context()).Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	minimisationSQL(t, s, `INSERT INTO newsletter_subscriptions(user_id,email_key,status,locale,consent_version,source,requested_unix)
VALUES(?,'alice@example.test','pending','en','notice-test','/blog/',1000)`, u.ID)
	j, err := s.InitializeLifecycleJournal(t.Context(), filepath.Join(t.TempDir(), "identity-journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	key := newsletterIdentityFixtureKey(t, 0)
	if err := j.InitializeNewsletterIdentity(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	var incarnation string
	if err := s.SQL.QueryRowContext(t.Context(), "SELECT incarnation FROM account_incarnations WHERE user_id=?", u.ID).Scan(&incarnation); err != nil {
		t.Fatal(err)
	}
	tag, err := key.Tag(u.Email)
	if err != nil {
		t.Fatal(err)
	}
	record := NewsletterIdentityRecord{UserID: u.ID, Incarnation: incarnation, Class: "contact", KeyVersion: key.Version(), AddressTag: tag,
		Status: "pending", Revision: 1, NoticeVersion: Ptr("notice-test"), DecisionUnix: Ptr(int64(1000))}
	return s, j, key, record
}

func commitNewsletterIdentityFixture(t *testing.T, j *LifecycleJournal, op string, record NewsletterIdentityRecord) NewsletterIdentityRecord {
	t.Helper()
	intent, err := j.PrepareNewsletterIdentity(t.Context(), op, record)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.app.DB.WithContext(t.Context()).Transaction(func(tx *gorm.DB) error {
		return j.MarkNewsletterIdentityCommit(t.Context(), tx, intent)
	}); err != nil {
		t.Fatal(err)
	}
	if err := j.CommitNewsletterIdentity(t.Context(), op); err != nil {
		t.Fatal(err)
	}
	current, found, err := j.NewsletterIdentity(t.Context(), record.UserID)
	if err != nil || !found {
		t.Fatal("committed identity absent", err)
	}
	return current
}

func TestNewsletterIdentityJournalUpgradeBindingAndBootstrap(t *testing.T) {
	s, j, key, record := newsletterIdentityJournalFixture(t)
	var version int
	if err := j.store.SQL.QueryRowContext(t.Context(), "SELECT version FROM lifecycle_journal_meta").Scan(&version); err != nil || version != 3 {
		t.Fatal("explicit journal upgrade missing", err)
	}
	if err := j.CheckReady(t.Context()); err == nil {
		t.Fatal("incomplete contact registry became ready")
	}
	if err := j.CompleteNewsletterIdentityBootstrap(t.Context()); err == nil {
		t.Fatal("current newsletter-only identity was omitted from bootstrap")
	}
	if err := j.InitializeNewsletterIdentity(t.Context(), newsletterIdentityFixtureKey(t, 1)); err == nil {
		t.Fatal("journal adopted another purpose key")
	}
	commitNewsletterIdentityFixture(t, j, strings.Repeat("a", 32), record)
	if err := j.CompleteNewsletterIdentityBootstrap(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := j.CheckReady(t.Context()); err != nil {
		t.Fatal("completed registry not ready", err)
	}
	if err := j.InitializeNewsletterIdentity(t.Context(), key); err != nil {
		t.Fatal("same-key migration retry changed the completed registry", err)
	}
	// Never infer a legacy full account's origin from subscription history or
	// adopt an unknown old placeholder after the authoritative registry is final.
	minimisationSQL(t, s, `INSERT INTO users(user_id,firstname,lastname,email,email_key,password,username,isNewsletterOnly)
VALUES(99,'','','unknown@example.test','unknown@example.test','','unknown-placeholder',1)`)
	if err := j.CompleteNewsletterIdentityBootstrap(t.Context()); err == nil {
		t.Fatal("completed registry silently adopted an unknown placeholder")
	}
}

func TestNewsletterIdentityJournalAllocatedIDsSurviveRestart(t *testing.T) {
	s, j, _, _ := newsletterIdentityJournalFixture(t)
	const floor int64 = 9007199254740993
	allocated, err := j.ReserveNewsletterUserID(t.Context(), floor)
	if err != nil || allocated != floor+1 {
		t.Fatal("high-water allocation did not preserve integer identity", err)
	}
	path := j.Path
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := s.OpenLifecycleJournal(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	next, err := reopened.ReserveNewsletterUserID(t.Context(), 0)
	if err != nil || next != allocated+1 {
		t.Fatal("an allocated ID was reused after SQL never committed", err)
	}
}

func TestNewsletterIdentityJournalPreparedIntentNeedsSQLFate(t *testing.T) {
	_, j, _, record := newsletterIdentityJournalFixture(t)
	op := strings.Repeat("a", 32)
	first, err := j.PrepareNewsletterIdentity(t.Context(), op, record)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := j.PrepareNewsletterIdentity(t.Context(), op, record)
	if err != nil || replay.Sequence != first.Sequence {
		t.Fatal("identical prepared retry allocated another transition", err)
	}
	changed := record
	changed.Status = "unsubscribed"
	if _, err := j.PrepareNewsletterIdentity(t.Context(), op, changed); err == nil {
		t.Fatal("changed prepared operation was accepted")
	}
	if _, err := j.PrepareNewsletterIdentity(t.Context(), strings.Repeat("b", 32), record); err == nil {
		t.Fatal("another operation overtook the unresolved transition")
	}
	if err := j.CommitNewsletterIdentity(t.Context(), op); err == nil {
		t.Fatal("journal committed without SQL proof")
	}
	if err := j.ReconcileNewsletterIdentityIntents(t.Context()); err == nil {
		t.Fatal("uncertain crash/old copy was guessed to be rolled back")
	}
	// Only the owner of a known rolled-back transaction explicitly cancels it.
	if err := j.CancelNewsletterIdentity(t.Context(), op); err != nil {
		t.Fatal(err)
	}
	if _, found, err := j.NewsletterIdentity(t.Context(), record.UserID); err != nil || found {
		t.Fatal("cancelled operation created registry state", err)
	}
}

func TestNewsletterIdentityJournalRecoversCommitAndCompactsHistory(t *testing.T) {
	s, j, _, record := newsletterIdentityJournalFixture(t)
	op := strings.Repeat("a", 32)
	intent, err := j.PrepareNewsletterIdentity(t.Context(), op, record)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DB.WithContext(t.Context()).Transaction(func(tx *gorm.DB) error {
		return j.MarkNewsletterIdentityCommit(t.Context(), tx, intent)
	}); err != nil {
		t.Fatal(err)
	}
	// Simulate process loss after SQL committed and before journal finalization.
	if err := j.ReconcileNewsletterIdentityIntents(t.Context()); err != nil {
		t.Fatal("committed transition not reconciled", err)
	}
	if err := j.CommitNewsletterIdentity(t.Context(), op); err != nil {
		t.Fatal("known completed acknowledgment failed", err)
	}
	current, found, err := j.NewsletterIdentity(t.Context(), record.UserID)
	if err != nil || !found || current.Sequence != intent.Sequence || current.AddressTag != record.AddressTag {
		t.Fatal("recovered state changed", err)
	}
	if err := j.CompleteNewsletterIdentityBootstrap(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := j.CompactNewsletterIdentityCheckpoints(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		store *Store
		query string
	}{
		{j.store, "SELECT count(*) FROM newsletter_identity_intents"},
		{s, "SELECT count(*) FROM newsletter_identity_commits"},
	} {
		var count int
		if err := item.store.SQL.QueryRowContext(t.Context(), item.query).Scan(&count); err != nil || count != 0 {
			t.Fatal("obsolete transition history retained", err)
		}
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		body, err := os.ReadFile(j.Path + suffix)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if bytes.Contains(body, []byte("alice@example.test")) || bytes.Contains(body, []byte("/blog/")) {
			t.Fatal("journal persisted clear address or source data")
		}
	}
}

func TestNewsletterIdentityJournalRequiresConsistentPostState(t *testing.T) {
	s, j, _, record := newsletterIdentityJournalFixture(t)
	current := commitNewsletterIdentityFixture(t, j, strings.Repeat("a", 32), record)
	withdrawn := current
	withdrawn.Status = "unsubscribed"
	withdrawn.DecisionUnix = Ptr(int64(2000))
	intent, err := j.PrepareNewsletterIdentity(t.Context(), strings.Repeat("b", 32), withdrawn)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DB.WithContext(t.Context()).Transaction(func(tx *gorm.DB) error {
		return j.MarkNewsletterIdentityCommit(t.Context(), tx, intent)
	}); err == nil {
		t.Fatal("commit marker certified a state SQL did not apply")
	}
	if err := j.CancelNewsletterIdentity(t.Context(), intent.Operation); err != nil {
		t.Fatal(err)
	}
	minimisationSQL(t, s, "INSERT INTO newsletter_identity_commits(operation,sequence,user_id,incarnation,record_hash) VALUES(?,999,?,?,?)", strings.Repeat("c", 32), record.UserID, record.Incarnation, strings.Repeat("d", 64))
	if err := j.ReconcileNewsletterIdentityIntents(t.Context()); err == nil {
		t.Fatal("unknown SQL operation was treated as authoritative")
	}
}

func TestNewsletterIdentityJournalPromotionEpochAndNoDowngrade(t *testing.T) {
	s, j, _, record := newsletterIdentityJournalFixture(t)
	current := commitNewsletterIdentityFixture(t, j, strings.Repeat("a", 32), record)
	promoted := current
	promoted.Class, promoted.KeyVersion, promoted.AddressTag = "app", 0, ""
	if _, err := j.PrepareNewsletterIdentity(t.Context(), strings.Repeat("b", 32), promoted); err == nil {
		t.Fatal("app classification accepted without an ownership epoch")
	}
	promoted.OwnershipEpoch = strings.Repeat("e", 32)
	intent, err := j.PrepareNewsletterIdentity(t.Context(), strings.Repeat("b", 32), promoted)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DB.WithContext(t.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("UPDATE users SET isNewsletterOnly=0,password='retained-hash',email_verified_at=1001,firstname='Full',lastname='Account',newsletter_ownership_epoch=? WHERE user_id=?", promoted.OwnershipEpoch, record.UserID).Error; err != nil {
			return err
		}
		return j.MarkNewsletterIdentityCommit(t.Context(), tx, intent)
	}); err != nil {
		t.Fatal(err)
	}
	if err := j.CommitNewsletterIdentity(t.Context(), intent.Operation); err != nil {
		t.Fatal(err)
	}
	app, found, err := j.NewsletterIdentity(t.Context(), record.UserID)
	if err != nil || !found || app.Class != "app" || app.AddressTag != "" || app.OwnershipEpoch != promoted.OwnershipEpoch {
		t.Fatal("promotion kept old address matching or lost its fence", err)
	}
	record.Sequence = app.Sequence
	if _, err := j.PrepareNewsletterIdentity(t.Context(), strings.Repeat("c", 32), record); err == nil {
		t.Fatal("known app account was downgraded to a contact")
	}
}

func TestNewsletterIdentityJournalCannotAdoptOlderUnboundJournal(t *testing.T) {
	_, j, key, record := newsletterIdentityJournalFixture(t)
	commitNewsletterIdentityFixture(t, j, strings.Repeat("a", 32), record)
	if err := j.CompleteNewsletterIdentityBootstrap(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Simulate an older same-binding journal before newsletter initialization.
	// The current SQL namespace must still remember that authority was bound.
	if err := j.store.DB.WithContext(t.Context()).Exec("DELETE FROM newsletter_identity_registry; DELETE FROM newsletter_identity_key").Error; err != nil {
		t.Fatal(err)
	}
	if err := j.InitializeNewsletterIdentity(t.Context(), key); err == nil {
		t.Fatal("an older unbound journal was adopted as fresh authority")
	}
}

func TestNewsletterIdentityJournalCannotReassignContactMatch(t *testing.T) {
	_, j, key, record := newsletterIdentityJournalFixture(t)
	current := commitNewsletterIdentityFixture(t, j, strings.Repeat("a", 32), record)
	different, err := key.Tag("another@example.test")
	if err != nil {
		t.Fatal(err)
	}
	current.AddressTag = different
	if _, err := j.PrepareNewsletterIdentity(t.Context(), strings.Repeat("b", 32), current); err == nil {
		t.Fatal("contact identity changed to another address without a verified app ownership transition")
	}
}

func TestNewsletterIdentityTransactionBatchRollsBackAndRetries(t *testing.T) {
	s, j, _, record := newsletterIdentityJournalFixture(t)
	commitNewsletterIdentityFixture(t, j, strings.Repeat("a", 32), record)
	if err := j.CompleteNewsletterIdentityBootstrap(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := j.CleanupNewsletterFiles(t.Context()); err != nil {
		t.Fatal(err)
	}
	second := User{Email: "second@example.test", Username: "second-opaque", IsNewsletterOnly: true}
	if err := s.NewsletterTransaction(t.Context(), j, func(tx *gorm.DB) error { return tx.Create(&second).Error }); err != nil {
		t.Fatal(err)
	}
	original, found, err := j.NewsletterIdentity(t.Context(), second.ID)
	if err != nil || !found {
		t.Fatal(err)
	}
	minimisationSQL(t, s, `CREATE TRIGGER refuse_second_identity BEFORE INSERT ON newsletter_identity_commits WHEN NEW.user_id=2 AND NEW.disposition='committed' BEGIN SELECT RAISE(ABORT,'injected marker write failure'); END`)
	update := func(tx *gorm.DB) error {
		TrackNewsletterIdentity(tx, record.UserID)
		TrackNewsletterIdentity(tx, second.ID)
		if err := tx.Exec("UPDATE newsletter_subscriptions SET status='unsubscribed',revision=revision+1,proved_order=5,withdrawn_unix=2000 WHERE user_id=?", record.UserID).Error; err != nil {
			return err
		}
		return tx.Exec("INSERT INTO newsletter_subscriptions(user_id,email_key,status,locale,consent_version,source,requested_unix) VALUES(?,'second@example.test','pending','en','notice-test','/blog/',1000)", second.ID).Error
	}
	if err := s.NewsletterTransaction(t.Context(), j, update); err == nil {
		t.Fatal("injected second marker failure was ignored")
	}
	current, found, err := j.NewsletterIdentity(t.Context(), record.UserID)
	if err != nil || !found || current.Status != "pending" {
		t.Fatal("first identity committed alone", err)
	}
	after, found, err := j.NewsletterIdentity(t.Context(), second.ID)
	if err != nil || !found || after.Sequence != original.Sequence || after.Revision != 0 {
		t.Fatal("rolled-back second identity changed", err)
	}
	var status string
	if err := s.SQL.QueryRowContext(t.Context(), "SELECT status FROM newsletter_subscriptions WHERE user_id=?", record.UserID).Scan(&status); err != nil || status != "pending" {
		t.Fatal("SQL partially committed", err)
	}
	if err := j.CheckReady(t.Context()); err != nil {
		t.Fatal("known rolled-back batch was not cancelled", err)
	}
	minimisationSQL(t, s, "DROP TRIGGER refuse_second_identity")
	if err := s.NewsletterTransaction(t.Context(), j, update); err != nil {
		t.Fatal("safe batch retry failed", err)
	}
	current, found, err = j.NewsletterIdentity(t.Context(), record.UserID)
	if err != nil || !found || current.Status != "unsubscribed" {
		t.Fatal("retry did not commit first decision", err)
	}
	after, found, err = j.NewsletterIdentity(t.Context(), second.ID)
	if err != nil || !found || after.Revision != 1 {
		t.Fatal("retry did not commit second decision", err)
	}
}

func TestNewsletterIdentityTransactionRejectsAnotherStore(t *testing.T) {
	_, j, _, record := newsletterIdentityJournalFixture(t)
	commitNewsletterIdentityFixture(t, j, strings.Repeat("a", 32), record)
	if err := j.CompleteNewsletterIdentityBootstrap(t.Context()); err != nil {
		t.Fatal(err)
	}
	other := minimisationSchemaStore(t)
	if err := other.NewsletterTransaction(t.Context(), j, func(tx *gorm.DB) error { return nil }); err == nil {
		t.Fatal("a different application store borrowed the journal's authority")
	}
}
