package email

import (
	"context"
	"time"

	"github.com/LowGok/my-chef/internal/email/mailqueue"
	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

var ErrKeyConflict = mailqueue.ErrKeyConflict
var errStorage = mailqueue.ErrStorage

type Request = mailqueue.Request
type outbox = mailqueue.Record

type Queue struct {
	Store                   *store.Store
	Now                     func() time.Time
	ProofKeys               *mailqueue.ProofKeys
	AllowLoopbackProofLinks bool
	RecoveryWork            *store.RecoveryWork
}

func (q Queue) now() time.Time {
	if q.Now != nil {
		return q.Now().UTC()
	}
	return time.Now().UTC()
}

func (q Queue) background(ctx context.Context, operation func(*gorm.DB) error) error {
	return q.RecoveryWork.Background(ctx, func() error {
		return q.Store.DB.WithContext(ctx).Transaction(operation)
	})
}

// Enqueue joins tx when provided. The returned ID is durable after commit.
func (q Queue) Enqueue(ctx context.Context, tx *gorm.DB, req Request) (int64, error) {
	return mailqueue.Enqueue(ctx, q.Store, tx, q.now(), req)
}
