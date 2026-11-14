package analytics

import (
	"context"
	"path/filepath"

	"gorm.io/gorm"
)

func (s *Service) uniqueID(ctx context.Context, prefix string) (string, error) {
	for range 16 {
		id, e := NewID(prefix)
		if e != nil {
			return "", e
		}
		var n int
		switch prefix {
		case "sub":
			e = s.privacy.QueryRowContext(ctx, "SELECT count(*) FROM links WHERE subject=?", id).Scan(&n)
		case "per":
			e = s.journal.QueryRowContext(ctx, "SELECT count(*) FROM processing_periods WHERE id=?", id).Scan(&n)
			if e == nil && n == 0 {
				e = s.privacy.QueryRowContext(ctx, "SELECT count(*) FROM links WHERE epoch=?", id).Scan(&n)
			}
		case "bat":
			e = s.journal.QueryRowContext(ctx, "SELECT count(*) FROM delivery_batches WHERE id=?", id).Scan(&n)
		case "ext", "stg":
			e = s.privacy.QueryRowContext(ctx, "SELECT count(*) FROM copies WHERE path=?", filepath.Join(s.root, id+".sqlite")).Scan(&n)
		default:
			return "", ErrInput
		}
		if e != nil {
			return "", ErrPrivacy
		}
		if n == 0 {
			return id, nil
		}
	}
	return "", ErrConflict
}
func (s *Service) uniqueEventID(ctx context.Context, tx *gorm.DB) (string, error) {
	for range 16 {
		id, e := NewID("evt")
		if e != nil {
			return "", e
		}
		var pending int64
		if e = tx.Raw("SELECT count(*) FROM analytics_outbox WHERE id=?", id).Row().Scan(&pending); e != nil {
			return "", e
		}
		var accepted int
		if e = s.journal.QueryRowContext(ctx, "SELECT count(*) FROM delivery_receipts WHERE id=?", id).Scan(&accepted); e != nil {
			return "", e
		}
		if pending == 0 && accepted == 0 {
			return id, nil
		}
	}
	return "", ErrConflict
}
