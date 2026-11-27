package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/LowGok/my-chef/internal/store"
)

func TestNativeIdentityCredentialsCannotCrossAnIngress(t *testing.T) {
	m := New(openPasswordTestStore(t))
	app := WithNativeIdentity(t.Context(), "app")
	legacy := t.Context()
	first, err := m.NativeStart(app, "ios")
	if err != nil {
		t.Fatal(err)
	}
	for _, attempt := range []struct {
		context context.Context
		token   string
	}{{legacy, first.AccessToken}, {WithNativeIdentity(legacy, "unknown"), first.AccessToken}} {
		if _, err := m.NativeLoad(attempt.context, attempt.token); !errors.Is(err, ErrSession) {
			t.Fatal("cross-ingress or unknown lane accepted access", err)
		}
	}
	if _, err := m.NativeRefresh(legacy, first.RefreshToken); !errors.Is(err, ErrSession) {
		t.Fatal("cross-ingress refresh accepted")
	}
	current, err := m.NativeLoad(app, first.AccessToken)
	if err != nil || current.IdentityLane != "app" {
		t.Fatal("wrong-ingress attempt damaged original access", err)
	}
	next, err := m.NativeRefresh(app, first.RefreshToken)
	if err != nil {
		t.Fatal("wrong-ingress attempt consumed original refresh", err)
	}
	if _, err := m.NativeLoad(app, next.AccessToken); err != nil {
		t.Fatal(err)
	}
	if err := m.NativeRevoke(legacy, current.FamilyID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.NativeLoad(app, next.AccessToken); err != nil {
		t.Fatal("wrong-ingress revoke touched the sibling lane", err)
	}
}

func TestNativeIdentitySurvivesSignInAndFamilyReplacement(t *testing.T) {
	s := openPasswordTestStore(t)
	m := New(s)
	user := usernamePolicyRegister(t, m, "identity-owner@example.test", "Identity owner")
	ctx := WithNativeIdentity(t.Context(), "app")
	start, err := m.NativeStart(ctx, "android")
	if err != nil {
		t.Fatal(err)
	}
	old, err := m.NativeLoad(ctx, start.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	_, anchor, err := m.LoginSession(ctx, user.Email, "synthetic pantry account password", start.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := m.NativeBind(ctx, anchor, "android", old.FamilyID)
	if err != nil {
		t.Fatal(err)
	}
	current, err := m.NativeLoad(ctx, signed.AccessToken)
	if err != nil || current.Session.UserID == nil || *current.Session.UserID != user.ID || current.IdentityLane != "app" {
		t.Fatal("sign-in lost account or installation identity", err)
	}
	refreshed, err := m.NativeRefresh(ctx, signed.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	current, err = m.NativeLoad(ctx, refreshed.AccessToken)
	if err != nil || current.IdentityLane != "app" {
		t.Fatal("refresh lost the identity", err)
	}
	if _, err = m.NativeLoad(t.Context(), refreshed.AccessToken); !errors.Is(err, ErrSession) {
		t.Fatal("replacement gained legacy authority", err)
	}

	legacy, err := m.NativeStart(t.Context(), "ios")
	if err != nil {
		t.Fatal(err)
	}
	l, err := m.NativeLoad(t.Context(), legacy.AccessToken)
	if err != nil || l.IdentityLane != "preview" {
		t.Fatal("existing caller stopped working", err)
	}
	var row store.NativeFamily
	if err := s.DB.First(&row, "id=?", l.FamilyID).Error; err != nil || row.IdentityLane != "preview" {
		t.Fatal("legacy identity was not persisted", err)
	}
}
