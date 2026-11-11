package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
)

func feedbackFixture(t *testing.T) (*Store, FeedbackActor, FeedbackActor, time.Time) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "feedback.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1800000000, 0)
	var actors [2]FeedbackActor
	for i := range actors {
		u := User{FirstName: "Test", LastName: "Cook", Email: fmt.Sprintf("cook%d@example.test", i), Username: fmt.Sprintf("cook%d", i), Password: "fixture"}
		if err := s.DB.Create(&u).Error; err != nil {
			t.Fatal(err)
		}
		if err := s.DB.Model(&u).Update("email_verified_at", now.Unix()).Error; err != nil {
			t.Fatal(err)
		}
		actors[i] = FeedbackActor{UserID: u.ID, SessionHash: fmt.Sprintf("session-%d", i)}
		if err := s.DB.Create(&Session{Hash: actors[i].SessionHash, UserID: &u.ID, ExpiresUnix: now.Add(24 * time.Hour).Unix()}).Error; err != nil {
			t.Fatal(err)
		}
	}
	return s, actors[0], actors[1], now
}

func feedbackRequest(n int) FeedbackInput {
	return FeedbackInput{RequestKey: fmt.Sprintf("%032x", n), Topic: "technical_issue", Message: "Please make the app easier to use."}
}

func feedbackFakeEnqueue(tx *gorm.DB, note FeedbackNotice) (int64, error) {
	var id int64
	err := tx.Raw(`INSERT INTO email_outbox(key,recipient,template,data,state,outcome,reason,attempts,next_at,fingerprint,created_at,updated_at)
VALUES(?, 'operator@example.test', 'fixture', '{}', 'queued', '', '', 0, ?, '', ?, ?) RETURNING id`,
		"feedback/"+note.Reference, note.CreatedUnix, note.CreatedUnix, note.CreatedUnix).Row().Scan(&id)
	return id, err
}

func feedbackCount(t *testing.T, s *Store, table string, want int64) {
	t.Helper()
	var n int64
	if err := s.DB.Table(table).Count(&n).Error; err != nil || n != want {
		t.Fatalf("%s: got %d, want %d (%v)", table, n, want, err)
	}
}

