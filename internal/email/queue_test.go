package email

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

func TestQueueDurability(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "queue.db")
	s, err := store.Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	q := Queue{Store: s, Now: time.Now}
	request := Request{Key: "smoke/1", Recipient: "delivered@resend.dev", Template: "provider-smoke-test/v1", Fields: map[string]string{"testID": "test", "timestamp": "2026-11-06T12:00:00Z"}}
	rollback := errors.New("rollback")
	err = s.DB.Transaction(func(tx *gorm.DB) error {
		if _, err := q.Enqueue(ctx, tx, request); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	var count int
	if err := s.SQL.QueryRow("SELECT count(*) FROM email_outbox").Scan(&count); err != nil || count != 0 {
		t.Fatalf("rollback: count=%d err=%v", count, err)
	}
	type result struct {
		id  int64
		err error
	}
	results := make(chan result, 8)
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() { id, err := q.Enqueue(ctx, nil, request); results <- result{id, err} })
	}
	group.Wait()
	close(results)
	var id int64
	for got := range results {
		if got.err != nil || got.id == 0 || (id != 0 && id != got.id) {
			t.Fatalf("duplicate enqueue: %+v", got)
		}
		id = got.id
	}
	request.Recipient = "bounced@resend.dev"
	if _, err := q.Enqueue(ctx, nil, request); !errors.Is(err, ErrKeyConflict) {
		t.Fatalf("conflicting enqueue: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	var recipient, version, data, state string
	err = reopened.SQL.QueryRow("SELECT recipient, template, data, state FROM email_outbox WHERE id = ?", id).Scan(&recipient, &version, &data, &state)
	if err != nil || recipient != "delivered@resend.dev" || version != request.Template || data != `{"testID":"test","timestamp":"2026-11-06T12:00:00Z"}` || state != "queued" {
		t.Fatalf("reopened queue record did not match: %v", err)
	}
}
