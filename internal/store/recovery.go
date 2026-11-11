package store

import (
	"context"
	"database/sql"
	"errors"
	"sync"

	"github.com/LowGok/my-chef/migrations"
	"gorm.io/gorm"
)

var ErrRecoveryUnavailable = errors.New("password recovery unavailable")

type RecoveryJob struct {
	AcceptedOrder int64 `gorm:"primaryKey;autoIncrement:false"`
	AddressTag    string
	ExpiresNS     int64 `gorm:"column:expires_ns"`
	Draft         string
}

func (RecoveryJob) TableName() string { return "recovery_jobs" }

type RecoveryProof struct {
	Hash             string `gorm:"primaryKey"`
	AcceptedOrder    int64
	UserID           int64
	AddressTag       string
	EligibilityOrder int64
	IssuedNS         int64  `gorm:"column:issued_ns"`
	ExpiresNS        int64  `gorm:"column:expires_ns"`
	ConsumedNS       *int64 `gorm:"column:consumed_ns"`
}

func (RecoveryProof) TableName() string { return "recovery_proofs" }

type RecoveryAccountOrder struct {
	UserID            int64 `gorm:"primaryKey;autoIncrement:false"`
	EligibilityOrder  int64
	LatestIssuedOrder int64
}

func (RecoveryAccountOrder) TableName() string { return "recovery_account_orders" }

// NextRecoveryOrder allocates an order in the caller's write transaction.
// An aborted transaction does not spend an order.
func NextRecoveryOrder(ctx context.Context, tx *gorm.DB) (int64, error) {
	if tx == nil || tx.Statement == nil {
		return 0, ErrRecoveryUnavailable
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return 0, ErrRecoveryUnavailable
	}
	var order int64
	err := tx.WithContext(ctx).Raw(`UPDATE recovery_clock SET sequence=sequence+1
WHERE singleton=1 AND sequence<9223372036854775807 RETURNING sequence`).Row().Scan(&order)
	if err != nil || order <= 0 {
		return 0, ErrRecoveryUnavailable
	}
	return order, nil
}

// AdvanceRecoveryFence records an account transition in its write transaction.
// The latest issued order is retained independently of the eligibility fence.
func AdvanceRecoveryFence(ctx context.Context, tx *gorm.DB, userID int64) (int64, error) {
	order, err := NextRecoveryOrder(ctx, tx)
	if err != nil {
		return 0, err
	}
	if err := tx.WithContext(ctx).Exec(`INSERT INTO recovery_account_orders(user_id,eligibility_order) VALUES(?,?)
ON CONFLICT(user_id) DO UPDATE SET eligibility_order=excluded.eligibility_order`, userID, order).Error; err != nil {
		return 0, ErrRecoveryUnavailable
	}
	return order, nil
}

func (s *Store) migratePasswordRecovery(ctx context.Context) error {
	tx, err := s.SQL.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var version int
	var checksum string
	var dirty bool
	if err := tx.QueryRowContext(ctx, "SELECT version,checksum,dirty FROM schema_migrations").Scan(&version, &checksum, &dirty); err != nil {
		return err
	}
	if version != 9 || checksum != emailChangeSchemaChecksum() || dirty {
		return errors.New("unexpected schema before password recovery migration")
	}
	if _, err := tx.ExecContext(ctx, migrations.PasswordRecovery); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE schema_migrations SET version=10,checksum=?,dirty=0 WHERE version=9", passwordRecoverySchemaChecksum()); err != nil {
		return err
	}
	return tx.Commit()
}

// RecoveryWork gives a waiting intake priority over the next background
// database operation. Neither the HTTP response delay nor provider calls use it.
type RecoveryWork struct {
	mu      sync.Mutex
	changed chan struct{}
	intake  int
	running bool
}

func (w *RecoveryWork) notifyLocked() {
	if w.changed != nil {
		close(w.changed)
		w.changed = nil
	}
}

// BeginIntake prevents new background work from starting until release.
func (w *RecoveryWork) BeginIntake() func() {
	if w == nil {
		return func() {}
	}
	w.mu.Lock()
	w.intake++
	w.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			w.mu.Lock()
			w.intake--
			w.notifyLocked()
			w.mu.Unlock()
		})
	}
}

func (w *RecoveryWork) Background(ctx context.Context, operation func() error) error {
	if w == nil {
		return operation()
	}
	for {
		w.mu.Lock()
		if err := ctx.Err(); err != nil {
			w.mu.Unlock()
			return err
		}
		if w.intake == 0 && !w.running {
			w.running = true
			w.mu.Unlock()
			break
		}
		if w.changed == nil {
			w.changed = make(chan struct{})
		}
		change := w.changed
		w.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-change:
		}
	}
	defer func() {
		w.mu.Lock()
		w.running = false
		w.notifyLocked()
		w.mu.Unlock()
	}()
	return operation()
}
