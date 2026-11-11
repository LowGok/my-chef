package auth

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/store"
)

func TestNativeCredentialsAreHashedAndSeparatedFromBrowser(t *testing.T) {
	s := openPasswordTestStore(t)
	m := New(s)
	ctx := t.Context()
	for _, platform := range []string{"web", "", "ios\n"} {
		if _, err := m.NativeStart(ctx, platform); !errors.Is(err, ErrInput) {
			t.Fatal("invalid native platform accepted")
		}
	}
	tokens, err := m.NativeStart(ctx, "ios")
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens.AccessToken) != 64 || len(tokens.RefreshToken) != 64 || tokens.AccessToken == tokens.RefreshToken {
		t.Fatal("native credential entropy or separation")
	}
	current, err := m.NativeLoad(ctx, tokens.AccessToken)
	if err != nil || current.Session.UserID != nil || current.Platform != "ios" {
		t.Fatalf("anonymous native authority: %v", err)
	}
	if _, err := m.Load(ctx, tokens.AccessToken); !errors.Is(err, ErrSession) {
		t.Fatal("native access accepted as a browser session")
	}
	if _, err := m.NativeLoad(ctx, tokens.RefreshToken); !errors.Is(err, ErrSession) {
		t.Fatal("refresh accepted as access")
	}
	if _, err := m.NativeRefresh(ctx, tokens.AccessToken); !errors.Is(err, ErrSession) {
		t.Fatal("access accepted as refresh")
	}
	cookie, err := m.Rotate(ctx, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.NativeLoad(ctx, cookie); !errors.Is(err, ErrSession) {
		t.Fatal("browser cookie accepted as native authority")
	}
	var refresh store.NativeRefresh
	if err := s.DB.Where("hash=?", Hash(tokens.RefreshToken)).Take(&refresh).Error; err != nil {
		t.Fatal(err)
	}
	if refresh.Hash == tokens.RefreshToken || refresh.ConsumedUnix != nil || refresh.FamilyID != current.FamilyID {
		t.Fatal("refresh was not stored as an unconsumed family-bound digest")
	}
	var raw int64
	for _, table := range []string{"sessions", "native_accesses", "native_refreshes"} {
		if err := s.DB.Table(table).Where("hash IN ?", []string{tokens.AccessToken, tokens.RefreshToken}).Count(&raw).Error; err != nil || raw != 0 {
			t.Fatal("raw credentials persisted")
		}
	}
	if tokens.AccessExpiresUnix > time.Now().Add(6*time.Minute).Unix() || tokens.SessionExpiresUnix > time.Now().Add(time.Hour).Unix() {
		t.Fatal("anonymous session lifetime extended")
	}
}

func TestNativeRefreshRotatesAndReplayRevokesOnlyItsFamily(t *testing.T) {
	s := openPasswordTestStore(t)
	m := New(s)
	ctx := t.Context()
	first, err := m.NativeStart(ctx, "ios")
	if err != nil {
		t.Fatal(err)
	}
	other, err := m.NativeStart(ctx, "android")
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := m.NativeRefresh(ctx, first.RefreshToken)
	if err != nil || rotated.AccessToken == first.AccessToken || rotated.RefreshToken == first.RefreshToken {
		t.Fatalf("refresh rotation: %v", err)
	}
	if rotated.SessionExpiresUnix != first.SessionExpiresUnix {
		t.Fatal("rotation extended family lifetime")
	}
	if _, err := m.NativeLoad(ctx, first.AccessToken); !errors.Is(err, ErrSession) {
		t.Fatal("old access survived refresh")
	}
	if _, err := m.NativeLoad(ctx, rotated.AccessToken); err != nil {
		t.Fatal(err)
	}
	if _, err := m.NativeRefresh(ctx, first.RefreshToken); !errors.Is(err, ErrSession) {
		t.Fatal("consumed refresh accepted")
	}
	if _, err := m.NativeLoad(ctx, rotated.AccessToken); !errors.Is(err, ErrSession) {
		t.Fatal("replay revocation was rolled back")
	}
	if _, err := m.NativeRefresh(ctx, rotated.RefreshToken); !errors.Is(err, ErrSession) {
		t.Fatal("family refresh survived replay")
	}
	if _, err := m.NativeLoad(ctx, other.AccessToken); err != nil {
		t.Fatal("replay revoked another installation")
	}
}

