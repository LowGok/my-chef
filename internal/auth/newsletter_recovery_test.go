package auth

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/email/mailqueue"
	"github.com/LowGok/my-chef/internal/newsletter"
	"github.com/LowGok/my-chef/internal/store"
)

func destinationFixture(t *testing.T) (*EmailChange, *newsletter.Service, *store.LifecycleJournal, store.User, store.User, string) {
	t.Helper()
	change, now, account, cookie := changeFixture(t, true)
	n, contact, _, _ := contactFixture(t, change.SignUp, "destination@example.test", "confirmed")
	j, err := change.Store.InitializeLifecycleJournal(t.Context(), change.Store.Path+".lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	change.Lifecycle, change.Newsletter, n.Lifecycle = j, n, j
	if _, err := change.Start(t.Context(), account.ID, cookie, "192.0.2.55", contact.Email, changeFixturePassword); err != nil {
		t.Fatal(err)
	}
	first := deliveredChangeProof(t, change, *now, mailqueue.EmailChangeCurrentTemplate, "approve")
	second := deliveredChangeProof(t, change, *now, mailqueue.EmailChangeProposedTemplate, "link")
	if _, err := change.Decide(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	return change, n, j, account, contact, second
}

func TestNewsletterTransferFailureRollsBackAndCanRetry(t *testing.T) {
	for _, stage := range []string{"prepared", "user", "outbox"} {
		t.Run(stage, func(t *testing.T) {
			change, _, j, account, contact, proof := destinationFixture(t)
			before := readContactConsent(t, change.Store, contact.ID)
			if stage == "outbox" {
				if err := change.Store.DB.Exec("CREATE TRIGGER fail_transfer_mail BEFORE INSERT ON email_outbox WHEN NEW.template LIKE 'newsletter-withdrawal-%' BEGIN SELECT RAISE(ABORT,'injected'); END").Error; err != nil {
					t.Fatal(err)
				}
			} else {
				change.retirementFault = func(at string) error {
					if at == stage {
						return errors.New("injected transfer failure")
					}
					return nil
				}
			}
			if _, err := change.Decide(t.Context(), proof); !errors.Is(err, ErrEmailChangeUnavailable) {
				t.Fatalf("fault accepted: %v", err)
			}
			var kept store.User
			if err := change.Store.DB.First(&kept, account.ID).Error; err != nil || kept.EmailKey != account.EmailKey {
				t.Fatal("app address partially changed", err)
			}
			if after := readContactConsent(t, change.Store, contact.ID); after.Status != before.Status || after.Revision != before.Revision {
				t.Fatal("contact consent partially changed")
			}
			if err := j.CheckReady(t.Context()); err != nil {
				t.Fatal("rolled-back intent was not cancelled", err)
			}
			change.retirementFault = nil
			if stage == "outbox" {
				if err := change.Store.DB.Exec("DROP TRIGGER fail_transfer_mail").Error; err != nil {
					t.Fatal(err)
				}
			}
			if result, err := change.Decide(t.Context(), proof); err != nil || result.UserID != account.ID || result.Status != "changed" {
				t.Fatalf("retry failed: %+v %v", result, err)
			}
		})
	}
}

func TestNewsletterTransferProcessCrashBoundaries(t *testing.T) {
	if stage := os.Getenv("MYCHEF_NEWSLETTER_TRANSFER_CRASH"); stage != "" {
		s, err := store.Open(os.Getenv("MYCHEF_NEWSLETTER_CRASH_DB"), false)
		if err != nil {
			t.Fatal(err)
		}
		j, err := s.OpenLifecycleJournal(t.Context(), os.Getenv("MYCHEF_NEWSLETTER_CRASH_JOURNAL"))
		if err != nil {
			t.Fatal(err)
		}
		keys, err := mailqueue.NewProofKeys("fixture", map[string][]byte{"fixture": bytes.Repeat([]byte{3}, 32)})
		if err != nil {
			t.Fatal(err)
		}
		now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
		n := &newsletter.Service{Store: s, Keys: keys, RateKey: bytes.Repeat([]byte{4}, 32), Origin: "https://mychef.dev", ConsentVersion: "newsletter-v1", Now: func() time.Time { return now }, Lifecycle: j}
		change := &EmailChange{SignUp: &SignUp{Store: s, Keys: keys, RateKey: n.RateKey, Origin: "https://mychef.example.test", Now: n.Now, Lifecycle: j, Newsletter: n}, retirementFault: func(at string) error {
			if at == stage {
				os.Exit(23)
			}
			return nil
		}}
		_, _ = change.Decide(t.Context(), os.Getenv("MYCHEF_NEWSLETTER_CRASH_PROOF"))
		t.Fatal("crash boundary not reached")
	}
	for _, stage := range []string{"prepared", "sqlite", "marker"} {
		t.Run(stage, func(t *testing.T) {
			change, n, j, account, contact, proof := destinationFixture(t)
			backup := filepath.Join(t.TempDir(), "before-transfer.db")
			if err := change.Store.Backup(t.Context(), backup); err != nil {
				t.Fatal(err)
			}
			child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestNewsletterTransferProcessCrashBoundaries$")
			child.Env = append(os.Environ(), "MYCHEF_NEWSLETTER_TRANSFER_CRASH="+stage, "MYCHEF_NEWSLETTER_CRASH_DB="+change.Store.Path, "MYCHEF_NEWSLETTER_CRASH_JOURNAL="+j.Path, "MYCHEF_NEWSLETTER_CRASH_PROOF="+proof)
			out, err := child.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 23 {
				t.Fatalf("expected process crash: %v %s", err, out)
			}
			if stage == "prepared" {
				if !errors.Is(j.CheckReady(t.Context()), store.ErrLifecycleUncertain) {
					t.Fatal("unresolved intent reported ready")
				}
				if _, err := n.Step(t.Context()); !errors.Is(err, newsletter.ErrUnavailable) {
					t.Fatal("newsletter work ignored uncertainty", err)
				}
				destination := filepath.Join(t.TempDir(), "refused.db")
				if err := store.Restore(t.Context(), backup, destination, store.AccountRestoreOptions{JournalPath: j.Path}); !errors.Is(err, store.ErrLifecycleUncertain) {
					t.Fatal("uncertain restore accepted", err)
				}
				if _, err := os.Stat(destination); !os.IsNotExist(err) {
					t.Fatal("unsafe restore published")
				}
				return
			}
			if err := j.ReconcileIntents(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := j.CheckReady(t.Context()); err != nil {
				t.Fatal(err)
			}
			updated, err := New(change.Store).Login(t.Context(), contact.Email, changeFixturePassword)
			if err != nil || updated.ID != account.ID {
				t.Fatal("committed transfer lost", err)
			}
			if _, err := change.Decide(t.Context(), proof); !errors.Is(err, ErrEmailProof) {
				t.Fatal("transfer replay succeeded", err)
			}
			destination := filepath.Join(t.TempDir(), "restored.db")
			if err := store.Restore(t.Context(), backup, destination, store.AccountRestoreOptions{JournalPath: j.Path}); err != nil {
				t.Fatal(err)
			}
			restored, err := store.Open(destination, false)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = restored.Close() }()
			var count int
			if err := restored.SQL.QueryRow("SELECT count(*) FROM users WHERE user_id=?", contact.ID).Scan(&count); err != nil || count != 0 {
				t.Fatal("retired placeholder revived", err)
			}
			if err := restored.SQL.QueryRow("SELECT count(*) FROM users WHERE user_id=?", account.ID).Scan(&count); err != nil || count != 1 {
				t.Fatal("surviving app account erased", err)
			}
		})
	}
}

func TestNewsletterConcurrentCompletionAndWithdrawalKeepOneAccount(t *testing.T) {
	signup, _ := signUpFixture(t)
	n, user, _, withdrawal := contactFixture(t, signup, "reader@example.test", "confirmed")
	anonymous, proof := requestContactSignup(t, signup, user.Email)
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan error, 3)
	for range 2 {
		wg.Go(func() { <-start; results <- signup.Complete(t.Context(), anonymous, contactCompletion(proof)) })
	}
	wg.Go(func() { <-start; results <- n.Unsubscribe(t.Context(), withdrawal) })
	close(start)
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrSignUpProof) {
			t.Fatal(err)
		}
	}
	if success != 2 {
		t.Fatalf("expected one promotion and withdrawal, got %d", success)
	}
	var count int
	if err := signup.Store.SQL.QueryRow("SELECT count(*) FROM users").Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate account", err)
	}
	if state := readContactConsent(t, signup.Store, user.ID); state.Status != "unsubscribed" {
		t.Fatal("promotion restored withdrawn consent")
	}
}

