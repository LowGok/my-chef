package mailqueue

import (
	"context"
	"time"

	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
)

// SweepAccountProofSlice runs under exclusive account work ownership. It starts
// no signup/recovery worker and only removes ended, purpose-owned authority.
func SweepAccountProofSlice(ctx context.Context, tx *gorm.DB, now time.Time) (bool, error) {
	return store.SweepExpiredAccountProofSlice(ctx, tx, now)
}
