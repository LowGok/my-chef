package analytics

import (
	"context"
	"database/sql"
	"path/filepath"
	"time"
)

// CheckReadOnlyStorage inspects already opened read-only handles. It never
// opens a service, reconciles privacy state, creates files or starts workers.
func CheckReadOnlyStorage(ctx context.Context, app, privacy, journal *sql.DB, o OpenOptions, binding string, now time.Time) error {
	var actual string
	if e := app.QueryRowContext(ctx, "SELECT binding FROM lifecycle_binding WHERE singleton=1").Scan(&actual); e != nil || actual != binding {
		return ErrPrivacy
	}
	var required, paused int
	if e := app.QueryRowContext(ctx, "SELECT required,restore_paused FROM analytics_installation WHERE singleton=1").Scan(&required, &paused); e != nil || required != 1 || paused != 0 {
		return ErrPrivacy
	}
	s := &Service{root: o.Root, privacyPath: o.PrivacyPath, anchorPath: o.AnchorPath, binding: binding,
		privacy: privacy, journal: journal, now: func() time.Time { return now }}
	if s.validateSchema(ctx, privacy, privacySchema) != nil || s.validateSchema(ctx, journal, journalSchema) != nil || s.checkAnchor(ctx) != nil || s.checkInventory(ctx) != nil {
		return ErrPrivacy
	}
	var version, applicationID int
	var environment, source, checksum string
	if e := journal.QueryRowContext(ctx, "PRAGMA application_id").Scan(&applicationID); e != nil || applicationID != 1296253263 {
		return ErrPrivacy
	}
	if e := journal.QueryRowContext(ctx, "SELECT version,environment,binding,checksum FROM journal_meta WHERE singleton=1").Scan(&version, &environment, &source, &checksum); e != nil || version != 2 || environment != "production" || source != binding || checksum != Digest([]byte(journalSchema)) {
		return ErrPrivacy
	}
	var unresolved int
	if e := privacy.QueryRowContext(ctx, "SELECT count(*) FROM copies WHERE state IN('building','quarantined') OR state!='deleted' AND deadline>0 AND deadline<=?", now.Unix()).Scan(&unresolved); e != nil || unresolved != 0 {
		return ErrPrivacy
	}
	var registered int
	if e := privacy.QueryRowContext(ctx, "SELECT count(*) FROM copies WHERE path=? AND kind='journal' AND state='ready'", filepath.Join(o.Root, "journal.sqlite")).Scan(&registered); e != nil || registered != 1 {
		return ErrPrivacy
	}
	return nil
}
