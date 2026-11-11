package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

func accountDeletionFixture(t *testing.T) (*Store, *LifecycleJournal, AccountActor) {
	t.Helper()
	s, actor := lifecycleExportFixture(t)
	j, err := s.InitializeLifecycleJournal(t.Context(), s.Path+".lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	if err := LinkAccountMail(s.DB, 1, 51); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SQL.ExecContext(t.Context(), `INSERT INTO sessions VALUES('other-session',2,9999999999);
 INSERT INTO email_verification_proofs VALUES('verification',1,'verification-hash','address-tag',9999999999,NULL);
 INSERT INTO pending_email_changes(id,user_id,original_email,original_key,proposed_email,proposed_key,expires) VALUES('owned-change',1,'owner@example.test','owner@example.test','new@example.test','new@example.test',9999999999),('other-change',2,'other@example.test','other@example.test','owner@example.test','owner@example.test',9999999999);
 INSERT INTO email_change_proofs VALUES('owned-proof','owned-change','confirm',NULL),('other-proof','other-change','confirm',NULL);`); err != nil {
		t.Fatal(err)
	}
	return s, j, actor
}

func TestAccountDeletionCompleteOwnedPurgeAndFreshSignup(t *testing.T) {
	s, j, actor := accountDeletionFixture(t)
	ctx := t.Context()
	op := strings.Repeat("1", 32)
	var original string
	if err := s.SQL.QueryRowContext(ctx, "SELECT incarnation FROM account_incarnations WHERE user_id=1").Scan(&original); err != nil {
		t.Fatal(err)
	}
	result, err := j.DeleteAccount(ctx, actor, op, "DELETE", time.Now(), AccountDeletionOptions{})
	if err != nil || result.Operation != op {
		t.Fatal("deletion failed", err)
	}
	var skipped string
	if err := s.SQL.QueryRowContext(ctx, "SELECT notice FROM lifecycle_operations WHERE operation=?", op).Scan(&skipped); err != nil || skipped != "skipped" {
		t.Fatal("missing nonpersonal skipped-notice result", err)
	}
	for _, table := range []string{"users", "menus", "generations", "sessions", "account_incarnations", "shopping_lists", "favorites", "favorite_queue", "favorite_swaps", "feedback_reports", "feedback_ratings", "feedback_rating_receipts", "email_verification_proofs", "pending_email_changes"} {
		column := "user_id"
		if table == "users" {
			column = "user_id"
		}
		var n int
		if err := s.SQL.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE "+column+"=1").Scan(&n); err != nil || n != 0 {
			t.Fatal("owned record survived", table, err)
		}
	}
	for _, q := range []string{"SELECT COUNT(*) FROM meals WHERE creator_id=1", "SELECT COUNT(*) FROM ingredients WHERE recipe_id=27", "SELECT COUNT(*) FROM email_outbox WHERE id=51", "SELECT COUNT(*) FROM email_events"} {
		var n int
		if err := s.SQL.QueryRowContext(ctx, q).Scan(&n); err != nil || n != 0 {
			t.Fatal("owned content survived", err)
		}
	}
	for _, q := range []string{"SELECT COUNT(*) FROM users WHERE user_id=2", "SELECT COUNT(*) FROM meals WHERE creator_id=2", "SELECT COUNT(*) FROM sessions WHERE hash='other-session'", "SELECT COUNT(*) FROM pending_email_changes WHERE id='other-change'", "SELECT COUNT(*) FROM email_change_proofs WHERE hash='other-proof'"} {
		var n int
		if err := s.SQL.QueryRowContext(ctx, q).Scan(&n); err != nil || n != 1 {
			t.Fatal("unrelated data removed", err)
		}
	}
	if deleted, err := j.IsDeleted(ctx, original); err != nil || !deleted {
		t.Fatal("missing restore marker", err)
	}
	work, err := j.WorkFor(ctx, original)
	if err != nil || len(work) != 1 || work[0].Target != "provider-secret-id" {
		t.Fatal("external asset not tracked", err)
	}
	if _, err = j.DeleteAccount(ctx, actor, op, "DELETE", time.Now(), AccountDeletionOptions{}); !errors.Is(err, ErrAccountAccess) {
		t.Fatal("deleted session authenticated on replay", err)
	}
	if _, err := s.SQL.ExecContext(ctx, "INSERT INTO users(user_id,firstname,lastname,email,email_key,password,username) VALUES(1,'Fresh','Account','owner@example.test','owner@example.test','fresh-hash','new-owner')"); err != nil {
		t.Fatal(err)
	}
	var fresh string
	if err := s.SQL.QueryRowContext(ctx, "SELECT incarnation FROM account_incarnations WHERE user_id=1").Scan(&fresh); err != nil || fresh == original {
		t.Fatal("fresh account reused deleted incarnation", err)
	}
	if deleted, err := j.IsDeleted(ctx, fresh); err != nil || deleted {
		t.Fatal("new account treated as old account", err)
	}
	if err := s.Validate(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestAccountDeletionInvalidInputAndTransactionRollback(t *testing.T) {
	for _, stage := range []string{"prepared", "feedback", "mail", "notice", "user", "proof"} {
		t.Run(stage, func(t *testing.T) {
			s, j, actor := accountDeletionFixture(t)
			ctx := t.Context()
			op := strings.Repeat("2", 32)
			injected := errors.New("injected deletion failure")
			opts := AccountDeletionOptions{Fault: func(point string) error {
				if point == stage {
					return injected
				}
				return nil
			}}
			if _, err := j.DeleteAccount(ctx, actor, op, "DELETE", time.Now(), opts); err == nil {
				t.Fatal("injected failure reported success")
			}
			for _, q := range []string{"SELECT COUNT(*) FROM users WHERE user_id=1", "SELECT COUNT(*) FROM sessions WHERE hash='export-session-secret'", "SELECT COUNT(*) FROM feedback_reports WHERE user_id=1", "SELECT COUNT(*) FROM email_outbox WHERE id=51"} {
				var n int
				if err := s.SQL.QueryRowContext(ctx, q).Scan(&n); err != nil || n != 1 {
					t.Fatal("partial account after failure", err)
				}
			}
			if err := j.ReconcileIntents(ctx); err != nil {
				t.Fatal("clean rollback left uncertain deletion", err)
			}
			var n int
			if err := s.SQL.QueryRowContext(ctx, "SELECT COUNT(*) FROM lifecycle_operations").Scan(&n); err != nil || n != 0 {
				t.Fatal("rollback kept deletion proof", err)
			}
		})
	}
	s, j, actor := accountDeletionFixture(t)
	for _, confirmation := range []string{"delete", "DELETE ", "", "Delete"} {
		if _, err := j.DeleteAccount(t.Context(), actor, strings.Repeat("3", 32), confirmation, time.Now(), AccountDeletionOptions{}); !errors.Is(err, ErrAccountConfirmation) {
			t.Fatal("invalid confirmation accepted", err)
		}
	}
	actor.PasswordHash = "wrong"
	if _, err := j.DeleteAccount(t.Context(), actor, strings.Repeat("3", 32), "DELETE", time.Now(), AccountDeletionOptions{}); !errors.Is(err, ErrAccountAccess) {
		t.Fatal("wrong password hash accepted", err)
	}
	var n int
	if err := s.SQL.QueryRow("SELECT COUNT(*) FROM users").Scan(&n); err != nil || n != 2 {
		t.Fatal("invalid request deleted account", err)
	}
}

func TestAccountDeletionCommitInterruptionAndFinalNotice(t *testing.T) {
	for _, stage := range []string{"sqlite", "marker"} {
		t.Run(stage, func(t *testing.T) {
			s, j, actor := accountDeletionFixture(t)
			ctx := t.Context()
			op := strings.Repeat("4", 32)
			injected := errors.New("lost response")
			opts := AccountDeletionOptions{Fault: func(point string) error {
				if point == stage {
					return injected
				}
				return nil
			}, FinalNotice: func(tx *gorm.DB, user User, operation string) error {
				return tx.Exec("INSERT INTO email_outbox(key,recipient,template,data,state,next_at,created_at,updated_at) VALUES(?,?,'account-deleted/v1','{}','queued',1,1,1)", "deleted/"+operation, user.Email).Error
			}}
			if _, err := j.DeleteAccount(ctx, actor, op, "DELETE", time.Now(), opts); !errors.Is(err, ErrLifecycleUncertain) {
				t.Fatal("interrupted commit misreported", err)
			}
			if err := j.ReconcileIntents(ctx); err != nil {
				t.Fatal(err)
			}
			var n int
			if err := s.SQL.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE user_id=1").Scan(&n); err != nil || n != 0 {
				t.Fatal("committed deletion returned", err)
			}
		})
	}
}

func TestAccountDeletionVerifiedNoticeJoinsTransaction(t *testing.T) {
	s, j, actor := accountDeletionFixture(t)
	ctx := t.Context()
	if _, err := s.SQL.ExecContext(ctx, "UPDATE users SET email_verified_at=1 WHERE user_id=1"); err != nil {
		t.Fatal(err)
	}
	called := false
	opts := AccountDeletionOptions{FinalNotice: func(tx *gorm.DB, user User, operation string) error {
		called = true
		if user.Email != "owner@example.test" {
			return errors.New("wrong verified notice recipient")
		}
		if err := tx.Exec("INSERT INTO email_outbox(id,key,recipient,template,data,state,next_at,created_at,updated_at) VALUES(52,?,?,'account-deleted/v1','{}','queued',1,1,1)", "deleted/"+operation, user.Email).Error; err != nil {
			return err
		}
		return LinkAccountMail(tx, user.ID, 52)
	}}
	if _, err := j.DeleteAccount(ctx, actor, strings.Repeat("5", 32), "DELETE", time.Now(), opts); err != nil || !called {
		t.Fatal("verified deletion did not atomically queue notice", err)
	}
	var status string
	if err := s.SQL.QueryRowContext(ctx, "SELECT notice FROM lifecycle_operations").Scan(&status); err != nil || status != "queued" {
		t.Fatal("missing queued-notice result", err)
	}
	var count int
	if err := s.SQL.QueryRowContext(ctx, "SELECT COUNT(*) FROM email_outbox WHERE id=52 AND recipient='owner@example.test'").Scan(&count); err != nil || count != 1 {
		t.Fatal("deletion notice lost", err)
	}
	if err := s.SQL.QueryRowContext(ctx, "SELECT COUNT(*) FROM account_mail_owners WHERE outbox_id=52").Scan(&count); err != nil || count != 1 {
		t.Fatal("notice snapshot untracked", err)
	}
}

func TestLifecycleMigrationRefusesAmbiguousLegacyMail(t *testing.T) {
	s := lifecycleLegacyStore(t)
	ctx := t.Context()
	// Historical notices have no durable creating-user link. The recipient may
	// now belong to somebody else, so migration must not guess from the address.
	if _, err := s.SQL.ExecContext(ctx, `INSERT INTO email_outbox(key,recipient,template,data,state,next_at,created_at,updated_at) VALUES('password-changed/opaque','old-address@example.test','password-changed/v1','{"timestamp":"2026-11-07T12:00:00Z"}','accepted',1,1,1)`); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); !errors.Is(err, ErrAccountMailOwnership) {
		t.Fatal("ambiguous prior notice ownership was guessed", err)
	}
	var version, count int
	if err := s.SQL.QueryRowContext(ctx, "SELECT version FROM schema_migrations").Scan(&version); err != nil || version != 11 {
		t.Fatal("failed migration partially changed schema", err)
	}
	if err := s.SQL.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&count); err != nil || count != 2 {
		t.Fatal("migration altered existing users", err)
	}
	if err := s.SQL.QueryRowContext(ctx, "SELECT COUNT(*) FROM email_outbox WHERE recipient='old-address@example.test'").Scan(&count); err != nil || count != 1 {
		t.Fatal("migration discarded unowned mail", err)
	}
}
