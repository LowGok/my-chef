package menus

import (
	"context"
	"strconv"
	"testing"

	"github.com/LowGok/my-chef/internal/providers"
	"github.com/LowGok/my-chef/internal/store"
)

func TestFavoritePlanningUsesOnlyRemainingProviderSlots(t *testing.T) {
	for _, all := range []bool{false, true} {
		t.Run(strconv.FormatBool(all), func(t *testing.T) {
			db, user := workerStore(t)
			ctx := context.Background()
			m := store.Meal{CreatorID: user, CreationDate: "2026-10-01", RecipeTitle: "Saved favorite", ImageStatus: "unavailable", Instructions: store.Ptr(`["Cook carefully"]`)}
			if err := db.DB.Create(&m).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := db.SetFavorite(ctx, user, m.ID, true); err != nil {
				t.Fatal(err)
			}
			_, _ = db.QueueFavorite(ctx, user, m.ID, "2026-12-28")
			week, _ := db.FavoriteWeek(ctx, user, "2026-12-28")
			in := request("Belgium")
			if !all {
				in.Dates = append(in.Dates, "2027-01-01")
			}
			in.FavoriteRevisions = map[string]string{"2026-12-28": week.Revision}
			p := &counted{}
			svc, err := New(db, p, providers.Fixture{}, Options{Shuffle: func(d []string) {
				for i, j := 0, len(d)-1; i < j; i, j = i+1, j-1 {
					d[i], d[j] = d[j], d[i]
				}
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()
			op, err := svc.Start(ctx, user, "favorite-plan", in)
			if err != nil {
				t.Fatal(err)
			}
			id, _ := strconv.ParseInt(op.ID, 10, 64)
			done := waitOperation(t, svc, user, id)
			if done.Status != "succeeded" {
				t.Fatal(done)
			}
			menuID, _ := strconv.ParseInt(*done.MenuID, 10, 64)
			menu, err := db.GetMenu(ctx, user, menuID)
			if err != nil || len(menu.Recipes) != len(in.Dates) {
				t.Fatal(menu, err)
			}
			found := false
			for _, r := range menu.Recipes {
				if r.Title == "Saved favorite" {
					found = true
					if r.Date != in.Dates[len(in.Dates)-1] {
						t.Fatal("shuffle assignment not used")
					}
				}
			}
			if !found {
				t.Fatal("favorite lost")
			}
			expected := int32(1)
			if all {
				expected = 0
			}
			if p.calls.Load() != expected {
				t.Fatal("provider calls", p.calls.Load())
			}
			again, err := svc.Start(ctx, user, "favorite-plan", in)
			if err != nil || again.ID != op.ID {
				t.Fatal("idempotency", err)
			}
			remaining, _ := db.FavoriteWeek(ctx, user, "2026-12-28")
			if len(remaining.Recipes) != 0 {
				t.Fatal("queue not consumed")
			}
		})
	}
}
func TestFavoritePlanningRejectsUnreviewedQueue(t *testing.T) {
	db, user := workerStore(t)
	m := store.Meal{CreatorID: user, CreationDate: "2026-10-01", RecipeTitle: "Saved", ImageStatus: "unavailable"}
	_ = db.DB.Create(&m).Error
	_, _ = db.SetFavorite(context.Background(), user, m.ID, true)
	_, _ = db.QueueFavorite(context.Background(), user, m.ID, "2026-12-28")
	p := &counted{}
	s, err := New(db, p, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.Start(context.Background(), user, "missing-review", request("Belgium")); err == nil {
		t.Fatal("unreviewed queue accepted")
	}
	if p.calls.Load() != 0 {
		t.Fatal("provider called")
	}
}

func queueSaved(t *testing.T, db *store.Store, user int64, title string) int64 {
	t.Helper()
	ctx := context.Background()
	m := store.Meal{CreatorID: user, CreationDate: "2026-10-01", RecipeTitle: title, ImageStatus: "unavailable"}
	if err := db.DB.Create(&m).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := db.SetFavorite(ctx, user, m.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := db.QueueFavorite(ctx, user, m.ID, "2026-12-28"); err != nil {
		t.Fatal(err)
	}
	return m.ID
}
func TestFavoritePlanningFailureAndConcurrentAddRetainPending(t *testing.T) {
	for _, region := range []string{"__quota", "__slow"} {
		t.Run(region, func(t *testing.T) {
			db, user := workerStore(t)
			ctx := context.Background()
			queueSaved(t, db, user, "First saved")
			week, _ := db.FavoriteWeek(ctx, user, "2026-12-28")
			in := request(region)
			in.Dates = append(in.Dates, "2027-01-01")
			in.FavoriteRevisions = map[string]string{"2026-12-28": week.Revision}
			p := &counted{}
			svc, err := New(db, p, nil, Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer svc.Close()
			op, err := svc.Start(ctx, user, "pending-retention", in)
			if err != nil {
				t.Fatal(err)
			}
			second := int64(0)
			if region == "__slow" {
				second = queueSaved(t, db, user, "Later saved")
			}
			id, _ := strconv.ParseInt(op.ID, 10, 64)
			done := waitOperation(t, svc, user, id)
			pending, err := db.FavoriteWeek(ctx, user, "2026-12-28")
			if err != nil || len(pending.Recipes) != 1 {
				t.Fatal("lost pending", pending, err)
			}
			if region == "__quota" {
				if done.Status != "failed" || pending.Recipes[0].Title != "First saved" {
					t.Fatal(done, pending)
				}
			} else {
				if done.Status != "succeeded" || pending.Recipes[0].ID != strconv.FormatInt(second, 10) {
					t.Fatal(done, pending)
				}
			}
		})
	}
}
func TestFavoritePlanningInsufficientDatesAndChangedReview(t *testing.T) {
	db, user := workerStore(t)
	ctx := context.Background()
	queueSaved(t, db, user, "First")
	week, _ := db.FavoriteWeek(ctx, user, "2026-12-28")
	queueSaved(t, db, user, "Second")
	p := &counted{}
	svc, err := New(db, p, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	in := request("Belgium")
	in.FavoriteRevisions = map[string]string{"2026-12-28": week.Revision}
	if _, err = svc.Start(ctx, user, "stale-queue-review", in); err == nil {
		t.Fatal("stale review accepted")
	}
	week, _ = db.FavoriteWeek(ctx, user, "2026-12-28")
	in.FavoriteRevisions[week.Week] = week.Revision
	if _, err = svc.Start(ctx, user, "too-few-dates", in); err == nil {
		t.Fatal("discarded excess favorites")
	}
	if p.calls.Load() != 0 {
		t.Fatal("unreviewed plan reached provider")
	}
}

func TestFavoriteInterruptedPlanningKeepsSnapshotAndQueueOnRestart(t *testing.T) {
	db, user := workerStore(t)
	ctx := context.Background()
	queueSaved(t, db, user, "Keep after restart")
	pending, _ := db.FavoriteWeek(ctx, user, "2026-12-28")
	in := request("__slow")
	in.Dates = append(in.Dates, "2027-01-01")
	in.FavoriteRevisions = map[string]string{pending.Week: pending.Revision}
	p := &counted{}
	svc, err := New(db, p, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	op, err := svc.Start(ctx, user, "interrupted-favorite", in)
	if err != nil {
		svc.Close()
		t.Fatal(err)
	}
	id, _ := strconv.ParseInt(op.ID, 10, 64)
	var before store.Generation
	if err = db.DB.First(&before, id).Error; err != nil {
		svc.Close()
		t.Fatal(err)
	}
	svc.Close()
	restarted, err := New(db, p, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	again, err := restarted.Start(ctx, user, "interrupted-favorite", in)
	if err != nil || again.ID != op.ID || again.Status != "failed" {
		t.Fatal(again, err)
	}
	var after store.Generation
	if err = db.DB.First(&after, id).Error; err != nil || after.RequestJSON != before.RequestJSON {
		t.Fatal("captured shuffle changed", err)
	}
	pending, err = db.FavoriteWeek(ctx, user, pending.Week)
	if err != nil || len(pending.Recipes) != 1 {
		t.Fatal("interrupted planning consumed queue", pending, err)
	}
}
