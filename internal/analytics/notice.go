package analytics

import (
	"context"

	"gorm.io/gorm"
)

type PublicNotice struct {
	Available      bool   `json:"available"`
	DefaultEnabled bool   `json:"defaultEnabled"`
	Version        string `json:"version"`
	Hash           string `json:"hash"`
	Basis          string `json:"basis"`
	Text           string `json:"text"`
}

const NoticeText = "Help improve MyChef with limited records of completed core actions. We use random server identifiers, the action, time, platform and authorized plan. Events are kept for 90 days and monthly contributions for 13 UTC month buckets. Small groups are suppressed. You can turn this off in Profile at any time; isolated encrypted recovery copies are removed within seven days. Optional demographics and marketing are separate."

func (s *Service) Notice() PublicNotice {
	available := s.collectionReady()
	return PublicNotice{Available: available, DefaultEnabled: available, Version: s.config.NoticeVersion, Hash: s.config.NoticeHash, Basis: s.config.Assessment.Basis, Text: NoticeText}
}

type platformKey struct{}

func WithPlatform(ctx context.Context, platform string) context.Context {
	return context.WithValue(ctx, platformKey{}, platform)
}
func Platform(ctx context.Context) string {
	p, _ := ctx.Value(platformKey{}).(string)
	if p != "ios" && p != "android" {
		return "web"
	}
	return p
}
func DisabledPreference(ctx context.Context, app *gorm.DB, user int64) (Preference, error) {
	inc, e := incarnationOn(app.WithContext(ctx), user)
	if e != nil {
		return Preference{}, e
	}
	p, e := preferenceOn(app.WithContext(ctx), inc)
	if e != nil {
		return Preference{}, e
	}
	state := "off"
	if p.Enabled {
		state = "review_required"
	}
	return Preference{Enabled: false, Revision: p.Revision, NoticeVersion: p.NoticeVersion, Source: p.Source, State: state, DeletionStatus: p.DeletionStatus, Available: false}, nil
}
func UpdateDisabledPreference(ctx context.Context, app *gorm.DB, user int64, in PreferenceUpdate) (Preference, error) {
	if in.Enabled {
		return Preference{}, ErrDisabled
	}
	e := app.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var required bool
		if e := tx.Raw("SELECT required FROM analytics_installation WHERE singleton=1").Row().Scan(&required); e != nil || required {
			return ErrPrivacy
		}
		inc, e := incarnationOn(tx, user)
		if e != nil {
			return e
		}
		p, e := preferenceOn(tx, inc)
		if e != nil {
			return e
		}
		if p.Revision != in.ExpectedRevision {
			return ErrConflict
		}
		return tx.Exec(`INSERT INTO analytics_preferences(incarnation,enabled,revision,changed_unix,source,deletion_status) VALUES(?,0,?,unixepoch(),'objection','complete') ON CONFLICT(incarnation) DO UPDATE SET enabled=0,revision=revision+1,source='objection',deletion_status='complete'`, inc, p.Revision+1).Error
	})
	if e != nil {
		return Preference{}, e
	}
	return DisabledPreference(ctx, app, user)
}
