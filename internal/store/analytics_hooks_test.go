package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/analytics"
	"github.com/LowGok/my-chef/internal/domain"
	"gorm.io/gorm"
)

func analyticsHookFixture(t *testing.T, s *Store, user int64, now time.Time) context.Context {
	t.Helper()
	root := t.TempDir()
	_ = os.Chmod(root, 0700)
	if e := s.DB.Model(&User{}).Where("user_id=?", user).Update("email_verified_at", now.Unix()).Error; e != nil {
		t.Fatal(e)
	}
	c := analytics.Config{Enabled: true, NoticeVersion: "fixture", NoticeHash: analytics.Digest([]byte(analytics.NoticeText)), Assessment: analytics.Assessment{Status: "approved", Basis: "legitimate_interest", Version: "fixture", Scope: analytics.ScopeDigest(), NoticeVersion: "fixture", Jurisdiction: "fictional", Audience: "fictional", DeviceAccess: "fictional", ValidUntil: now.AddDate(1, 0, 0).Unix()}}
	a, e := analytics.Open(t.Context(), s.DB, analytics.OpenOptions{Root: filepath.Join(root, "data"), PrivacyPath: filepath.Join(root, "privacy.sqlite"), AnchorPath: filepath.Join(root, "anchor"), Create: true, BackupKey: make([]byte, 32), Config: c, Now: func() time.Time { return now }})
	if e != nil {
		t.Fatal(e)
	}
	s.Analytics = a
	t.Cleanup(func() { _ = a.Close() })
	p, e := a.Update(t.Context(), user, analytics.PreferenceUpdate{Enabled: true, NoticeVersion: "fixture"}, "web")
	if e != nil {
		t.Fatal(e)
	}
	return a.Request(t.Context(), user, p.Revision, "web")
}
func eventCount(t *testing.T, s *Store, name string, want int) {
	t.Helper()
	var n int
	e := s.SQL.QueryRow("SELECT count(*) FROM analytics_outbox WHERE name=?", name).Scan(&n)
	if e != nil || n != want {
		t.Fatalf("%s = %d want %d (%v)", name, n, want, e)
	}
}
func TestAnalyticsFavoriteAndShoppingHooks(t *testing.T) {
	s := shoppingStore(t)
	ctx := analyticsHookFixture(t, s, 1, time.Now())
	m := shoppingMeal(t, s, 1, "2026-11-02", "canary-title", "20")
	for range 2 {
		if _, e := s.SetFavorite(ctx, 1, m.ID, true); e != nil {
			t.Fatal(e)
		}
	}
	eventCount(t, s, "favorite_saved", 1)
	list, e := s.Shopping(ctx, 1, "2026-11-02")
	if e != nil {
		t.Fatal(e)
	}
	eventCount(t, s, "shopping_list_edited", 0)
	list, e = s.SaveShopping(ctx, 1, list.Week, list)
	if e != nil {
		t.Fatal(e)
	}
	eventCount(t, s, "shopping_list_edited", 0)
	list.Items = append(list.Items, domain.ShoppingItem{ID: "manual", Name: "canary-extra", State: "buy", Manual: true})
	if _, e = s.SaveShopping(ctx, 1, list.Week, list); e != nil {
		t.Fatal(e)
	}
	eventCount(t, s, "shopping_list_edited", 1)
}
func TestAnalyticsFeedbackHook(t *testing.T) {
	s, a, _, now := feedbackFixture(t)
	ctx := analyticsHookFixture(t, s, a.UserID, now)
	in := feedbackRequest(1)
	for range 2 {
		if _, e := s.WriteFeedback(ctx, a, in, now, nil, feedbackFakeEnqueue); e != nil {
			t.Fatal(e)
		}
	}
	eventCount(t, s, "feedback_submitted", 1)
}
func TestAnalyticsPersonalAndImportedHooks(t *testing.T) {
	s, a, _, reader, now := personalFixture(t)
	ctx := analyticsHookFixture(t, s, a.UserID, now)
	in := PersonalSave{RequestKey: strings.Repeat("1", 32), Content: personalDocument("canary-recipe")}
	first, e := s.SavePersonalRecipe(ctx, a, reader, in, now)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.SavePersonalRecipe(ctx, a, reader, in, now); e != nil {
		t.Fatal(e)
	}
	eventCount(t, s, "personal_recipe_saved", 1)
	same := PersonalSave{ID: first.ID, ExpectedVersion: first.VersionID, RequestKey: strings.Repeat("2", 32), Content: first.Content}
	if _, e = s.SavePersonalRecipe(ctx, a, reader, same, now); e != nil {
		t.Fatal(e)
	}
	eventCount(t, s, "personal_recipe_saved", 1)
	_, imported := readyImportSave(t, s, a, reader, importVault(t), strings.Repeat("3", 32), now)
	for range 2 {
		if _, e = s.SavePersonalRecipe(ctx, a, reader, imported, now); e != nil {
			t.Fatal(e)
		}
	}
	eventCount(t, s, "personal_recipe_saved", 2)
	eventCount(t, s, "recipe_import_saved", 1)
}
func TestAnalyticsMenuHookAndOldAdmission(t *testing.T) {
	s := shoppingStore(t)
	ctx := analyticsHookFixture(t, s, 1, time.Now())
	input := domain.MenuInput{Dates: []string{"2026-11-02"}, Adults: 2, MaxMinutes: 30, Budget: "low", Region: "BE", Creativity: 3}
	request, _ := json.Marshal(domain.GenerationRequest{MenuInput: input})
	g := Generation{UserID: 1, Key: "analytics-menu", RequestHash: "hash", RequestJSON: string(request), Status: "running", Stage: "saving"}
	if e := s.DB.Create(&g).Error; e != nil {
		t.Fatal(e)
	}
	if e := s.DB.Transaction(func(tx *gorm.DB) error { return analytics.SaveGenerationCapture(ctx, tx, g.ID) }); e != nil {
		t.Fatal(e)
	}
	if _, e := s.SaveMenu(t.Context(), 1, g.ID, input, []domain.RecipeContent{shoppingRecipe("carrot", "20")}); e != nil {
		t.Fatal(e)
	}
	eventCount(t, s, "meal_plan_saved", 1)
}

func TestAnalyticsShoppingSourceAcknowledgementIsNotAnEdit(t *testing.T) {
	s := shoppingStore(t)
	ctx := analyticsHookFixture(t, s, 1, time.Now())
	meal := shoppingMeal(t, s, 1, "2026-11-02", "source meal", "20")
	if _, e := s.Shopping(ctx, 1, "2026-11-02"); e != nil {
		t.Fatal(e)
	}
	if e := s.DB.Model(&Ingredient{}).Where("recipe_id=?", meal.ID).Update("quantity", "30").Error; e != nil {
		t.Fatal(e)
	}
	list, e := s.Shopping(ctx, 1, "2026-11-02")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.SaveShopping(ctx, 1, list.Week, list); e != nil {
		t.Fatal(e)
	}
	eventCount(t, s, "shopping_list_edited", 0)
}
