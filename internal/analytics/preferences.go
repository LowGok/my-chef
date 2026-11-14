package analytics

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"gorm.io/gorm"
)

type Preference struct {
	Enabled        bool   `json:"enabled"`
	Revision       int64  `json:"revision"`
	NoticeVersion  string `json:"noticeVersion"`
	NoticeHash     string `json:"noticeHash"`
	Basis          string `json:"basis"`
	BasisVersion   string `json:"basisVersion"`
	Source         string `json:"source"`
	State          string `json:"state"`
	DeletionStatus string `json:"deletionStatus"`
	Available      bool   `json:"available"`
}
type PreferenceUpdate struct {
	Enabled          bool   `json:"enabled"`
	NoticeVersion    string `json:"noticeVersion"`
	ExpectedRevision int64  `json:"expectedRevision"`
}
type preferenceRow struct {
	Enabled                                                                         bool
	Revision                                                                        int64
	Subject, Epoch, NoticeVersion, NoticeHash, BasisVersion, Source, DeletionStatus string
	ChangedUnix, Sequence                                                           int64
	CaptureGap                                                                      int
}

func incarnationOn(tx *gorm.DB, user int64) (string, error) {
	var inc string
	e := tx.Raw(`SELECT a.incarnation FROM account_incarnations a JOIN users u ON u.user_id=a.user_id WHERE a.user_id=? AND u.email_verified_at IS NOT NULL`, user).Row().Scan(&inc)
	if e != nil {
		return "", ErrInput
	}
	return inc, nil
}
func preferenceOn(tx *gorm.DB, inc string) (preferenceRow, error) {
	var p preferenceRow
	r := tx.Raw("SELECT * FROM analytics_preferences WHERE incarnation=?", inc).Scan(&p)
	if r.Error != nil {
		return p, r.Error
	}
	if r.RowsAffected == 0 {
		p.Source = "migration_off"
		p.DeletionStatus = "none"
	}
	return p, nil
}
func (s *Service) present(p preferenceRow) Preference {
	available := s.collectionReady()
	state := "off"
	if p.Enabled {
		state = "on"
		if !available || p.NoticeVersion != s.config.NoticeVersion || p.BasisVersion != s.config.Assessment.Version {
			state = "review_required"
		}
	}
	if p.DeletionStatus != "none" && p.DeletionStatus != "complete" {
		state = "deletion_in_progress"
	}
	return Preference{Enabled: p.Enabled && state == "on", Revision: p.Revision, NoticeVersion: s.config.NoticeVersion, NoticeHash: s.config.NoticeHash, Basis: s.config.Assessment.Basis, BasisVersion: s.config.Assessment.Version, Source: p.Source, State: state, DeletionStatus: p.DeletionStatus, Available: available}
}
func (s *Service) Preference(ctx context.Context, user int64) (Preference, error) {
	if e := s.mu.LockContext(ctx); e != nil {
		return Preference{}, e
	}
	defer s.mu.Unlock()
	if s.closed {
		return Preference{}, ErrPrivacy
	}
	inc, e := incarnationOn(s.app.WithContext(ctx), user)
	if e != nil {
		return Preference{}, e
	}
	p, e := preferenceOn(s.app.WithContext(ctx), inc)
	if e != nil {
		return Preference{}, e
	}
	p, e = s.controlledPreference(ctx, inc, p)
	if e != nil {
		return Preference{}, e
	}
	out := s.present(p)
	if e = s.checkAnchor(ctx); e != nil {
		out.Enabled = false
		out.Available = false
		out.State = "review_required"
	}
	status, e := s.pendingDeletion(ctx, inc)
	if e != nil {
		return Preference{}, ErrPrivacy
	}
	if status != "" {
		out.DeletionStatus = status
	}

	var active int
	if e = s.privacy.QueryRowContext(ctx, "SELECT count(*) FROM links WHERE incarnation=? AND state='active'", inc).Scan(&active); e != nil {
		return Preference{}, ErrPrivacy
	}
	if p.Enabled && active == 0 {
		out.Enabled = false
		out.State = "deletion_in_progress"
		out.DeletionStatus = "pending"
	}
	if !p.Enabled && active > 0 && p.Source != "objection" {
		out.State = "review_required"
	}
	return out, nil
}
func (s *Service) Update(ctx context.Context, user int64, in PreferenceUpdate, platform string) (Preference, error) {
	if e := s.mu.LockContext(ctx); e != nil {
		return Preference{}, e
	}
	defer s.mu.Unlock()
	if s.closed {
		return Preference{}, ErrPrivacy
	}
	if !slices.Contains([]string{"web", "ios", "android"}, platform) || in.ExpectedRevision < 0 {
		return Preference{}, ErrInput
	}
	if e := s.checkAnchor(ctx); e != nil {
		return Preference{}, e
	}
	if in.Enabled {
		if e := s.maintainLocked(ctx); e != nil {
			return Preference{}, e
		}
	}
	var out preferenceRow
	var inc string
	e := s.app.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var e error
		inc, e = incarnationOn(tx, user)
		if e != nil {
			return e
		}
		old, e := preferenceOn(tx, inc)
		if e != nil {
			return e
		}
		old, e = s.controlledPreference(ctx, inc, old)
		if e != nil {
			return e
		}
		if old.Revision != in.ExpectedRevision {
			return ErrConflict
		}
		if in.Enabled && (!s.collectionReady() || in.NoticeVersion != s.config.NoticeVersion) {
			return ErrDisabled
		}
		if !in.Enabled {
			// Durable intent and generation fence precede app acknowledgement. It survives an app rollback.
			if e = s.revokeOn(ctx, inc, old.Revision+1); e != nil {
				return e
			}
			if e = tx.Exec("DELETE FROM analytics_outbox WHERE incarnation=?", inc).Error; e != nil {
				return e
			}
			s.appDirty.Store(true)
			out = old
			out.Enabled = false
			out.Revision++
			out.Subject = ""
			out.Epoch = ""
			out.Source = "objection"
			out.DeletionStatus = "pending"
		} else {
			out = old
			out.Enabled = true
			out.Revision++
			out.Source = "explicit_enable"
			out.DeletionStatus = "none"
			if old.Enabled && old.Subject != "" && old.NoticeVersion == s.config.NoticeVersion && old.BasisVersion == s.config.Assessment.Version {
				out.Revision = old.Revision
				return nil
			}
			// A retained, unobjected subject can resume after restore or a notice pause.
			var retained string
			e = s.privacy.QueryRowContext(ctx, "SELECT subject FROM links WHERE incarnation=? AND state='active' ORDER BY created DESC LIMIT 1", inc).Scan(&retained)
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return ErrPrivacy
			}
			out.Subject = retained
			if out.Subject == "" {
				out.Subject, e = s.uniqueID(ctx, "sub")
				if e != nil {
					return e
				}
			}
			out.Epoch, e = s.uniqueID(ctx, "per")
			if e != nil {
				return e
			}
			e = s.prepareEnrollment(ctx, out.Subject, inc, out.Epoch, out.Revision)
			if e != nil {
				return ErrPrivacy
			}
		}
		out.NoticeVersion = s.config.NoticeVersion
		out.NoticeHash = s.config.NoticeHash
		out.BasisVersion = s.config.Assessment.Version
		out.ChangedUnix = s.now().Unix()
		if e = tx.Exec(`INSERT INTO analytics_preferences(incarnation,enabled,revision,subject,epoch,notice_version,notice_hash,basis_version,source,changed_unix,deletion_status) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(incarnation) DO UPDATE SET enabled=excluded.enabled,revision=excluded.revision,subject=excluded.subject,epoch=excluded.epoch,notice_version=excluded.notice_version,notice_hash=excluded.notice_hash,basis_version=excluded.basis_version,source=excluded.source,changed_unix=excluded.changed_unix,deletion_status=excluded.deletion_status,sequence=0`, inc, out.Enabled, out.Revision, out.Subject, out.Epoch, out.NoticeVersion, out.NoticeHash, out.BasisVersion, out.Source, out.ChangedUnix, out.DeletionStatus).Error; e != nil {
			return e
		}
		id, e := NewID("rcp")
		if e != nil {
			return e
		}
		return tx.Exec("INSERT INTO analytics_receipts VALUES(?,?,?,?,?,?,?,?,?)", id, inc, out.Revision, out.Enabled, out.Source, out.NoticeVersion, out.BasisVersion, out.ChangedUnix, platform).Error
	})
	if e != nil {
		return Preference{}, e
	}
	if out.Enabled {
		if e = s.ensurePeriod(ctx, out); e != nil {
			return Preference{}, ErrPrivacy
		}
		if e = s.privacyChange(ctx, "UPDATE links SET state='active' WHERE subject=? AND epoch=? AND state='prepared'", out.Subject, out.Epoch); e != nil {
			return Preference{}, ErrPrivacy
		}
	}

	result := s.present(out)
	if status, e := s.pendingDeletion(ctx, inc); e == nil && status != "" {
		result.DeletionStatus = status
	}
	return result, nil
}
func (s *Service) revokeOn(ctx context.Context, inc string, nextRevision ...int64) error {
	tx, e := s.privacy.BeginTx(ctx, nil)
	if e != nil {
		return ErrPrivacy
	}
	defer func() { _ = tx.Rollback() }()
	var revision int64
	e = tx.QueryRowContext(ctx, "SELECT revision FROM account_controls WHERE incarnation=?", inc).Scan(&revision)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return ErrPrivacy
	}
	revision++
	if len(nextRevision) > 0 {
		revision = max(revision, nextRevision[0])
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO account_controls VALUES(?,?,1,?) ON CONFLICT(incarnation) DO UPDATE SET revision=excluded.revision,objection=1,changed=excluded.changed", inc, revision, s.now().Unix()); e != nil {
		return ErrPrivacy
	}
	if _, e = tx.ExecContext(ctx, "UPDATE links SET state='revoked',revoked=coalesce(revoked,?),cleanup='pending' WHERE incarnation=? AND cleanup!='complete'", s.now().Unix(), inc); e != nil {
		return ErrPrivacy
	}
	if _, e = tx.ExecContext(ctx, "UPDATE privacy_meta SET generation=generation+1 WHERE singleton=1"); e != nil {
		return ErrPrivacy
	}
	if e = tx.Commit(); e != nil {
		return ErrPrivacy
	}
	return s.writeAnchor(ctx)
}

func (s *Service) privacyChange(ctx context.Context, query string, args ...any) error {
	tx, e := s.privacy.BeginTx(ctx, nil)
	if e != nil {
		return ErrPrivacy
	}
	defer func() { _ = tx.Rollback() }()
	if _, e = tx.ExecContext(ctx, query, args...); e != nil {
		return ErrPrivacy
	}
	if _, e = tx.ExecContext(ctx, "UPDATE privacy_meta SET generation=generation+1 WHERE singleton=1"); e != nil {
		return ErrPrivacy
	}
	if e = tx.Commit(); e != nil {
		return ErrPrivacy
	}
	return s.writeAnchor(ctx)
}
