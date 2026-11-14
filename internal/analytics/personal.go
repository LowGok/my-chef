package analytics

import (
	"context"
	"database/sql"
	"encoding/json"
)

// PersonalExportLease holds the privacy fence through the authenticated account
// stream. The caller releases it on every path, including a broken transfer.
// Acquire the account-work lock before this lease and the app SQL snapshot after it.
func (s *Service) PersonalExportLease(ctx context.Context, user int64) ([]byte, func(), error) {
	if e := s.mu.LockContext(ctx); e != nil {
		return nil, nil, e
	}
	fail := func(e error) ([]byte, func(), error) { s.mu.Unlock(); return nil, nil, e }
	if s.closed {
		return fail(ErrPrivacy)
	}
	if e := s.maintainLocked(ctx); e != nil {
		return fail(e)
	}
	var inc string
	e := s.app.WithContext(ctx).Raw("SELECT incarnation FROM account_incarnations WHERE user_id=?", user).Row().Scan(&inc)
	if e != nil {
		return fail(e)
	}
	p, e := preferenceOn(s.app.WithContext(ctx), inc)
	if e != nil {
		return fail(e)
	}
	p, e = s.controlledPreference(ctx, inc, p)
	if e != nil {
		return fail(e)
	}
	out := struct {
		Preference    Preference       `json:"preference"`
		History       []map[string]any `json:"history"`
		Events        []map[string]any `json:"events"`
		Contributions []map[string]any `json:"monthlyContributions"`
		Definitions   string           `json:"definitions"`
	}{Preference: s.present(p), History: []map[string]any{}, Events: []map[string]any{}, Contributions: []map[string]any{}, Definitions: "Completed core app actions. UTC seconds; monthly binary contributions. Only retained processing periods are observed. Product participation can be stopped in Profile."}
	type receipt struct {
		Revision                                      int64
		Enabled                                       bool
		Source, NoticeVersion, BasisVersion, Platform string
		ChangedUnix                                   int64
	}
	var receipts []receipt
	if e = s.app.WithContext(ctx).Raw("SELECT revision,enabled,source,notice_version,basis_version,platform,changed_unix FROM analytics_receipts WHERE incarnation=? ORDER BY revision", inc).Scan(&receipts).Error; e != nil {
		return fail(e)
	}
	for _, r := range receipts {
		out.History = append(out.History, map[string]any{"revision": r.Revision, "enabled": r.Enabled, "source": r.Source, "noticeVersion": r.NoticeVersion, "basisVersion": r.BasisVersion, "platform": r.Platform, "changedUnix": r.ChangedUnix})
	}
	rows, e := s.privacy.QueryContext(ctx, "SELECT subject FROM links WHERE incarnation=? AND state='active'", inc)
	if e != nil {
		return fail(e)
	}
	var subjects []string
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			_ = rows.Close()
			return fail(e)
		}
		subjects = append(subjects, id)
	}
	if e = rows.Close(); e != nil {
		return fail(e)
	}
	for _, id := range subjects {
		periods, e := s.journal.QueryContext(ctx, "SELECT start,end,basis,basis_version,notice,enrollment_source,revision FROM processing_periods WHERE subject=? ORDER BY start,id", id)
		if e != nil {
			return fail(e)
		}
		for periods.Next() {
			var start, revision int64
			var end sql.NullInt64
			var basis, basisVersion, notice, source string
			if e = periods.Scan(&start, &end, &basis, &basisVersion, &notice, &source, &revision); e != nil {
				_ = periods.Close()
				return fail(e)
			}
			var until any
			if end.Valid {
				until = end.Int64
			}
			out.History = append(out.History, map[string]any{"kind": "processing_period", "startUnix": start, "endUnix": until, "basis": basis, "basisVersion": basisVersion, "noticeVersion": notice, "source": source, "revision": revision})
		}
		if e = periods.Close(); e != nil {
			return fail(e)
		}

		facts, e := s.journal.QueryContext(ctx, "SELECT occurred,name,platform,plan FROM facts WHERE subject=? ORDER BY occurred,id", id)
		if e != nil {
			return fail(e)
		}
		for facts.Next() {
			var at int64
			var name, platform, plan string
			if e = facts.Scan(&at, &name, &platform, &plan); e != nil {
				_ = facts.Close()
				return fail(e)
			}
			out.Events = append(out.Events, map[string]any{"occurredUnix": at, "name": name, "platform": platform, "plan": plan})
		}
		if e = facts.Close(); e != nil {
			return fail(e)
		}
		contrib, e := s.journal.QueryContext(ctx, "SELECT month,metric,numerator,denominator,coverage,observation_status FROM contributions WHERE subject=? ORDER BY month,metric", id)
		if e != nil {
			return fail(e)
		}
		for contrib.Next() {
			var month, metric, coverage, status string
			var n, d int
			if e = contrib.Scan(&month, &metric, &n, &d, &coverage, &status); e != nil {
				_ = contrib.Close()
				return fail(e)
			}
			out.Contributions = append(out.Contributions, map[string]any{"month": month, "metric": metric, "numerator": n, "denominator": d, "coverage": coverage, "observationStatus": status})
		}
		if e = contrib.Close(); e != nil {
			return fail(e)
		}
	}
	b, e := json.Marshal(out)
	if e != nil {
		return fail(e)
	}
	return b, s.mu.Unlock, nil
}

