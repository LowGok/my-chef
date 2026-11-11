package menus

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/domain"
	"github.com/LowGok/my-chef/internal/providers"
	"github.com/LowGok/my-chef/internal/store"
)

type counted struct {
	calls   atomic.Int32
	fixture providers.Fixture
}

func (p *counted) Generate(ctx context.Context, r domain.GenerationRequest) ([]domain.RecipeContent, error) {
	p.calls.Add(1)
	return p.fixture.Generate(ctx, r)
}
func workerStore(t *testing.T) (*store.Store, int64) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "worker.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err = s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	u := store.User{Email: "worker@test.invalid", Username: "worker", FirstName: "A", LastName: "B", Password: "hash"}
	if err = s.DB.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	return s, u.ID
}
func waitOperation(t *testing.T, s *Service, user, id int64) domain.Operation {
	t.Helper()
	until := time.Now().Add(5 * time.Second)
	for time.Now().Before(until) {
		op, err := s.Status(context.Background(), user, id)
		if err != nil {
			t.Fatal(err)
		}
		if op.Status == "succeeded" || op.Status == "failed" {
			return op
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("operation did not finish")
	return domain.Operation{}
}
func request(region string) domain.MenuInput {
	return domain.MenuInput{Dates: []string{"2026-12-31"}, Adults: 2, MaxMinutes: 30, Budget: "low", Region: region, Creativity: 5}
}

func TestConcurrentIdempotencyDoesNotRepeatProviderCalls(t *testing.T) {
	db, user := workerStore(t)
	p := &counted{}
	s, err := New(db, p, providers.Fixture{}, Options{Workers: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var wg sync.WaitGroup
	ids := make(chan string, 12)
	for range 12 {
		wg.Go(func() {
			op, e := s.Start(context.Background(), user, "same-request-key", request("__slow"))
			if e != nil {
				t.Error(e)
				return
			}
			ids <- op.ID
		})
	}
	wg.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatalf("duplicate operations: %s %s", id, first)
		}
	}
	var row store.Generation
	if err = db.DB.Where("user_id = ?", user).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	op := waitOperation(t, s, user, row.ID)
	if op.Status != "succeeded" || p.calls.Load() != 1 {
		t.Fatalf("status %s provider calls %d", op.Status, p.calls.Load())
	}
	other := request("Belgium")
	if _, err = s.Start(context.Background(), user, "same-request-key", other); err == nil {
		t.Fatal("different payload reused a key")
	}
	if _, err = s.Status(context.Background(), user+1, row.ID); err == nil {
		t.Fatal("another user read operation status")
	}
}
func TestQuotaInvalidResponseAndRestartNeverRetryAutomatically(t *testing.T) {
	for _, region := range []string{"__quota", "__invalid"} {
		t.Run(region, func(t *testing.T) {
			db, user := workerStore(t)
			p := &counted{}
			s, err := New(db, p, providers.Fixture{}, Options{})
			if err != nil {
				t.Fatal(err)
			}
			op, err := s.Start(context.Background(), user, "failed-request", request(region))
			if err != nil {
				t.Fatal(err)
			}
			var row store.Generation
			if err = db.DB.First(&row, op.ID).Error; err != nil {
				t.Fatal(err)
			}
			done := waitOperation(t, s, user, row.ID)
			s.Close()
			if done.Status != "failed" || p.calls.Load() != 1 {
				t.Fatalf("unexpected outcome: %+v %d", done, p.calls.Load())
			}
			var count int64
			if err = db.DB.Model(&store.Meal{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatal("failed provider request saved meals")
			}
		})
	}
	db, user := workerStore(t)
	job := store.Generation{UserID: user, Key: "interrupted-key", RequestHash: "hash", RequestJSON: "{}", Status: "running", Stage: "generating"}
	if err := db.DB.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	p := &counted{}
	s, err := New(db, p, providers.Fixture{}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	op, err := s.Status(context.Background(), user, job.ID)
	if err != nil || op.Status != "failed" || op.ErrorCode == nil || *op.ErrorCode != "interrupted" || p.calls.Load() != 0 {
		t.Fatalf("restart replayed an operation: %+v %v", op, err)
	}
}
