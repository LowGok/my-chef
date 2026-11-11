package auth

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/store"
)

func TestChangePasswordAtomicRollback(t *testing.T) {
	for _, trigger := range []struct {
		name, sql string
	}{
		{"hash update", "BEFORE UPDATE OF password ON users"},
		{"session deletion", "BEFORE DELETE ON sessions"},
		{"replacement session", "BEFORE INSERT ON sessions"},
		{"outbox enqueue", "BEFORE INSERT ON email_outbox"},
	} {
		t.Run(trigger.name, func(t *testing.T) {
			ctx := context.Background()
			m, u, first, second := passwordChangeFixture(t)
			if _, err := m.Store.SQL.Exec("CREATE TRIGGER reject_change " + trigger.sql + " BEGIN SELECT RAISE(ABORT, 'injected failure'); END"); err != nil {
				t.Fatal(err)
			}
			_, token, err := m.ChangePassword(ctx, u.ID, first, "original test phrase", "replacement test phrase")
			if err == nil || token != "" {
				t.Fatal("a failed transaction returned success or a session token")
			}
			assertPasswordChangeUnwritten(t, m, u, first, second)
		})
	}
}

func TestChangePasswordRejectsInvalidRequestsWithoutMutation(t *testing.T) {
	ctx := context.Background()
	m, u, first, second := passwordChangeFixture(t)
	for _, tc := range []struct {
		name, token, old, next string
		want                   error
	}{
		{"wrong before policy", first, "wrong", "short", ErrCurrentPassword},
		{"missing current", first, "", "replacement test phrase", ErrCurrentPassword},
		{"too short", first, "original test phrase", "short", ErrPasswordLength},
		{"common", first, "original test phrase", "1234567890", ErrPasswordCommon},
		{"reuse", first, "original test phrase", "original test phrase", ErrPasswordReuse},
		{"inactive session", strings.Repeat("0", 64), "original test phrase", "replacement test phrase", ErrSession},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, token, err := m.ChangePassword(ctx, u.ID, tc.token, tc.old, tc.next); token != "" || !errors.Is(err, tc.want) {
				t.Fatalf("unexpected result: %v", err)
			}
			assertPasswordChangeUnwritten(t, m, u, first, second)
		})
	}
}

func TestChangePasswordRotatesSessionsAndQueuesNotice(t *testing.T) {
	ctx := context.Background()
	m, u, first, second := passwordChangeFixture(t)
	other := addLegacyUser(t, m.Store, "unrelated", "unrelated", 4)
	unrelated, err := m.Rotate(ctx, &other.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now().UTC().Truncate(time.Second)
	replacement := strings.Repeat("😀", 128)
	user, token, err := m.ChangePassword(ctx, u.ID, first, "original test phrase", replacement)
	if err != nil || user.ID != u.ID || token == first || CSRF(token) == CSRF(first) {
		t.Fatal("password change did not rotate the current session")
	}
	for _, old := range []string{first, second} {
		if _, err := m.Load(ctx, old); !errors.Is(err, ErrSession) {
			t.Fatal("an old session remains valid")
		}
	}
	if _, err := m.Load(ctx, unrelated); err != nil {
		t.Fatal("another account's session changed")
	}
	if s, err := m.Load(ctx, token); err != nil || s.UserID == nil || *s.UserID != u.ID {
		t.Fatal("the replacement session is not valid")
	}
	var count int
	if err := m.Store.SQL.QueryRow("SELECT count(*) FROM sessions WHERE user_id = ?", u.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("expected exactly one session")
	}
	if _, err := m.Login(ctx, u.Email, "original test phrase"); !errors.Is(err, ErrCredentials) {
		t.Fatal("old password still works")
	}
	if _, err := m.Login(ctx, u.Email, replacement); err != nil {
		t.Fatal("new password does not work")
	}
	if err := m.Store.SQL.QueryRow("SELECT count(*) FROM email_outbox").Scan(&count); err != nil || count != 1 {
		t.Fatal("expected one security notice")
	}
	var key, to, template, data, state string
	err = m.Store.SQL.QueryRow("SELECT key, recipient, template, data, state FROM email_outbox").Scan(&key, &to, &template, &data, &state)
	if err != nil || to != u.Email || template != "password-changed/v1" || state != "queued" ||
		key == token || strings.Contains(key, Hash(token)) || strings.Contains(data, replacement) {
		t.Fatal("unexpected outbox data")
	}
	var fields map[string]string
	if err := json.Unmarshal([]byte(data), &fields); err != nil || len(fields) != 1 {
		t.Fatal("unexpected notice fields")
	}
	stamp, err := time.Parse(time.RFC3339, fields["timestamp"])
	if err != nil || stamp.Before(before) || stamp.After(time.Now()) {
		t.Fatal("notice timestamp does not describe the change")
	}
}

func passwordChangeFixture(t *testing.T) (*Manager, store.User, string, string) {
	t.Helper()
	ctx := context.Background()
	m := New(openPasswordTestStore(t))
	u, err := m.Register(ctx, Registration{
		Email: "change@example.test", Username: "change", FirstName: "Change", LastName: "Test", Password: "original test phrase",
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := m.Rotate(ctx, &u.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Rotate(ctx, &u.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	return m, u, first, second
}

func assertPasswordChangeUnwritten(t *testing.T, m *Manager, u store.User, first, second string) {
	t.Helper()
	ctx := context.Background()
	var current store.User
	if err := m.Store.DB.First(&current, u.ID).Error; err != nil || current.Password != u.Password {
		t.Fatal("failed password change wrote a credential")
	}
	for _, token := range []string{first, second} {
		if _, err := m.Load(ctx, token); err != nil {
			t.Fatal("failed password change revoked an existing session")
		}
	}
	var count int
	if err := m.Store.SQL.QueryRow("SELECT count(*) FROM sessions WHERE user_id = ?", u.ID).Scan(&count); err != nil || count != 2 {
		t.Fatal("failed password change wrote a replacement session")
	}
	if err := m.Store.SQL.QueryRow("SELECT count(*) FROM email_outbox").Scan(&count); err != nil || count != 0 {
		t.Fatal("failed password change queued email")
	}
}

func TestCommittedPasswordChangeRejectsAPreviouslyVerifiedLogin(t *testing.T) {
	ctx := context.Background()
	m, user, first, _ := passwordChangeFixture(t)
	anonymous, err := m.Rotate(ctx, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	stale, err := m.verifyLogin(ctx, user.Email, "original test phrase")
	if err != nil {
		t.Fatal(err)
	}

	_, replacement, err := m.ChangePassword(ctx, user.ID, first, "original test phrase", "a different test phrase")
	if err != nil {
		t.Fatal(err)
	}
	if _, token, err := m.completeLogin(ctx, stale, anonymous, true); token != "" || !errors.Is(err, ErrCredentials) {
		t.Fatal("a sign-in verified before the change created a session afterward")
	}
	var count int
	if err := m.Store.SQL.QueryRow("SELECT count(*) FROM sessions WHERE user_id = ?", user.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("the committed change did not leave exactly one active user session")
	}
	if _, err := m.Load(ctx, replacement); err != nil {
		t.Fatal("stale sign-in altered the replacement session")
	}
	if _, err := m.Load(ctx, anonymous); err != nil {
		t.Fatal("stale sign-in mutated the anonymous session")
	}
}
