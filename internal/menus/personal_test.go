package menus

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/domain"
	"github.com/LowGok/my-chef/internal/providers"
	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

type countedPhotoNormalizer struct {
	calls atomic.Int32
	fail  bool
}

func (n *countedPhotoNormalizer) Normalize(_ context.Context, input []byte) (providers.NormalizedPhoto, error) {
	n.calls.Add(1)
	if n.fail {
		return providers.NormalizedPhoto{}, providers.ErrPersonalPhotoInvalid
	}
	return providers.NormalizePersonalPhoto(input)
}

type countedPrivatePhotos struct {
	calls   atomic.Int32
	backend *providers.FixturePersonalPhotos
	after   func()
	lost    bool
}

func (p *countedPrivatePhotos) AssetBinding() string { return p.backend.AssetBinding() }
func (p *countedPrivatePhotos) UploadPersonalPhoto(ctx context.Context, photo providers.NormalizedPhoto) (providers.PrivatePhotoObject, error) {
	p.calls.Add(1)
	object, err := p.backend.UploadPersonalPhoto(ctx, photo)
	if p.after != nil {
		p.after()
	}
	if p.lost {
		return providers.PrivatePhotoObject{}, providers.Failure("provider_unavailable")
	}
	return object, err
}

func personalSaveFixture(t *testing.T) (*store.Store, *store.LifecycleJournal, store.PersonalActor, store.PersonalEntitlementReader, store.PersonalSave, []byte) {
	t.Helper()
	s, id := workerStore(t)
	now := time.Now()
	if err := s.DB.Model(&store.User{}).Where("user_id=?", id).Update("email_verified_at", now.Unix()).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Create(&store.Session{Hash: "photo-service-session", UserID: &id, ExpiresUnix: now.Add(time.Hour).Unix()}).Error; err != nil {
		t.Fatal(err)
	}
	j, err := s.InitializeLifecycleJournal(t.Context(), s.Path+".lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	reader := func(context.Context, *gorm.DB, int64, string) (store.PersonalEntitlement, error) {
		return store.PersonalEntitlement{Premium: true, ExpiresUnix: now.Add(time.Hour).Unix()}, nil
	}
	input := store.PersonalSave{RequestKey: strings.Repeat("a", 32), Content: domain.PersonalRecipeContent{SchemaVersion: 1, Title: "Private saved dish", Servings: 2, PrepMinutes: 5, CookMinutes: 10, TotalMinutes: 20, Ingredients: []domain.IngredientContent{{Name: "Carrot"}}, Instructions: []string{"Cook gently."}}, PhotoAction: "replace"}
	var raw bytes.Buffer
	if err := png.Encode(&raw, image.NewNRGBA(image.Rect(0, 0, 20, 20))); err != nil {
		t.Fatal(err)
	}
	return s, j, store.PersonalActor{UserID: id, SessionHash: "photo-service-session"}, reader, input, raw.Bytes()
}

func TestPersonalSaveServiceNormalizesOnceAndReplaysCommittedResult(t *testing.T) {
	s, j, actor, reader, input, raw := personalSaveFixture(t)
	n := &countedPhotoNormalizer{}
	p := &countedPrivatePhotos{backend: providers.NewFixturePersonalPhotos()}
	service := NewPersonalRecipes(s, j, reader, n, p)
	first, err := service.Save(t.Context(), actor, input, raw, "192.0.2.1")
	if err != nil || first.PhotoID == nil {
		t.Fatal("service save failed", err)
	}
	for range 3 {
		replay, err := service.Save(t.Context(), actor, input, raw, "192.0.2.1")
		if err != nil || replay.VersionID != first.VersionID {
			t.Fatal("response-loss retry failed", err)
		}
	}
	if n.calls.Load() != 1 || p.calls.Load() != 1 {
		t.Fatal("retry repeated decoding or upload")
	}
	asset, err := s.ReadPersonalPhoto(t.Context(), actor, *first.PhotoID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := p.backend.ReadPersonalPhoto(t.Context(), asset.Target, asset.Digest)
	if err != nil || bytes.Equal(encoded, raw) {
		t.Fatal("normalized bytes not stored", err)
	}
	input.Content.Title = "Different draft"
	if _, err := service.Save(t.Context(), actor, input, raw, "192.0.2.1"); !errors.Is(err, store.ErrPersonalKeyConflict) {
		t.Fatal("changed retry reused old result", err)
	}
}

func TestPersonalSaveServiceRejectsBeforeProviderAndPreservesSavedState(t *testing.T) {
	for _, mode := range []string{"invalid_text", "invalid_photo", "unavailable", "lost_response", "cancelled", "revoked"} {
		t.Run(mode, func(t *testing.T) {
			s, j, actor, reader, input, raw := personalSaveFixture(t)
			n := &countedPhotoNormalizer{fail: mode == "invalid_photo"}
			p := &countedPrivatePhotos{backend: providers.NewFixturePersonalPhotos(), lost: mode == "lost_response"}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "cancelled" {
				p.after = cancel
			}
			if mode == "revoked" {
				p.after = func() {
					if err := s.DB.Where("hash=?", actor.SessionHash).Delete(&store.Session{}).Error; err != nil {
						t.Error(err)
					}
				}
			}
			if mode == "invalid_text" {
				input.Content.Instructions = nil
			}
			var provider PersonalPhotoUploader = p
			if mode == "unavailable" {
				provider = nil
			}
			service := NewPersonalRecipes(s, j, reader, n, provider)
			if _, err := service.Save(ctx, actor, input, raw, "192.0.2.2"); err == nil {
				t.Fatal("failed photo saved recipe")
			}
			var recipes int
			if err := s.SQL.QueryRow("SELECT COUNT(*) FROM personal_recipes").Scan(&recipes); err != nil || recipes != 0 {
				t.Fatal("partial saved recipe", err)
			}
			if mode == "invalid_text" && (n.calls.Load() != 0 || p.calls.Load() != 0) {
				t.Fatal("invalid text reached decoder/provider")
			}
			if mode == "invalid_photo" && p.calls.Load() != 0 {
				t.Fatal("invalid photo reached provider")
			}
			if mode == "lost_response" {
				if _, err := service.Save(ctx, actor, input, raw, "192.0.2.2"); err == nil || p.calls.Load() != 1 {
					t.Fatal("uncertain upload silently retried")
				}
				var incarnation string
				if err := s.SQL.QueryRow("SELECT incarnation FROM account_incarnations WHERE user_id=?", actor.UserID).Scan(&incarnation); err != nil {
					t.Fatal(err)
				}
				work, err := j.WorkFor(t.Context(), incarnation)
				if err != nil || len(work) != 1 || work[0].StartedNS == 0 || work[0].Confirmed {
					t.Fatal("lost response became untracked", err)
				}
			}
		})
	}
}