func TestNativeConcurrentRefreshCannotLeaveTwoActiveCredentials(t *testing.T) {
	s := openPasswordTestStore(t)
	m := New(s)
	tokens, err := m.NativeStart(t.Context(), "ios")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan NativeTokens, 2)
	errorsSeen := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			next, err := m.NativeRefresh(t.Context(), tokens.RefreshToken)
			results <- next
			errorsSeen <- err
		})
	}
	wg.Wait()
	close(results)
	close(errorsSeen)
	accepted := 0
	for err := range errorsSeen {
		if err == nil {
			accepted++
		} else if !errors.Is(err, ErrSession) {
			t.Fatal(err)
		}
	}
	if accepted != 1 {
		t.Fatalf("refresh accepted %d times", accepted)
	}
	for next := range results {
		if next.AccessToken != "" {
			if _, err := m.NativeLoad(t.Context(), next.AccessToken); !errors.Is(err, ErrSession) {
				t.Fatal("concurrent replay left issued credentials active")
			}
		}
	}
}

func TestNativeBindingExpiryAndSecurityRevocation(t *testing.T) {
	s := openPasswordTestStore(t)
	m := New(s)
	ctx := t.Context()
	u := addLegacyUser(t, s, "native-owner", "Garden native account!", 4)
	cookie, err := m.Rotate(ctx, &u.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := m.NativeBind(ctx, cookie, "android", "")
	if err != nil {
		t.Fatal(err)
	}
	current, err := m.NativeLoad(ctx, tokens.AccessToken)
	if err != nil || current.Session.UserID == nil || *current.Session.UserID != u.ID {
		t.Fatalf("native owner binding: %v", err)
	}
	if _, err := m.Load(ctx, cookie); !errors.Is(err, ErrSession) {
		t.Fatal("native anchor was usable as browser cookie")
	}
	if tokens.SessionExpiresUnix > time.Now().Add(7*24*time.Hour).Unix() {
		t.Fatal("native family exceeds existing signed-in lifetime")
	}
	if err := s.DB.Model(&store.Session{}).Where("hash=?", Hash(tokens.AccessToken)).Update("expires_unix", time.Now().Unix()-1).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := m.NativeLoad(ctx, tokens.AccessToken); !errors.Is(err, ErrSession) {
		t.Fatal("expired native access accepted")
	}
	next, err := m.NativeRefresh(ctx, tokens.RefreshToken)
	if err != nil {
		t.Fatal("expired access prevented valid family refresh")
	}
	if err := s.DB.Where("user_id=?", u.ID).Delete(&store.Session{}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := m.NativeLoad(ctx, next.AccessToken); !errors.Is(err, ErrSession) {
		t.Fatal("security revocation did not remove native access")
	}
	if _, err := m.NativeRefresh(ctx, next.RefreshToken); !errors.Is(err, ErrSession) {
		t.Fatal("security revocation did not remove native refresh")
	}
	for _, table := range []string{"native_families", "native_accesses", "native_refreshes"} {
		var count int64
		if err := s.DB.Table(table).Count(&count).Error; err != nil || count != 0 {
			t.Fatal("revoked native records retained")
		}
	}
	for _, invalid := range []string{"", strings.Repeat("z", 64), strings.Repeat("a", 65)} {
		if _, err := m.NativeRefresh(ctx, invalid); !errors.Is(err, ErrSession) {
			t.Fatal("malformed refresh accepted")
		}
	}
}

func TestNativeRefreshSurvivesRestartButNotFamilyExpiry(t *testing.T) {
	s := openPasswordTestStore(t)
	m := New(s)
	tokens, err := m.NativeStart(t.Context(), "ios")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(s.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	m = New(reopened)
	next, err := m.NativeRefresh(t.Context(), tokens.RefreshToken)
	if err != nil {
		t.Fatal("persisted refresh could not rotate after restart")
	}
	current, err := m.NativeLoad(t.Context(), next.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.DB.Model(&store.NativeFamily{}).Where("id=?", current.FamilyID).Update("expires_unix", time.Now().Unix()-1).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := m.NativeLoad(t.Context(), next.AccessToken); !errors.Is(err, ErrSession) {
		t.Fatal("expired family access accepted")
	}
	if _, err := m.NativeRefresh(t.Context(), next.RefreshToken); !errors.Is(err, ErrSession) {
		t.Fatal("expired family refresh accepted")
	}
}

func TestNativeRefreshStorageFailureDoesNotConsumeTheCredential(t *testing.T) {
	s := openPasswordTestStore(t)
	m := New(s)
	tokens, err := m.NativeStart(t.Context(), "android")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Exec(`CREATE TRIGGER native_refresh_storage_failure BEFORE INSERT ON native_refreshes
BEGIN SELECT RAISE(ABORT,'synthetic storage failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := m.NativeRefresh(t.Context(), tokens.RefreshToken); err == nil || errors.Is(err, ErrSession) {
		t.Fatal("storage failure was not surfaced")
	}
	if _, err := m.NativeLoad(t.Context(), tokens.AccessToken); err != nil {
		t.Fatal("failed refresh discarded current access")
	}
	if err := s.DB.Exec("DROP TRIGGER native_refresh_storage_failure").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := m.NativeRefresh(t.Context(), tokens.RefreshToken); err != nil {
		t.Fatal("failed refresh consumed the credential")
	}
}