// WithDeletionFence serializes account deletion with delivery, extract builders,
// reports and exports. The callback invokes revoke inside its authenticated app
// transaction; the independent intent survives an uncertain product commit.
func (s *Service) WithDeletionFence(ctx context.Context, work func(revoke func(string) error) error) error {
	if e := s.mu.LockContext(ctx); e != nil {
		return e
	}
	defer s.mu.Unlock()
	if s.closed {
		return ErrPrivacy
	}
	if e := s.maintainLocked(ctx); e != nil {
		return e
	}
	if e := work(func(inc string) error { return s.revokeOn(ctx, inc) }); e != nil {
		return e
	}
	return s.maintainLocked(ctx)
}

// ReconcileAppRestore pauses every recovered account and resolves links against
// the independent lookup before an app-only restore can be published.
func (s *Service) ReconcileAppRestore(ctx context.Context) error {
	if e := s.mu.LockContext(ctx); e != nil {
		return e
	}
	defer s.mu.Unlock()
	if e := s.checkAnchor(ctx); e != nil {
		return e
	}
	if e := s.app.WithContext(ctx).Exec("DELETE FROM analytics_outbox").Error; e != nil {
		return e
	}
	if e := s.app.WithContext(ctx).Exec("DELETE FROM analytics_generation").Error; e != nil {
		return e
	}
	if e := s.app.WithContext(ctx).Exec("DELETE FROM analytics_preferences").Error; e != nil {
		return e
	}
	if e := s.restoreControls(ctx); e != nil {
		return e
	}
	// Retained subjects whose original account incarnation is absent are erased.
	rows, e := s.privacy.QueryContext(ctx, "SELECT DISTINCT incarnation FROM links WHERE state IN('active','prepared')")
	if e != nil {
		return e
	}
	var incarnations []string
	for rows.Next() {
		var inc string
		if e = rows.Scan(&inc); e != nil {
			_ = rows.Close()
			return e
		}
		incarnations = append(incarnations, inc)
	}
	if e = rows.Close(); e != nil {
		return e
	}
	for _, inc := range incarnations {
		var n int64
		if e = s.app.WithContext(ctx).Raw("SELECT count(*) FROM account_incarnations WHERE incarnation=?", inc).Row().Scan(&n); e != nil {
			return e
		}
		if n == 0 {
			if e = s.revokeOn(ctx, inc); e != nil {
				return e
			}
		}
	}
	// Close old epochs at the last trustworthy maintained instant. Later enabling
	// retains the subject while a new period records the observation gap.
	a, e := s.readAnchor(ctx)
	if e != nil {
		return e
	}
	if _, e = s.journal.ExecContext(ctx, "UPDATE processing_periods SET end=max(start+1,?) WHERE end IS NULL", a.Maintenance); e != nil {
		return e
	}
	if e = s.maintainLocked(ctx); e != nil {
		return e
	}
	return s.app.WithContext(ctx).Exec("UPDATE analytics_installation SET restore_paused=0 WHERE singleton=1").Error
}
