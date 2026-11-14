package analytics

import (
	"context"
	"database/sql"
	"errors"
)

func (s *Service) reconcilePrepared(ctx context.Context) error {
	rows, e := s.privacy.QueryContext(ctx, "SELECT subject,incarnation,epoch FROM links WHERE state='prepared'")
	if e != nil {
		return e
	}
	type link struct{ subject, inc, epoch string }
	var links []link
	for rows.Next() {
		var l link
		if e = rows.Scan(&l.subject, &l.inc, &l.epoch); e != nil {
			_ = rows.Close()
			return e
		}
		links = append(links, l)
	}
	if e = rows.Close(); e != nil {
		return e
	}
	for _, l := range links {
		p, e := preferenceOn(s.app.WithContext(ctx), l.inc)
		if e != nil {
			return e
		}
		if p.Enabled && p.Subject == l.subject {
			if e = s.ensurePeriod(ctx, p); e != nil {
				return e
			}
			if e = s.privacyChange(ctx, "UPDATE links SET epoch=?,state='active' WHERE subject=? AND state='prepared'", p.Epoch, l.subject); e != nil {
				return e
			}
			continue
		}
		// A failed app transaction may have prepared a replacement epoch for an
		// already retained subject. Keep its cleanup lookup and close capture.
		var epoch string
		e = s.journal.QueryRowContext(ctx, "SELECT id FROM processing_periods WHERE subject=? ORDER BY start DESC,id DESC LIMIT 1", l.subject).Scan(&epoch)
		if e == nil {
			if e = s.privacyChange(ctx, "UPDATE links SET epoch=?,state='active' WHERE subject=?", epoch, l.subject); e != nil {
				return e
			}
			continue
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if e = s.privacyChange(ctx, "DELETE FROM links WHERE subject=? AND state='prepared'", l.subject); e != nil {
			return e
		}
	}
	return nil
}
func (s *Service) ensurePeriod(ctx context.Context, p preferenceRow) error {
	if _, e := s.journal.ExecContext(ctx, "UPDATE processing_periods SET end=max(start+1,?) WHERE subject=? AND id!=? AND end IS NULL", p.ChangedUnix, p.Subject, p.Epoch); e != nil {
		return e
	}
	_, e := s.journal.ExecContext(ctx, `INSERT OR IGNORE INTO processing_periods VALUES(?,?,?,NULL,?,?,?,?,?)`, p.Epoch, p.Subject, p.ChangedUnix, "legitimate_interest", p.BasisVersion, p.NoticeVersion, p.Source, p.Revision)
	return e
}