func TestFeedbackStoreRollbackReplayAndVerifiedOwner(t *testing.T) {
	s, actor, stranger, now := feedbackFixture(t)
	ctx := t.Context()
	input := feedbackRequest(1)
	_, err := s.WriteFeedback(ctx, actor, input, now, nil, func(tx *gorm.DB, note FeedbackNotice) (int64, error) {
		if _, err := feedbackFakeEnqueue(tx, note); err != nil {
			return 0, err
		}
		return 0, errors.New("fixture failure")
	})
	if !errors.Is(err, ErrFeedbackStorage) {
		t.Fatal("expected safe storage error")
	}
	feedbackCount(t, s, "feedback_reports", 0)
	feedbackCount(t, s, "email_outbox", 0)
	var receipts [2]FeedbackReceipt
	var outcomes [2]error
	var group sync.WaitGroup
	for i := range receipts {
		group.Go(func() { receipts[i], outcomes[i] = s.WriteFeedback(ctx, actor, input, now, nil, feedbackFakeEnqueue) })
	}
	group.Wait()
	if outcomes[0] != nil || outcomes[1] != nil || receipts[0] != receipts[1] || receipts[0].Reference == "" {
		t.Fatal("concurrent same-key calls did not return the same receipt")
	}
	feedbackCount(t, s, "feedback_reports", 1)
	feedbackCount(t, s, "email_outbox", 1)
	if _, err := s.FeedbackReceipt(ctx, stranger, "feedback", input.RequestKey, now); !errors.Is(err, ErrFeedbackNotFound) {
		t.Fatal("another user read a receipt")
	}
	input.Message += " Different content."
	if _, err := s.WriteFeedback(ctx, actor, input, now, nil, feedbackFakeEnqueue); !errors.Is(err, ErrFeedbackConflict) {
		t.Fatal("changed content did not conflict")
	}
	if err := s.DB.Where("hash = ?", actor.SessionHash).Delete(&Session{}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.WriteFeedback(ctx, actor, feedbackRequest(3), now, nil, feedbackFakeEnqueue); !errors.Is(err, ErrFeedbackSession) {
		t.Fatal("revoked session wrote feedback")
	}
	feedbackCount(t, s, "feedback_reports", 1)
}

func TestFeedbackStoreRollingQuotaAndNoMailboxReplay(t *testing.T) {
	s, actor, _, now := feedbackFixture(t)
	ctx := t.Context()
	priority := func(_ context.Context, _ *gorm.DB, _ int64) (FeedbackEntitlement, error) {
		return FeedbackEntitlement{Premium: true, ExpiresUnix: now.Add(time.Hour).Unix()}, nil
	}
	for i := range 5 {
		if _, err := s.WriteFeedback(ctx, actor, feedbackRequest(i+1), now, priority, func(tx *gorm.DB, notice FeedbackNotice) (int64, error) {
			if notice.Priority != "priority" || notice.ContactEmail != "cook0@example.test" {
				t.Fatal("notice did not derive priority and address on the server")
			}
			return feedbackFakeEnqueue(tx, notice)
		}); err != nil {
			t.Fatal(err)
		}
	}
	_, err := s.WriteFeedback(ctx, actor, feedbackRequest(6), now.Add(time.Hour-time.Second), nil, feedbackFakeEnqueue)
	var limit *FeedbackLimit
	if !errors.As(err, &limit) || limit.RetryAfter != 1 {
		t.Fatalf("wrong rolling boundary: %v", err)
	}
	if _, err := s.WriteFeedback(ctx, actor, feedbackRequest(1), now.Add(time.Minute), nil, nil); err != nil {
		t.Fatal("an exact replay failed while quota was full and mailbox unavailable")
	}
	if _, err := s.WriteFeedback(ctx, actor, feedbackRequest(6), now.Add(time.Hour), priority, func(tx *gorm.DB, notice FeedbackNotice) (int64, error) {
		if notice.Priority != "standard" {
			t.Fatal("expired entitlement retained priority")
		}
		return feedbackFakeEnqueue(tx, notice)
	}); err != nil {
		t.Fatal(err)
	}
	feedbackCount(t, s, "feedback_reports", 6)
}

func TestFeedbackRatingSurvivesFavoriteFallbackAndReplay(t *testing.T) {
	s, owner, stranger, now := feedbackFixture(t)
	ctx := t.Context()
	if err := s.DB.Exec("INSERT INTO meals(meal_id,creator_id,creationdate,recipetitle) VALUES(40,?,'2026-11-06','Recipe')", owner.UserID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetFavorite(ctx, owner.UserID, 40, true); err != nil {
		t.Fatal(err)
	}
	input := FeedbackRatingInput{RequestKey: feedbackRequest(1).RequestKey, RecipeID: 40, Rating: 2}
	if _, err := s.WriteFeedbackRating(ctx, stranger, input, now); !errors.Is(err, ErrFeedbackNotFound) {
		t.Fatal("another account rated an inaccessible recipe")
	}
	first, err := s.WriteFeedbackRating(ctx, owner, input, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Exec("DELETE FROM meals WHERE meal_id=40").Error; err != nil {
		t.Fatal(err)
	}
	if current, err := s.FeedbackRating(ctx, owner, 40, now); err != nil || current == nil || *current != 2 {
		t.Fatal("favorite fallback lost the original rating")
	}
	second := FeedbackRatingInput{RequestKey: feedbackRequest(2).RequestKey, RecipeID: 40, Rating: 5}
	if _, err := s.WriteFeedbackRating(ctx, owner, second, now); err != nil {
		t.Fatal(err)
	}
	if replay, err := s.WriteFeedbackRating(ctx, owner, input, now); err != nil || replay != first {
		t.Fatal("rating replay lost its original receipt")
	}
	if current, err := s.FeedbackRating(ctx, owner, 40, now); err != nil || current == nil || *current != 5 {
		t.Fatal("replaying an older score overwrote the current rating")
	}
	second.RequestKey = feedbackRequest(3).RequestKey
	if _, err := s.WriteFeedbackRating(ctx, owner, second, now); !errors.Is(err, ErrFeedbackUnchanged) {
		t.Fatal("same score under a new key was not recognized as unchanged")
	}
	if _, err := s.SetFavorite(ctx, owner.UserID, 40, false); err != nil {
		t.Fatal(err)
	}
	if replay, err := s.WriteFeedbackRating(ctx, owner, input, now); err != nil || replay != first {
		t.Fatal("lost recipe access vetoed replay of an already accepted receipt")
	}
	feedbackCount(t, s, "feedback_rating_receipts", 2)
	feedbackCount(t, s, "email_outbox", 0)
}

func TestFeedbackRatingQuotaSurvivesRestartAndIsSeparate(t *testing.T) {
	s, actor, _, now := feedbackFixture(t)
	ctx := t.Context()
	if err := s.DB.Exec("INSERT INTO meals(meal_id,creator_id,creationdate,recipetitle) VALUES(41,?,'2026-11-06','Recipe')", actor.UserID).Error; err != nil {
		t.Fatal(err)
	}
	var first FeedbackReceipt
	for i := range 30 {
		receipt, err := s.WriteFeedbackRating(ctx, actor, FeedbackRatingInput{RequestKey: feedbackRequest(i + 1).RequestKey, RecipeID: 41, Rating: i%2 + 1}, now)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = receipt
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(s.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if got, err := reopened.FeedbackReceipt(ctx, actor, "rating", feedbackRequest(1).RequestKey, now); err != nil || got != first {
		t.Fatal("rating receipt did not survive restart")
	}
	input := FeedbackRatingInput{RequestKey: feedbackRequest(31).RequestKey, RecipeID: 41, Rating: 5}
	var limit *FeedbackLimit
	if _, err := reopened.WriteFeedbackRating(ctx, actor, input, now.Add(time.Hour-time.Second)); !errors.As(err, &limit) || limit.RetryAfter != 1 {
		t.Fatalf("restart did not preserve rating quota: %v", err)
	}
	if _, err := reopened.WriteFeedback(ctx, actor, feedbackRequest(1), now, nil, feedbackFakeEnqueue); err != nil {
		t.Fatal("rating quota affected written feedback")
	}
	if _, err := reopened.WriteFeedbackRating(ctx, actor, input, now.Add(time.Hour)); err != nil {
		t.Fatal("rating quota did not reset at the exact boundary")
	}
}

func TestFeedbackRecordsRetainedWithoutChangingPlans(t *testing.T) {
	s, actor, _, now := feedbackFixture(t)
	ctx := t.Context()
	if err := s.DB.Exec("INSERT INTO meals(meal_id,creator_id,creationdate,recipetitle) VALUES(51,?,'2026-11-06','Original recipe')", actor.UserID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetFavorite(ctx, actor.UserID, 51, true); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Exec("DELETE FROM meals WHERE meal_id=51").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Exec("INSERT INTO meals(meal_id,creator_id,creationdate,recipetitle) VALUES(52,?,'2026-11-06','Replacement recipe')", actor.UserID).Error; err != nil {
		t.Fatal(err)
	}
	snapshot := func() string {
		t.Helper()
		var state string
		if err := s.SQL.QueryRow(`SELECT json_array(
(SELECT json_group_array(json_array(id,user_id,created_unix)) FROM menus),
(SELECT json_group_array(json_array(id,user_id,request_key,status,menu_id)) FROM generations),
(SELECT json_group_array(json_array(meal_id,creator_id,menu_id,recipetitle)) FROM meals),
(SELECT count(*) FROM favorites), (SELECT count(*) FROM favorite_queue), (SELECT count(*) FROM favorite_swaps))`).Scan(&state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	before := snapshot()
	report := feedbackRequest(1)
	report.Topic, report.RecipeID = "recipe_issue", Ptr(int64(51))
	free := func(context.Context, *gorm.DB, int64) (FeedbackEntitlement, error) {
		return FeedbackEntitlement{ExpiresUnix: now.Add(time.Hour).Unix()}, nil
	}
	written, err := s.WriteFeedback(ctx, actor, report, now, free, func(tx *gorm.DB, notice FeedbackNotice) (int64, error) {
		if notice.Priority != "standard" || notice.RecipeID == nil || *notice.RecipeID != 51 {
			t.Fatal("favorite report did not keep Standard support and original recipe identity")
		}
		return feedbackFakeEnqueue(tx, notice)
	})
	if err != nil {
		t.Fatal(err)
	}
	rating := FeedbackRatingInput{RequestKey: report.RequestKey, RecipeID: 51, Rating: 4}
	rated, err := s.WriteFeedbackRating(ctx, actor, rating, now)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.FeedbackRating(ctx, actor, 52, now); err != nil || got != nil {
		t.Fatal("replacement reused the old recipe rating")
	}
	if before != snapshot() {
		t.Fatal("feedback or rating changed planning data")
	}
	later := now.Add(90 * 24 * time.Hour)
	if err := s.DB.Model(&Session{}).Where("hash=?", actor.SessionHash).Update("expires_unix", later.Add(time.Hour).Unix()).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(s.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	for operation, want := range map[string]FeedbackReceipt{"feedback": written, "rating": rated} {
		if got, err := reopened.FeedbackReceipt(ctx, actor, operation, report.RequestKey, later); err != nil || got != want {
			t.Fatal("receipt did not survive elapsed time and restart")
		}
	}
	if got, err := reopened.FeedbackRating(ctx, actor, 51, later); err != nil || got == nil || *got != 4 {
		t.Fatal("current rating did not survive elapsed time and restart")
	}
	var message string
	if err := reopened.SQL.QueryRow("SELECT message FROM feedback_reports WHERE reference=?", written.Reference).Scan(&message); err != nil || message != report.Message {
		t.Fatal("original report did not survive elapsed time and restart")
	}
}
