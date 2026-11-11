package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/domain"
	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

func TestNativeProfileCannotWriteAfterExpiryWhileWaitingToExecute(t *testing.T) {
	m, user, cookie, _ := passwordChangeFixture(t)
	tokens, err := m.NativeBind(t.Context(), cookie, "ios", "")
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(2 * time.Second).Unix()
	if err := m.Store.DB.Model(&store.Session{}).Where("hash=?", Hash(tokens.AccessToken)).Update("expires_unix", expires).Error; err != nil {
		t.Fatal(err)
	}
	if err := m.Store.DB.Callback().Update().Before("gorm:update").Register("native-profile-expiry", func(_ *gorm.DB) {
		time.Sleep(time.Until(time.Unix(expires, 0).Add(30 * time.Millisecond)))
	}); err != nil {
		t.Fatal(err)
	}
	input := ProfileUpdate{Username: user.Username, FirstName: "Expired update", LastName: user.LastName, Preferences: domain.EmptyPreferences()}
	if _, err := m.Update(t.Context(), user.ID, input, tokens.AccessToken); !errors.Is(err, ErrSession) {
		t.Fatal("expired native actor wrote a delayed profile update")
	}
	var current store.User
	if err := m.Store.DB.First(&current, user.ID).Error; err != nil || current.FirstName != user.FirstName {
		t.Fatal("expired native profile mutation persisted")
	}
}

func TestNativeLoadRejectsExpiryWhileWaitingForSQLite(t *testing.T) {
	s := openPasswordTestStore(t)
	m := New(s)
	tokens, err := m.NativeStart(t.Context(), "ios")
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(2 * time.Second).Unix()
	if err := s.DB.Model(&store.Session{}).Where("hash=?", Hash(tokens.AccessToken)).Update("expires_unix", expires).Error; err != nil {
		t.Fatal(err)
	}
	conn, err := s.SQL.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { _, err := m.NativeLoad(t.Context(), tokens.AccessToken); finished <- err }()
	time.Sleep(time.Until(time.Unix(expires, 0).Add(30 * time.Millisecond)))
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; !errors.Is(err, ErrSession) {
		t.Fatal("expired access passed the native guard after a SQLite wait")
	}
}
