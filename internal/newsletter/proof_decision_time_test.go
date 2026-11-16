package newsletter

import (
	"errors"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/store"
)

func TestProofDecisionAcrossSecondBoundary(t *testing.T) {
	for _, mode := range []string{"ordinary", "tracked", "confirmed", "reduced"} {
		for _, purpose := range []string{"confirm", "unsubscribe"} {
			if mode == "confirmed" && purpose == "confirm" {
				continue // A consumed confirmation is covered by the replay check.
			}
			t.Run(mode+"/"+purpose, func(t *testing.T) {
				s, clock := fixture(t)
				request(t, s, "decision-clock@example.test", "en")
				step(t, s)
				confirm, withdraw := links(t, s)
				var id int64
				if err := s.Store.SQL.QueryRowContext(t.Context(), "SELECT user_id FROM users").Scan(&id); err != nil {
					t.Fatal(err)
				}
				var journal *store.LifecycleJournal
				if mode != "ordinary" {
					journal = minimisationJournal(t, s)
				}
				if mode == "confirmed" {
					if err := s.Confirm(t.Context(), confirm); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "reduced" {
					*clock = clock.Add(25 * time.Hour)
					if err := s.Minimise(t.Context(), journal); err != nil {
						t.Fatal(err)
					}
					if purpose == "confirm" {
						if err := journal.CleanupNewsletterFiles(t.Context()); err != nil {
							t.Fatal(err)
						}
						request(t, s, "decision-clock@example.test", "nl-BE")
						step(t, s)
						confirm, withdraw = links(t, s)
					}
				}
				// Every independent observation crosses a second; no sleeps or
				// assumptions about the number of internal clock calls are needed.
				s.Now = func() time.Time { *clock = clock.Add(time.Second); return *clock }
				proof, want := withdraw, "unsubscribed"
				apply := s.Unsubscribe
				if purpose == "confirm" {
					proof, want, apply = confirm, "confirmed", s.Confirm
				}
				if err := apply(t.Context(), proof); err != nil {
					t.Fatal("valid advancing-clock proof failed", err)
				}
				var actualID int64
				var status string
				if err := s.Store.SQL.QueryRowContext(t.Context(), "SELECT user_id,status FROM newsletter_subscriptions").Scan(&actualID, &status); err != nil || actualID != id || status != want {
					t.Fatal("proof did not preserve identity and record its choice", actualID, status, err)
				}
				if journal != nil {
					if err := journal.ValidateNewsletterIdentities(t.Context()); err != nil {
						t.Fatal("SQL/journal post-state differs", err)
					}
				}
				if err := apply(t.Context(), proof); purpose == "confirm" && !errors.Is(err, ErrProof) || purpose == "unsubscribe" && err != nil {
					t.Fatal("proof replay behavior changed", err)
				}
			})
		}
	}
}

func TestProofDecisionFailureRemainsRetriable(t *testing.T) {
	for _, purpose := range []string{"confirm", "unsubscribe"} {
		t.Run(purpose, func(t *testing.T) {
			s, clock := fixture(t)
			request(t, s, "decision-retry@example.test", "en")
			step(t, s)
			confirm, withdraw := links(t, s)
			journal := minimisationJournal(t, s)
			s.Now = func() time.Time { *clock = clock.Add(time.Second); return *clock }
			proof, apply := withdraw, s.Unsubscribe
			if purpose == "confirm" {
				proof, apply = confirm, s.Confirm
			}
			if err := s.Store.DB.Exec("CREATE TRIGGER fail_proof_choice BEFORE UPDATE ON newsletter_subscriptions BEGIN SELECT RAISE(ABORT,'synthetic proof failure'); END").Error; err != nil {
				t.Fatal(err)
			}
			if err := apply(t.Context(), proof); !errors.Is(err, ErrUnavailable) {
				t.Fatal("injected write failure did not refuse transition", err)
			}
			var status string
			var consumed bool
			if err := s.Store.SQL.QueryRowContext(t.Context(), "SELECT status FROM newsletter_subscriptions").Scan(&status); err != nil || status != "pending" {
				t.Fatal("failed transition changed choice", err)
			}
			if err := s.Store.SQL.QueryRowContext(t.Context(), "SELECT consumed_unix IS NOT NULL FROM newsletter_proofs WHERE purpose=?", purpose).Scan(&consumed); err != nil || consumed {
				t.Fatal("failed transition consumed proof", err)
			}
			if err := journal.CheckReady(t.Context()); err != nil {
				t.Fatal("proven rollback left uncertain journal", err)
			}
			if err := s.Store.DB.Exec("DROP TRIGGER fail_proof_choice").Error; err != nil {
				t.Fatal(err)
			}
			if err := apply(t.Context(), proof); err != nil {
				t.Fatal("same advancing-clock proof could not retry", err)
			}
		})
	}
}

func TestProofDecisionStillChecksExpiryAfterJournalDecision(t *testing.T) {
	s, _ := fixture(t)
	request(t, s, "decision-expiry@example.test", "en")
	step(t, s)
	confirm, _ := links(t, s)
	journal := minimisationJournal(t, s)
	var expires int64
	if err := s.Store.SQL.QueryRowContext(t.Context(), "SELECT expires_unix FROM newsletter_proofs WHERE purpose='confirm'").Scan(&expires); err != nil {
		t.Fatal(err)
	}
	calls := 0
	s.Now = func() time.Time {
		calls++
		if calls < 3 {
			return time.Unix(expires-1, 0)
		}
		return time.Unix(expires, 0)
	}
	if err := s.Confirm(t.Context(), confirm); !errors.Is(err, ErrProof) {
		t.Fatal("proof expiring after journal decision was not rejected", err)
	}
	if err := journal.CheckReady(t.Context()); err != nil {
		t.Fatal("expired transition left uncertain journal", err)
	}
}