func TestNewsletterPromotionInvalidatesContactAuthorityAndExpiry(t *testing.T) {
	signup, now := signUpFixture(t)
	n, user, _, _ := contactFixture(t, signup, "reader@example.test", "pending")
	j, err := signup.Store.InitializeLifecycleJournal(t.Context(), signup.Store.Path+".lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	var incarnation string
	if err := signup.Store.SQL.QueryRow("SELECT incarnation FROM account_incarnations WHERE user_id=?", user.ID).Scan(&incarnation); err != nil {
		t.Fatal(err)
	}
	actor := store.NewsletterContactAuthority{UserID: user.ID, EmailKey: user.EmailKey, Incarnation: incarnation, RequestReference: "before-promotion"}
	anonymous, proof := requestContactSignup(t, signup, user.Email)
	if err := signup.Complete(t.Context(), anonymous, contactCompletion(proof)); err != nil {
		t.Fatal(err)
	}
	if _, err := signup.Store.ExportNewsletterContact(t.Context(), actor); !errors.Is(err, store.ErrAccountAccess) {
		t.Fatal("obsolete contact export authority accepted", err)
	}
	if _, err := j.EraseNewsletterContact(t.Context(), actor, signup.RateKey, *now, store.AccountDeletionOptions{}); !errors.Is(err, store.ErrAccountAccess) {
		t.Fatal("obsolete contact erasure authority accepted", err)
	}
	*now = now.Add(48 * time.Hour)
	if err := n.Sweep(t.Context(), j, 1, 1, store.AccountDeletionOptions{}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := signup.Store.SQL.QueryRow("SELECT count(*) FROM users WHERE user_id=? AND isNewsletterOnly=0", user.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("newsletter retention deleted app account", err)
	}
}

func TestNewsletterCaptureAndSignupCompletionConverge(t *testing.T) {
	signup, _ := signUpFixture(t)
	anonymous, proof := requestContactSignup(t, signup, "reader@example.test")
	n := &newsletter.Service{Store: signup.Store, Keys: signup.Keys, RateKey: signup.RateKey, Origin: "https://mychef.dev", ConsentVersion: "newsletter-v1", Now: signup.Now}
	if _, err := n.Request(t.Context(), "192.0.2.56", newsletter.Input{Email: "reader@example.test", Locale: "en", ConsentVersion: n.ConsentVersion}); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan error, 2)
	wg.Go(func() { <-start; results <- signup.Complete(t.Context(), anonymous, contactCompletion(proof)) })
	wg.Go(func() { <-start; _, err := n.Step(t.Context()); results <- err })
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := signup.Store.SQL.QueryRow("SELECT count(*) FROM users").Scan(&count); err != nil || count != 1 {
		t.Fatal("capture allocated duplicate user", err)
	}
	if _, err := New(signup.Store).Login(t.Context(), "reader@example.test", contactCompletion(proof).Password); err != nil {
		t.Fatal(err)
	}
}

func TestNewsletterPromotionAndEmailChangeHaveOneWinner(t *testing.T) {
	change, _, _, account, contact, changeProof := destinationFixture(t)
	anonymous, signupProof := requestContactSignup(t, change.SignUp, contact.Email)
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan error, 2)
	wg.Go(func() { <-start; results <- change.Complete(t.Context(), anonymous, contactCompletion(signupProof)) })
	wg.Go(func() { <-start; _, err := change.Decide(t.Context(), changeProof); results <- err })
	close(start)
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrSignUpProof) && !errors.Is(err, ErrEmailProof) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("expected one verified owner, got %d", success)
	}
	var owner store.User
	if err := change.Store.DB.Where("email_key=?", contact.EmailKey).Take(&owner).Error; err != nil || owner.IsNewsletterOnly {
		t.Fatal("destination has no app owner", err)
	}
	if owner.ID != account.ID && owner.ID != contact.ID {
		t.Fatal("destination identity was replaced")
	}
}
