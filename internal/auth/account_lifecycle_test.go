package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

func TestAccountDeletionErasesAttributedRecoveryAndPreservesOtherRequests(t *testing.T) {
	signup, now := signUpFixture(t)
	s := signup.Store
	ctx := t.Context()
	manager := New(s)
	manager.RecoveryRateKey = signup.RateKey
	user := store.User{FirstName: "Owner", LastName: "Fixture", Username: "owner", Email: "owner@example.test", Password: "credential", EmailVerifiedAt: store.Ptr(now.Unix())}
	other := store.User{FirstName: "Other", LastName: "Fixture", Username: "other", Email: "other@example.test", Password: "different", EmailVerifiedAt: store.Ptr(now.Unix())}
	if err := s.DB.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	ownedTag, err := store.EmailRateBucket(signup.RateKey, "recovery-job/address", user.EmailKey)
	if err != nil {
		t.Fatal(err)
	}
	otherTag, err := store.EmailRateBucket(signup.RateKey, "recovery-job/address", other.EmailKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SQL.ExecContext(ctx, "UPDATE recovery_clock SET sequence=50"); err != nil {
		t.Fatal(err)
	}
	for i, tag := range []string{ownedTag, otherTag} {
		if err := s.DB.Create(&store.RecoveryJob{AcceptedOrder: int64(i + 1), AddressTag: tag, ExpiresNS: now.Add(time.Minute).UnixNano(), Draft: "protected-fixture"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DB.Create(&store.Session{Hash: "owner-session", UserID: &user.ID, ExpiresUnix: now.Add(time.Hour).Unix()}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Create(&store.PendingEmailChange{ID: "other-pending", UserID: other.ID, OriginalEmail: other.Email, OriginalKey: other.EmailKey, ProposedEmail: user.Email, ProposedKey: user.EmailKey, Expires: now.Add(time.Hour).Unix()}).Error; err != nil {
		t.Fatal(err)
	}
	// This independent pre-account request must not be erased just because its
	// suggested address also appears in the deleting account's profile.
	if err := s.DB.Create(&store.SignUpJob{ID: "independent-request", Requested: now.Unix(), Expires: now.Add(time.Hour).Unix(), Draft: "protected-independent-fixture"}).Error; err != nil {
		t.Fatal(err)
	}
	j, err := s.InitializeLifecycleJournal(ctx, s.Path+".lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	opts := store.AccountDeletionOptions{Cleanup: func(tx *gorm.DB, u store.User) error { return manager.EraseAccountLinks(ctx, tx, u, *now) }}
	if _, err := j.DeleteAccount(ctx, store.AccountActor{UserID: user.ID, SessionHash: "owner-session", PasswordHash: user.Password}, strings.Repeat("a", 32), "DELETE", *now, opts); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.SQL.QueryRowContext(ctx, "SELECT COUNT(*) FROM recovery_jobs WHERE address_tag=?", ownedTag).Scan(&count); err != nil || count != 0 {
		t.Fatal("owned recovery job retained", err)
	}
	for _, query := range []string{"SELECT COUNT(*) FROM recovery_jobs", "SELECT COUNT(*) FROM signup_requests WHERE id='independent-request'", "SELECT COUNT(*) FROM pending_email_changes WHERE id='other-pending'"} {
		if err := s.SQL.QueryRowContext(ctx, query).Scan(&count); err != nil || count != 1 {
			t.Fatal("other request removed", err)
		}
	}
}
