package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/email/mailqueue"
	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

func recoveryFixture(t *testing.T) (*Recovery, *time.Time, store.User) {
	t.Helper()
	ctx := context.Background()
	signUp, now := signUpFixture(t)
	u, err := New(signUp.Store).Register(ctx, Registration{
		FirstName: "Recovery", LastName: "Fixture", Username: "recovery", Email: "Verified+Case@Example.Test",
		Password: "a password for a recovery fixture",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := signUp.Store.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&u).Update("email_verified_at", now.Unix()).Error; err != nil {
			return err
		}
		_, err := store.AdvanceRecoveryFence(ctx, tx, u.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return &Recovery{SignUp: signUp}, now, u
}

func recoveryMail(t *testing.T, r *Recovery) []mailqueue.Record {
	t.Helper()
	var rows []mailqueue.Record
	if err := r.Store.DB.Where("template = ?", mailqueue.RecoveryProofTemplate).Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestRecoveryRequestQueuesUniformJobsAndOnlyVerifiedOwnerReceivesLink(t *testing.T) {
	ctx := context.Background()
	r, now, verified := recoveryFixture(t)
	restricted, err := New(r.Store).Register(ctx, Registration{
		FirstName: "Restricted", LastName: "Fixture", Username: "restricted", Email: "restricted@example.test",
		Password: "another password for a recovery fixture",
	})
	if err != nil {
		t.Fatal(err)
	}
	addresses := []string{strings.ToLower(verified.Email), restricted.Email, "missing@example.test"}
	for _, address := range addresses {
		if wait, err := r.Request(ctx, "192.0.2.30", address); err != nil || wait != 0 {
			t.Fatalf("intake rejected an eligible request: wait %d (%v)", wait, err)
		}
	}
	var jobs []store.RecoveryJob
	if err := r.Store.DB.Order("accepted_order").Find(&jobs).Error; err != nil || len(jobs) != len(addresses) {
		t.Fatal("all three account states must use the same intake")
	}
	for i, job := range jobs {
		if strings.Contains(job.Draft, addresses[i]) || strings.Contains(job.AddressTag, addresses[i]) ||
			job.ExpiresNS != now.Add(30*time.Minute).UnixNano() || job.Draft == "" {
			t.Fatal("pending job was not protected")
		}
	}
	if len(recoveryMail(t, r)) != 0 {
		t.Fatal("intake created an email before the worker ran")
	}

	// The durable request survives closing the original SQLite connection.
	if err := r.Store.Close(); err != nil {
		t.Fatal(err)
	}
	r.Store, err = store.Open(r.Store.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Store.Close() })
	for range jobs {
		if worked, err := r.Step(ctx); err != nil || !worked {
			t.Fatalf("recovery worker did not process a request: %v", err)
		}
	}
	rows := recoveryMail(t, r)
	if len(rows) != 1 || rows[0].Recipient != verified.Email {
		t.Fatal("only the verified account should receive mail, using the stored spelling")
	}
	fields, _, err := mailqueue.OpenProtected(r.Keys, rows[0], *now, false)
	if err != nil {
		t.Fatal(err)
	}
	link, err := url.Parse(fields["link"])
	if err != nil || link.Scheme != "https" || link.Path != "/app/reset-password" ||
		link.Fragment == "" || strings.Contains(rows[0].Data, link.Fragment) {
		t.Fatal("recovery link was not protected on the trusted route")
	}
	var proofs []store.RecoveryProof
	if err := r.Store.DB.Find(&proofs).Error; err != nil || len(proofs) != 1 {
		t.Fatal("only the verified account should receive a proof")
	}
	hash, err := signUpProofHash(link.Fragment)
	if err != nil || proofs[0].Hash != hash || proofs[0].UserID != verified.ID ||
		proofs[0].ExpiresNS-proofs[0].IssuedNS != int64(30*time.Minute) {
		t.Fatal("proof binding or lifetime did not match the delivered link")
	}
	journal, err := r.Store.InitializeLifecycleJournal(ctx, r.Store.Path+".lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	backup := filepath.Join(t.TempDir(), "recovery-snapshot.db")
	if err := r.Store.Backup(ctx, backup); err != nil {
		t.Fatal("could not snapshot pending encrypted mail")
	}
	for _, path := range []string{r.Store.Path, r.Store.Path + "-wal", backup} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal("could not scan disposable SQLite state")
		}
		if bytes.Contains(data, []byte(link.Fragment)) {
			t.Fatal("a raw recovery proof was found in SQLite or its backup")
		}
	}
	if worked, err := r.Step(ctx); err != nil || worked {
		t.Fatal("finished requests remained eligible")
	}
}

func TestRecoveryWorkerCannotIssueAnOlderJobAfterANewerLink(t *testing.T) {
	ctx := context.Background()
	r, now, user := recoveryFixture(t)
	for range 2 {
		if wait, err := r.Request(ctx, "192.0.2.31", user.Email); err != nil || wait != 0 {
			t.Fatal("could not stage two recovery requests")
		}
	}
	var jobs []store.RecoveryJob
	if err := r.Store.DB.Order("accepted_order").Find(&jobs).Error; err != nil || len(jobs) != 2 {
		t.Fatal("could not read queued orders")
	}
	for _, order := range []int64{jobs[1].AcceptedOrder, jobs[0].AcceptedOrder, jobs[1].AcceptedOrder} {
		if _, err := r.step(ctx, order); err != nil {
			t.Fatal(err)
		}
	}
	rows := recoveryMail(t, r)
	if len(rows) != 1 || rows[0].Key != mailqueue.RecoveryMailKey(jobs[1].AcceptedOrder) {
		t.Fatal("an older or replayed job replaced the newest issued link")
	}
	if wait, err := r.Request(ctx, "192.0.2.31", user.Email); err != nil || wait != 0 {
		t.Fatal("could not stage a later request")
	}
	if worked, err := r.Step(ctx); err != nil || !worked {
		t.Fatal("could not issue a replacement link")
	}
	rows = recoveryMail(t, r)
	if len(rows) != 2 || rows[0].State != "failed" || strings.Contains(rows[0].Data, "ciphertext") || rows[1].State != "queued" {
		t.Fatal("issuing the newest link did not erase the earlier queued link")
	}
	if wait, err := r.Request(ctx, "192.0.2.31", user.Email); err != nil || wait != 0 {
		t.Fatal("could not stage an expiring job")
	}
	*now = now.Add(recoveryLifetime)
	if worked, err := r.Step(ctx); err != nil || !worked || len(recoveryMail(t, r)) != 2 {
		t.Fatal("the worker issued a link for an expired job")
	}
}

const recoveredPassword = "a replacement recovery fixture password"
const originalRecoveryPassword = "a password for a recovery fixture"

func issueRecoveryFixture(t *testing.T, r *Recovery, now time.Time, address string) string {
	t.Helper()
	ctx := context.Background()
	if wait, err := r.Request(ctx, "192.0.2.40", address); wait != 0 || err != nil {
		t.Fatalf("could not request recovery: %d (%v)", wait, err)
	}
	if worked, err := r.Step(ctx); !worked || err != nil {
		t.Fatalf("could not issue recovery proof: %v", err)
	}
	rows := recoveryMail(t, r)
	if len(rows) == 0 {
		t.Fatal("missing recovery message")
	}
	fields, _, err := mailqueue.OpenProtected(r.Keys, rows[len(rows)-1], now, false)
	if err != nil {
		t.Fatal(err)
	}
	link, err := url.Parse(fields["link"])
	if err != nil || link.Fragment == "" {
		t.Fatal("missing recovery proof")
	}
	return link.Fragment
}

func recoverySession(t *testing.T, m *Manager, userID *int64) string {
	t.Helper()
	token, err := m.Rotate(context.Background(), userID, "")
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestRecoveryCompletionHasOneWinnerAndRevokesAllAccountSessions(t *testing.T) {
	ctx := context.Background()
	r, now, user := recoveryFixture(t)
	m := New(r.Store)
	proof := issueRecoveryFixture(t, r, *now, user.Email)
	heldLogin, err := m.verifyLogin(ctx, user.Email, originalRecoveryPassword)
	if err != nil {
		t.Fatal(err)
	}
	oldA := recoverySession(t, m, &user.ID)
	oldB := recoverySession(t, m, &user.ID)
	anonymous := recoverySession(t, m, nil)
	otherAnonymous := recoverySession(t, m, nil)
	if err := r.Complete(ctx, anonymous, proof, "short"); !errors.Is(err, ErrPasswordLength) {
		t.Fatalf("expected policy failure without consuming proof: %v", err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, cookie := range []string{anonymous, otherAnonymous} {
		wg.Go(func() { results <- r.Complete(ctx, cookie, proof, recoveredPassword) })
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrRecoveryProof) {
			t.Fatalf("unexpected competing reset outcome: %v", err)
		}
	}
	if success != 1 {
		t.Fatalf("expected one successful reset, got %d", success)
	}
	for _, cookie := range []string{oldA, oldB} {
		if _, err := m.Load(ctx, cookie); !errors.Is(err, ErrSession) {
			t.Fatal("an old account session survived")
		}
	}
	for _, cookie := range []string{anonymous, otherAnonymous} {
		if session, err := m.Load(ctx, cookie); err != nil || session.UserID != nil {
			t.Fatal("reset granted authentication or replaced the anonymous session")
		}
	}
	if _, err := m.Login(ctx, user.Email, originalRecoveryPassword); !errors.Is(err, ErrCredentials) {
		t.Fatal("old password still worked")
	}
	if _, _, err := m.completeLogin(ctx, heldLogin, anonymous, true); !errors.Is(err, ErrCredentials) {
		t.Fatal("a login checked before the reset created a session")
	}
	if _, err := m.Login(ctx, user.Email, recoveredPassword); err != nil {
		t.Fatal("new password did not work through ordinary sign-in")
	}
	var notices int64
	if err := r.Store.DB.Model(&mailqueue.Record{}).Where("template = ?", mailqueue.PasswordChangedTemplate).Count(&notices).Error; err != nil || notices != 1 {
		t.Fatal("reset did not enqueue exactly one security notice")
	}
}

func TestRecoveryCompletionRechecksAnonymousSessionAndExpiryAtWrite(t *testing.T) {
	ctx := context.Background()
	for _, outcome := range []string{"sign-in", "revocation", "exact expiry"} {
		t.Run(outcome, func(t *testing.T) {
			r, now, user := recoveryFixture(t)
			m := New(r.Store)
			proof := issueRecoveryFixture(t, r, *now, user.Email)
			anonymous := recoverySession(t, m, nil)
			hash, err := r.prepareRecovery(ctx, anonymous, proof, recoveredPassword)
			if err != nil {
				t.Fatal(err)
			}
			switch outcome {
			case "sign-in":
				other, err := m.Register(ctx, Registration{FirstName: "Other", LastName: "Account", Username: "other",
					Email: "other@example.test", Password: originalRecoveryPassword})
				if err != nil {
					t.Fatal(err)
				}
				if _, cookie, err := m.LoginSession(ctx, other.Email, originalRecoveryPassword, anonymous); err != nil || cookie == "" {
					t.Fatal("other account did not replace the initiating session")
				}
			case "revocation":
				if err := m.Revoke(ctx, anonymous); err != nil {
					t.Fatal(err)
				}
			case "exact expiry":
				*now = now.Add(recoveryLifetime)
			}
			want := ErrSession
			if outcome == "exact expiry" {
				want = ErrRecoveryProof
			}
			if err := r.commitRecovery(ctx, anonymous, proof, hash); !errors.Is(err, want) {
				t.Fatalf("late completion was not rejected: %v", err)
			}
			var saved store.RecoveryProof
			if err := r.Store.DB.Take(&saved).Error; err != nil || saved.ConsumedNS != nil {
				t.Fatal("a rejected completion consumed the proof")
			}
			if _, err := m.Login(ctx, user.Email, originalRecoveryPassword); err != nil {
				t.Fatal("a rejected completion changed the password")
			}
			if rows := recoveryMail(t, r); len(rows) != 1 || rows[0].State != "queued" {
				t.Fatal("a rejected completion changed the delivery row")
			}
		})
	}
}

func TestRecoveryRollsBackAtEachCredentialWrite(t *testing.T) {
	ctx := context.Background()
	faults := []string{
		"BEFORE UPDATE OF password ON users",
		"BEFORE UPDATE OF eligibility_order ON recovery_account_orders",
		"BEFORE UPDATE OF consumed_ns ON recovery_proofs",
		"BEFORE DELETE ON recovery_jobs",
		"BEFORE DELETE ON pending_email_changes",
		"BEFORE DELETE ON sessions",
		"BEFORE INSERT ON email_outbox WHEN NEW.template = 'password-changed/v1'",
		"BEFORE DELETE ON email_rate_events",
	}
	for _, fault := range faults {
		t.Run(fault, func(t *testing.T) {
			r, now, user := recoveryFixture(t)
			m := New(r.Store)
			authenticated := recoverySession(t, m, &user.ID)
			anonymous := recoverySession(t, m, nil)
			proof := issueRecoveryFixture(t, r, *now, user.Email)
			if wait, err := r.Request(ctx, "192.0.2.40", user.Email); err != nil || wait != 0 {
				t.Fatal("could not queue a second recovery request")
			}
			change := &EmailChange{SignUp: r.SignUp}
			if wait, err := change.Start(ctx, user.ID, authenticated, "192.0.2.41", "pending@example.test", originalRecoveryPassword); err != nil || wait != 0 {
				t.Fatal("could not stage a pending address change")
			}
			bucket, err := r.RecoveryCompletionBucket(proof)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.Store.ReserveEmailLimits(ctx, nil, *now, store.EmailLimit{Bucket: bucket, Maximum: 10, Window: 15 * time.Minute}); err != nil {
				t.Fatal(err)
			}
			var before store.RecoveryAccountOrder
			if err := r.Store.DB.Where("user_id = ?", user.ID).Take(&before).Error; err != nil {
				t.Fatal(err)
			}
			if err := r.Store.DB.Exec("CREATE TRIGGER fail_recovery " + fault + " BEGIN SELECT RAISE(ABORT, 'fixture fault'); END").Error; err != nil {
				t.Fatal(err)
			}
			if err := r.Complete(ctx, anonymous, proof, recoveredPassword); !errors.Is(err, store.ErrRecoveryUnavailable) {
				t.Fatalf("injected write did not abort the reset: %v", err)
			}
			var after store.RecoveryAccountOrder
			if err := r.Store.DB.Where("user_id = ?", user.ID).Take(&after).Error; err != nil || after != before {
				t.Fatal("rollback advanced the account order")
			}
			if _, err := m.Load(ctx, authenticated); err != nil {
				t.Fatal("rollback revoked an existing session")
			}
			if _, err := m.Login(ctx, user.Email, originalRecoveryPassword); err != nil {
				t.Fatal("rollback changed the original credential")
			}
			var saved store.RecoveryProof
			if err := r.Store.DB.Take(&saved).Error; err != nil || saved.ConsumedNS != nil {
				t.Fatal("rollback consumed the proof")
			}
			if rows := recoveryMail(t, r); len(rows) != 1 || rows[0].State != "queued" || !strings.Contains(rows[0].Data, "ciphertext") {
				t.Fatal("rollback erased the pending encrypted email")
			}
			for _, check := range []struct {
				query string
				args  []any
				want  int
			}{
				{"SELECT count(*) FROM pending_email_changes", nil, 1},
				{"SELECT count(*) FROM recovery_jobs", nil, 1},
				{"SELECT count(*) FROM email_outbox WHERE template = ?", []any{mailqueue.PasswordChangedTemplate}, 0},
				{"SELECT count(*) FROM email_rate_events WHERE bucket = ?", []any{bucket}, 1},
			} {
				var count int
				if err := r.Store.SQL.QueryRowContext(ctx, check.query, check.args...).Scan(&count); err != nil || count != check.want {
					t.Fatal("rollback left partial recovery state")
				}
			}
		})
	}
}

func TestKnownPasswordChangeInvalidatesQueuedAndIssuedRecovery(t *testing.T) {
	ctx := context.Background()
	r, now, user := recoveryFixture(t)
	m := &Manager{Store: r.Store, RecoveryRateKey: r.RateKey}
	anonymous := recoverySession(t, m, nil)
	signed := recoverySession(t, m, &user.ID)
	proof := issueRecoveryFixture(t, r, *now, user.Email)
	if _, err := r.Request(ctx, "192.0.2.40", user.Email); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.ChangePassword(ctx, user.ID, signed, originalRecoveryPassword, recoveredPassword); err != nil {
		t.Fatal(err)
	}
	if err := r.Complete(ctx, anonymous, proof, "another recovery password"); !errors.Is(err, ErrRecoveryProof) {
		t.Fatal("known-password change did not invalidate the issued recovery proof")
	}
	if worked, err := r.Step(ctx); err != nil || worked {
		t.Fatal("known-password change retained an earlier recovery job")
	}
	if rows := recoveryMail(t, r); len(rows) != 1 || rows[0].State != "failed" || strings.Contains(rows[0].Data, "ciphertext") {
		t.Fatal("known-password change retained the protected link")
	}
}

func TestEmailReplacementAndReuseCannotIssueEarlierRecoveryJobs(t *testing.T) {
	ctx := context.Background()
	r, now, user := recoveryFixture(t)
	m := New(r.Store)
	c := &EmailChange{SignUp: r.SignUp}
	anonymous := recoverySession(t, m, nil)
	signed := recoverySession(t, m, &user.ID)
	proof := issueRecoveryFixture(t, r, *now, user.Email)
	heldCredential, err := r.prepareRecovery(ctx, anonymous, proof, recoveredPassword)
	if err != nil {
		t.Fatal(err)
	}
	proposed := "New+Address@Example.Test"
	for _, address := range []string{user.Email, proposed} {
		if wait, err := r.Request(ctx, "192.0.2.40", address); err != nil || wait != 0 {
			t.Fatal("could not stage a job before address replacement")
		}
	}
	if wait, err := c.Start(ctx, user.ID, signed, "192.0.2.41", proposed, originalRecoveryPassword); err != nil || wait != 0 {
		t.Fatal("could not stage an address change")
	}
	current := deliveredChangeProof(t, c, *now, mailqueue.EmailChangeCurrentTemplate, "approve")
	next := deliveredChangeProof(t, c, *now, mailqueue.EmailChangeProposedTemplate, "link")
	if _, err := c.Decide(ctx, current); err != nil {
		t.Fatal(err)
	}
	if result, err := c.Decide(ctx, next); err != nil || result.Status != "changed" {
		t.Fatal("address did not change")
	}
	if err := r.commitRecovery(ctx, anonymous, proof, heldCredential); !errors.Is(err, ErrRecoveryProof) {
		t.Fatal("address replacement did not stop the held reset")
	}
	if worked, err := r.Step(ctx); err != nil || worked {
		t.Fatal("replacement retained a job for the old or new address")
	}
	if wait, err := r.Request(ctx, "192.0.2.40", user.Email); err != nil || wait != 0 {
		t.Fatal("could not request recovery while the original address was unclaimed")
	}
	details := SignUpDetails{FirstName: "Reused", LastName: "Address", Username: "reused"}
	if wait, err := r.SignUp.Request(ctx, "192.0.2.42", SignUpRequest{SignUpDetails: details, Email: user.Email}); err != nil || wait != 0 {
		t.Fatal("could not request sign-up for the reused address")
	}
	if worked, err := r.SignUp.Step(ctx); err != nil || !worked {
		t.Fatal("could not issue a sign-up proof")
	}
	if err := r.SignUp.Complete(ctx, anonymous, SignUpCompletion{SignUpDetails: details,
		Proof: signUpLink(t, r.SignUp, *now), Password: originalRecoveryPassword}); err != nil {
		t.Fatal(err)
	}
	if worked, err := r.Step(ctx); err != nil || worked || len(recoveryMail(t, r)) != 1 {
		t.Fatal("a job accepted before address reuse reached the new account")
	}
	if token := issueRecoveryFixture(t, r, *now, user.Email); token == proof {
		t.Fatal("the new account received the old proof")
	}
}

func TestResetWinsBeforePendingAddressFinalization(t *testing.T) {
	ctx := context.Background()
	r, now, user := recoveryFixture(t)
	m := New(r.Store)
	change := &EmailChange{SignUp: r.SignUp}
	signed := recoverySession(t, m, &user.ID)
	anonymous := recoverySession(t, m, nil)
	proof := issueRecoveryFixture(t, r, *now, user.Email)
	proposed := "proposed@example.test"
	if wait, err := change.Start(ctx, user.ID, signed, "192.0.2.50", proposed, originalRecoveryPassword); wait != 0 || err != nil {
		t.Fatal("could not stage address change")
	}
	current := deliveredChangeProof(t, change, *now, mailqueue.EmailChangeCurrentTemplate, "approve")
	newAddress := deliveredChangeProof(t, change, *now, mailqueue.EmailChangeProposedTemplate, "link")
	if _, err := change.Decide(ctx, current); err != nil {
		t.Fatal(err)
	}
	if err := r.Complete(ctx, anonymous, proof, recoveredPassword); err != nil {
		t.Fatal(err)
	}
	if _, err := change.Decide(ctx, newAddress); !errors.Is(err, ErrEmailProof) {
		t.Fatal("the pending address changed after a successful reset")
	}
	if _, err := m.Login(ctx, user.Email, recoveredPassword); err != nil {
		t.Fatal("the reset did not preserve the original verified address")
	}
}

func TestInitialVerificationFencesARequestAcceptedInTheSameSecond(t *testing.T) {
	ctx := context.Background()
	r, now, _ := recoveryFixture(t)
	m := New(r.Store)
	legacy, err := m.Register(ctx, Registration{FirstName: "Legacy", LastName: "Fixture", Username: "legacy-recovery",
		Email: "legacy-recovery@example.test", Password: originalRecoveryPassword})
	if err != nil {
		t.Fatal(err)
	}
	if wait, err := r.Request(ctx, "192.0.2.52", legacy.Email); err != nil || wait != 0 {
		t.Fatal("could not stage earlier recovery request")
	}
	restricted := recoverySession(t, m, &legacy.ID)
	v := &EmailVerification{SignUp: r.SignUp}
	if wait, err := v.Resend(ctx, legacy.ID, restricted, "192.0.2.52"); err != nil || wait != 0 {
		t.Fatal("could not issue verification mail")
	}
	var mail mailqueue.Record
	if err := r.Store.DB.Where("template = ?", mailqueue.EmailVerificationTemplate).Take(&mail).Error; err != nil {
		t.Fatal(err)
	}
	fields, _, err := mailqueue.OpenProtected(r.Keys, mail, *now, false)
	if err != nil {
		t.Fatal(err)
	}
	link, err := url.Parse(fields["link"])
	if err != nil || link.Fragment == "" {
		t.Fatal("verification proof was absent")
	}
	if _, _, err := v.Confirm(ctx, legacy.ID, restricted, link.Fragment); err != nil {
		t.Fatal(err)
	}
	if worked, err := r.Step(ctx); err != nil || worked {
		t.Fatal("a request accepted before verification remained eligible")
	}
	_ = issueRecoveryFixture(t, r, *now, legacy.Email)
}

func TestRecoveryRequestLimitsCanonicalAddressAndSourceAcrossRestart(t *testing.T) {
	ctx := t.Context()
	r, now, owner := recoveryFixture(t)
	start := *now
	check := func(source, address string, want int) {
		t.Helper()
		wait, err := r.Request(ctx, source, address)
		if err != nil || wait != want {
			t.Fatalf("request limit: got %d, want %d (%v)", wait, want, err)
		}
	}
	for i := range 20 {
		check("192.0.2.40", fmt.Sprintf("other-%d@example.test", i), 0)
	}
	*now = start.Add(time.Minute)
	for i := range 5 {
		check(fmt.Sprintf("2001:db8::%x", i+1), " "+strings.ToLower(owner.Email)+" ", 0)
	}
	if err := r.Store.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := store.Open(r.Store.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	r.Store = next
	t.Cleanup(func() { _ = next.Close() })
	*now = start.Add(2 * time.Minute)
	check("192.0.2.40", owner.Email, 29*60)
	check("192.0.2.40", "another@example.test", 28*60)
	check("192.0.2.41", owner.Email, 29*60)
	*now = start.Add(30 * time.Minute)
	check("192.0.2.40", "another@example.test", 0)
	check("192.0.2.41", owner.Email, 60)
	*now = start.Add(31 * time.Minute)
	check("192.0.2.41", owner.Email, 0)
}

func TestRecoveryStartupSweepErasesExpiredLiveSecretsAfterDowntime(t *testing.T) {
	ctx := t.Context()
	r, now, owner := recoveryFixture(t)
	proof := issueRecoveryFixture(t, r, *now, owner.Email)
	if wait, err := r.Request(ctx, "192.0.2.45", owner.Email); err != nil || wait != 0 {
		t.Fatal("could not stage an unprocessed request")
	}
	if err := r.Store.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := store.Open(r.Store.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	r.Store = next
	t.Cleanup(func() { _ = next.Close() })
	*now = now.Add(recoveryLifetime)
	if err := r.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	for _, table := range []any{&store.RecoveryProof{}, &store.RecoveryJob{}} {
		var count int64
		if err := next.DB.Model(table).Count(&count).Error; err != nil || count != 0 {
			t.Fatal("startup cleanup left expired proof or job state")
		}
	}
	rows := recoveryMail(t, r)
	if len(rows) != 1 || rows[0].State != "failed" || strings.Contains(rows[0].Data, "ciphertext") || strings.Contains(rows[0].Data, proof) {
		t.Fatal("startup cleanup left expired mail secrets")
	}
}

func TestRecoveryAccountDeletionErasesQueuedProofAtomically(t *testing.T) {
	for _, state := range []string{"queued", "sending"} {
		t.Run(state, func(t *testing.T) {
			r, now, owner := recoveryFixture(t)
			_ = issueRecoveryFixture(t, r, *now, owner.Email)
			rows := recoveryMail(t, r)
			if len(rows) != 1 || rows[0].State != "queued" || !strings.Contains(rows[0].Data, "ciphertext") {
				t.Fatal("expected protected mail before account deletion")
			}
			if err := r.Store.DB.Model(&rows[0]).Update("state", state).Error; err != nil {
				t.Fatal("could not set up the outbox state")
			}
			if _, err := r.Store.SQL.Exec(`CREATE TRIGGER fail_recovery_erasure
BEFORE UPDATE ON email_outbox WHEN NEW.state='failed'
BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
				t.Fatal("could not inject cleanup failure")
			}
			if err := r.Store.DB.Delete(&owner).Error; err == nil {
				t.Fatal("deletion committed despite failed proof mail erasure")
			}
			var count int64
			if err := r.Store.DB.Model(&store.RecoveryProof{}).Count(&count).Error; err != nil || count != 1 {
				t.Fatal("rollback did not retain the proof")
			}
			if rows := recoveryMail(t, r); len(rows) != 1 || rows[0].State != state || !strings.Contains(rows[0].Data, "ciphertext") {
				t.Fatal("rollback changed the protected outbox")
			}
			if _, err := r.Store.SQL.Exec("DROP TRIGGER fail_recovery_erasure"); err != nil {
				t.Fatal("could not clear cleanup fault")
			}
			if err := r.Store.DB.Delete(&owner).Error; err != nil {
				t.Fatal("could not delete the fixture account")
			}
			if err := r.Store.DB.Model(&store.RecoveryProof{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatal("deleting the account did not remove its proof")
			}
			*now = now.Add(recoveryLifetime + time.Minute)
			if err := r.Sweep(t.Context()); err != nil {
				t.Fatal("independent expiry sweep failed after account deletion")
			}
			rows = recoveryMail(t, r)
			if len(rows) != 1 || rows[0].State != "failed" || rows[0].Reason != "proof_invalidated" || strings.Contains(rows[0].Data, "ciphertext") {
				t.Fatal("deleting the account left the link recoverable")
			}
		})
	}
}
