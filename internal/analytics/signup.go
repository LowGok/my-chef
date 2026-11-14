package analytics

import (
	"context"
	"time"

	"gorm.io/gorm"
)

type SignupChoice struct {
	Enabled       *bool
	NoticeVersion string
}

func (s *Service) SignupAllowed(notice string) bool {
	return s != nil && s.collectionReady() && notice == s.config.NoticeVersion && s.checkAnchor(context.Background()) == nil
}
func RecordSignupChoice(ctx context.Context, tx *gorm.DB, key string, in SignupChoice, s *Service, now time.Time) error {
	enabled := in.Enabled != nil && *in.Enabled && s.SignupAllowed(in.NoticeVersion)
	source := "registration_unavailable"
	if in.Enabled != nil && !*in.Enabled && s.SignupAllowed(in.NoticeVersion) {
		source = "objection"
	} else if enabled {
		source = "registration_default"
	}
	if e := tx.SavePoint("analytics_signup").Error; e != nil {
		return nil
	}
	e := tx.Exec(`INSERT INTO analytics_signup(email_key,enabled,notice_version,expires_unix,source) VALUES(?,?,?,?,?) ON CONFLICT(email_key) DO UPDATE SET enabled=CASE WHEN analytics_signup.expires_unix<=? THEN excluded.enabled ELSE min(analytics_signup.enabled,excluded.enabled) END,notice_version=CASE WHEN analytics_signup.expires_unix<=? THEN excluded.notice_version WHEN analytics_signup.enabled=0 THEN analytics_signup.notice_version ELSE excluded.notice_version END,source=CASE WHEN analytics_signup.expires_unix<=? OR analytics_signup.enabled=1 THEN excluded.source ELSE analytics_signup.source END,expires_unix=max(analytics_signup.expires_unix,excluded.expires_unix)`, key, enabled, in.NoticeVersion, now.Add(25*time.Hour).Unix(), source, now.Unix(), now.Unix(), now.Unix()).Error
	if e != nil {
		if e = tx.RollbackTo("analytics_signup").Error; e != nil {
			return e
		}
	}
	return tx.Exec("RELEASE SAVEPOINT analytics_signup").Error
}
func InspectSignupChoice(ctx context.Context, tx *gorm.DB, key string, s *Service, now time.Time) (bool, string) {
	var row struct {
		Enabled       bool
		NoticeVersion string
		ExpiresUnix   int64
	}
	r := tx.WithContext(ctx).Raw("SELECT enabled,notice_version,expires_unix FROM analytics_signup WHERE email_key=?", key).Scan(&row)
	return r.Error == nil && r.RowsAffected == 1 && row.ExpiresUnix > now.Unix() && row.Enabled && s.SignupAllowed(row.NoticeVersion), row.NoticeVersion
}
func SignupTransaction(ctx context.Context, s *Service, work func() error) error {
	if s == nil {
		return work()
	}
	if e := s.mu.LockContext(ctx); e != nil {
		return e
	}
	defer s.mu.Unlock()
	e := work()
	if e == nil {
		_ = s.reconcilePrepared(ctx)
	}
	return e
}
func EnrollSignup(ctx context.Context, tx *gorm.DB, user int64, key string, in SignupChoice, s *Service, now time.Time) error {
	inc, e := incarnationOn(tx, user)
	if e != nil {
		return e
	}
	var row struct {
		Enabled               bool
		NoticeVersion, Source string
		ExpiresUnix           int64
	}
	r := tx.Raw("SELECT enabled,notice_version,source,expires_unix FROM analytics_signup WHERE email_key=?", key).Scan(&row)
	enabled := r.Error == nil && r.RowsAffected == 1 && row.ExpiresUnix > now.Unix() && row.Enabled && in.Enabled != nil && *in.Enabled && in.NoticeVersion == row.NoticeVersion && s.SignupAllowed(in.NoticeVersion)
	p := preferenceRow{Revision: 1, Source: "registration_unavailable", DeletionStatus: "none", ChangedUnix: now.Unix()}
	if row.Source == "objection" || in.Enabled != nil && !*in.Enabled && s.SignupAllowed(in.NoticeVersion) {
		p.Source = "objection"
		p.DeletionStatus = "complete"
	}
	if s != nil {
		p.NoticeVersion = s.config.NoticeVersion
		p.NoticeHash = s.config.NoticeHash
		p.BasisVersion = s.config.Assessment.Version
	}
	if e = tx.SavePoint("analytics_enrollment").Error; e != nil {
		return nil
	}
	enroll := func() error {
		p.Enabled = enabled
		if enabled {
			p.Source = "registration_default"
			p.Subject, e = s.uniqueID(ctx, "sub")
			if e != nil {
				return e
			}
			p.Epoch, e = s.uniqueID(ctx, "per")
			if e != nil {
				return e
			}
			if e = s.prepareEnrollment(ctx, p.Subject, inc, p.Epoch, p.Revision); e != nil {
				return e
			}
		}
		if !enabled && s != nil {
			if e = s.privacyChange(ctx, "INSERT INTO account_controls VALUES(?,?,?,?) ON CONFLICT(incarnation) DO NOTHING", inc, p.Revision, p.Source == "objection", now.Unix()); e != nil {
				return e
			}
		}
		if e = tx.Exec(`INSERT INTO analytics_preferences(incarnation,enabled,revision,subject,epoch,notice_version,notice_hash,basis_version,source,changed_unix,deletion_status) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, inc, p.Enabled, p.Revision, p.Subject, p.Epoch, p.NoticeVersion, p.NoticeHash, p.BasisVersion, p.Source, p.ChangedUnix, p.DeletionStatus).Error; e != nil {
			return e
		}
		id, e := NewID("rcp")
		if e != nil {
			return e
		}
		return tx.Exec("INSERT INTO analytics_receipts VALUES(?,?,?,?,?,?,?,?,?)", id, inc, p.Revision, p.Enabled, p.Source, p.NoticeVersion, p.BasisVersion, p.ChangedUnix, Platform(ctx)).Error
	}
	if e = enroll(); e != nil {
		if e = tx.RollbackTo("analytics_enrollment").Error; e != nil {
			return e
		}
	}
	if e = tx.Exec("RELEASE SAVEPOINT analytics_enrollment").Error; e != nil {
		return e
	}
	return tx.Exec("DELETE FROM analytics_signup WHERE email_key=?", key).Error
}
