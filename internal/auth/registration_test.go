package auth

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/email/mailqueue"
	"github.com/LowGok/my-chef/internal/store"
)

func signUpFixture(t *testing.T) (*SignUp, *time.Time) {
	t.Helper()
	s := openPasswordTestStore(t)
	keys, err := mailqueue.NewProofKeys("fixture", map[string][]byte{"fixture": bytes.Repeat([]byte{3}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	return &SignUp{Store: s, Keys: keys, RateKey: bytes.Repeat([]byte{4}, 32), Origin: "https://mychef.example.test",
		Now: func() time.Time { return now }}, &now
}

func signUpLink(t *testing.T, signup *SignUp, now time.Time) string {
	t.Helper()
	var row mailqueue.Record
	if err := signup.Store.DB.Where("template = ?", mailqueue.SignUpProofTemplate).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	fields, _, err := mailqueue.OpenProtected(signup.Keys, row, now, false)
	if err != nil {
		t.Fatal(err)
	}
	link, err := url.Parse(fields["link"])
	if err != nil || link.Scheme != "https" || link.Host != "mychef.example.test" || link.Path != "/app/signup" || link.Fragment == "" {
		t.Fatal("the fixture delivery did not contain a proof on the trusted origin")
	}
	return link.Fragment
}

func TestSignUpDoesNotCreateUsableAccountBeforeMailboxProof(t *testing.T) {
	ctx := context.Background()
	signup, now := signUpFixture(t)
	manager := New(signup.Store)
	anonymous, err := manager.Rotate(ctx, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	const existing = "Existing@Example.Test"
	old := Registration{Email: existing, Password: "original password fixture", Username: "existing", FirstName: "Existing", LastName: "User"}
	if _, err := manager.Register(ctx, old); err != nil {
		t.Fatal(err)
	}
	details := SignUpDetails{FirstName: "Untrusted", LastName: "Suggestion", Username: "suggested"}
	const unclaimed = "New@Example.Test"
	for _, address := range []string{"existing@example.test", unclaimed} {
		if wait, err := signup.Request(ctx, "192.0.2.1", SignUpRequest{Email: address, SignUpDetails: details}); err != nil || wait != 0 {
			t.Fatalf("valid registration request failed: %v", err)
		}
	}
	var jobs, users, messages int64
	for _, count := range []struct {
		table string
		want  int64
		dst   *int64
	}{{"signup_requests", 2, &jobs}, {"users", 1, &users}, {"email_outbox", 0, &messages}} {
		if err := signup.Store.DB.Table(count.table).Count(count.dst).Error; err != nil || *count.dst != count.want {
			t.Fatalf("request did unexpected account-dependent work: %s, %v", count.table, err)
		}
	}
	var queued []store.SignUpJob
	if err := signup.Store.DB.Find(&queued).Error; err != nil {
		t.Fatal(err)
	}
	for _, job := range queued {
		if strings.Contains(job.Draft, unclaimed) || strings.Contains(job.Draft, existing) || strings.Contains(job.Draft, details.Username) {
			t.Fatal("a pending job contains plaintext registration details")
		}
	}
	for range 2 {
		if worked, err := signup.Step(ctx); err != nil || !worked {
			t.Fatalf("sign-up worker did not process a request: %v", err)
		}
	}
	if err := signup.Store.DB.Table("email_outbox").Count(&messages).Error; err != nil || messages != 1 {
		t.Fatal("worker did not issue exactly one proof for the unclaimed address")
	}
	const chosen = "chosen after mailbox proof"
	for _, address := range []string{existing, unclaimed} {
		if _, err := manager.Login(ctx, address, chosen); !errors.Is(err, ErrCredentials) {
			t.Fatal("ordinary login revealed whether a requested address was unclaimed")
		}
	}
	proof := signUpLink(t, signup, *now)
	got, err := signup.Inspect(ctx, proof)
	if err != nil || got != details {
		t.Fatal("the proof did not return its editable suggestions")
	}
	confirmed := SignUpDetails{FirstName: "Mailbox", LastName: "Owner", Username: "confirmed"}
	if err := signup.Complete(ctx, anonymous, SignUpCompletion{Proof: proof, Password: "short", SignUpDetails: confirmed}); !errors.Is(err, ErrPasswordLength) {
		t.Fatal("sign-up did not reuse the password policy")
	}
	if err := manager.Revoke(ctx, anonymous); err != nil {
		t.Fatal(err)
	}
	if err := signup.Complete(ctx, anonymous, SignUpCompletion{Proof: proof, Password: chosen, SignUpDetails: confirmed}); !errors.Is(err, ErrSession) {
		t.Fatal("a revoked anonymous session could create the verified account")
	}
	anonymous, err = manager.Rotate(ctx, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	var failures sync.WaitGroup
	outcome := make(chan error, 2)
	for range 2 {
		failures.Go(func() {
			outcome <- signup.Complete(ctx, anonymous, SignUpCompletion{Proof: proof, Password: chosen, SignUpDetails: confirmed})
		})
	}
	failures.Wait()
	close(outcome)
	successes := 0
	for err := range outcome {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrSignUpProof) {
			t.Fatalf("concurrent proof replay returned unexpected error: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("expected one proof consumption, got %d", successes)
	}
	u, err := manager.Login(ctx, "new@example.test", chosen)
	if err != nil || u.Email != unclaimed || u.Username != confirmed.Username || u.EmailVerifiedAt == nil {
		t.Fatalf("verified account did not use the mailbox owner's choices: %v", err)
	}
	if _, err := signup.Inspect(ctx, proof); !errors.Is(err, ErrSignUpProof) {
		t.Fatal("a consumed proof exposed its draft")
	}
	var protected string
	if err := signup.Store.SQL.QueryRowContext(ctx, "SELECT draft FROM signup_proofs LIMIT 1").Scan(&protected); err != nil || protected != "" {
		t.Fatal("consuming the proof did not erase its encrypted draft")
	}
}

func TestSignUpExpiryAndLateAddressClaim(t *testing.T) {
	ctx := context.Background()
	for _, scenario := range []string{"expiry", "late claim"} {
		t.Run(scenario, func(t *testing.T) {
			signup, now := signUpFixture(t)
			anonymous, err := New(signup.Store).Rotate(ctx, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			details := SignUpDetails{FirstName: "Test", LastName: "User", Username: "suggested"}
			const address = "same@example.test"
			if _, err := signup.Request(ctx, "192.0.2.2", SignUpRequest{Email: address, SignUpDetails: details}); err != nil {
				t.Fatal(err)
			}
			if _, err := signup.Step(ctx); err != nil {
				t.Fatal(err)
			}
			proof := signUpLink(t, signup, *now)
			if scenario == "expiry" {
				*now = now.Add(24 * time.Hour)
			} else {
				if _, err := New(signup.Store).Register(ctx, Registration{Email: address,
					FirstName: "Already", LastName: "Claimed", Username: "claimed", Password: "original legacy user pass"}); err != nil {
					t.Fatal(err)
				}
			}
			if err := signup.Complete(ctx, anonymous, SignUpCompletion{Proof: proof, Password: "would replace the password", SignUpDetails: details}); !errors.Is(err, ErrSignUpProof) {
				t.Fatal("an expired or now-conflicting proof remained usable")
			}
		})
	}
}
