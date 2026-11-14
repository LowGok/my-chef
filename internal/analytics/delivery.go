package analytics

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"time"

	"github.com/LowGok/my-chef/internal/analyticsdataset"
)

type outboxRow struct {
	ID, Incarnation, Subject, Epoch, Name, Platform, Plan string
	Sequence, OccurredUnix                                int64
}

func (s *Service) Deliver(ctx context.Context, fault func(string) error) error {
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
	if !s.collectionReady() {
		return ErrDisabled
	}
	var rows []outboxRow
	if e := s.app.WithContext(ctx).Raw("SELECT * FROM analytics_outbox ORDER BY epoch,sequence LIMIT 100").Scan(&rows).Error; e != nil {
		return e
	}
	batch, e := s.uniqueID(ctx, "bat")
	if e != nil {
		return e
	}
	if _, e = s.journal.ExecContext(ctx, "INSERT INTO delivery_batches(id,created,status) VALUES(?,?,'open')", batch, s.now().Unix()); e != nil {
		return e
	}
	for _, r := range rows {
		p, e := preferenceOn(s.app.WithContext(ctx), r.Incarnation)
		if e != nil {
			return e
		}
		authorized, e := s.authorizedLink(ctx, r.Incarnation, p)
		if e != nil {
			return ErrPrivacy
		}
		if !authorized || !p.Enabled || p.Epoch != r.Epoch || p.Subject != r.Subject || p.NoticeVersion != s.config.NoticeVersion || p.BasisVersion != s.config.Assessment.Version || r.OccurredUnix < s.now().Unix()-90*86400 || r.OccurredUnix > s.now().Unix() {
			if e = s.app.WithContext(ctx).Exec("UPDATE analytics_preferences SET capture_gap=1 WHERE incarnation=?", r.Incarnation).Error; e != nil {
				return e
			}
			if e = s.app.WithContext(ctx).Exec("DELETE FROM analytics_outbox WHERE id=?", r.ID).Error; e != nil {
				return e
			}
			continue
		}
		event := Event{Version: 2, ID: r.ID, Subject: r.Subject, Epoch: r.Epoch, Occurred: r.OccurredUnix, Name: r.Name, Platform: r.Platform, Plan: r.Plan, Sequence: r.Sequence}
		if e = event.Validate(); e != nil {
			return e
		}
		e = func() error {
			tx, e := s.journal.BeginTx(ctx, nil)
			if e != nil {
				return e
			}
			defer func() { _ = tx.Rollback() }()
			var digest string
			e = tx.QueryRowContext(ctx, "SELECT digest FROM delivery_receipts WHERE id=?", event.ID).Scan(&digest)
			if e == nil {
				if digest != event.Digest() {
					return ErrConflict
				}
			} else if errors.Is(e, sql.ErrNoRows) {
				var previous int64
				var contiguous bool
				err := tx.QueryRowContext(ctx, "SELECT sequence,contiguous FROM source_checkpoints WHERE epoch=?", event.Epoch).Scan(&previous, &contiguous)
				if errors.Is(err, sql.ErrNoRows) {
					contiguous = true
				} else if err != nil {
					return err
				}
				contiguous = contiguous && event.Sequence == previous+1
				if _, e = tx.ExecContext(ctx, "INSERT INTO source_checkpoints VALUES(?,?,?) ON CONFLICT(epoch) DO UPDATE SET sequence=max(sequence,excluded.sequence),contiguous=excluded.contiguous", event.Epoch, event.Sequence, contiguous); e != nil {
					return e
				}
				if _, e = tx.ExecContext(ctx, "UPDATE journal_meta SET sequence=sequence+1 WHERE singleton=1"); e != nil {
					return e
				}
				if _, e = tx.ExecContext(ctx, "UPDATE delivery_batches SET accepted=accepted+1 WHERE id=?", batch); e != nil {
					return e
				}

				if _, e = tx.ExecContext(ctx, "INSERT INTO facts VALUES(?,?,?,?,?,?,?,?,?)", event.ID, event.Subject, event.Epoch, event.Occurred, event.Name, event.Platform, event.Plan, event.Sequence, event.Digest()); e != nil {
					return e
				}
				if _, e = tx.ExecContext(ctx, "INSERT INTO delivery_receipts VALUES(?,?,?)", event.ID, event.Digest(), event.Occurred); e != nil {
					return e
				}
			} else {
				return e
			}
			if fault != nil {
				if e = fault("before_journal_commit"); e != nil {
					return e
				}
			}
			return tx.Commit()
		}()
		if e != nil {
			return e
		}
		if fault != nil {
			if e = fault("journal_committed"); e != nil {
				return e
			}
		}
		if e = s.app.WithContext(ctx).Exec("DELETE FROM analytics_outbox WHERE id=?", r.ID).Error; e != nil {
			return e
		}
		s.appDirty.Store(true)
	}
	_, e = s.journal.ExecContext(ctx, "UPDATE delivery_batches SET status='finished' WHERE id=?", batch)
	return e
}
func (s *Service) snapshot(ctx context.Context, id string) (analyticsdataset.ProductionSnapshot, error) {
	a, e := s.readAnchor(ctx)
	if e != nil {
		return analyticsdataset.ProductionSnapshot{}, e
	}
	in := analyticsdataset.ProductionSnapshot{Binding: s.binding, ExtractID: id, Reporting: s.now().Unix(), Generation: a.Generation}
	if e = s.journal.QueryRowContext(ctx, "SELECT sequence FROM journal_meta WHERE singleton=1").Scan(&in.Watermark); e != nil {
		return in, e
	}
	active := map[string]bool{}
	links, e := s.privacy.QueryContext(ctx, "SELECT subject FROM links WHERE state='active'")
	if e != nil {
		return in, e
	}
	for links.Next() {
		var sub string
		if e = links.Scan(&sub); e != nil {
			_ = links.Close()
			return in, e
		}
		active[sub] = true
	}
	if e = links.Close(); e != nil {
		return in, e
	}
	periods, e := s.journal.QueryContext(ctx, "SELECT id,subject,start,end,basis,basis_version,notice,enrollment_source,revision FROM processing_periods ORDER BY start,id")
	if e != nil {
		return in, e
	}
	for periods.Next() {
		var p analyticsdataset.ProductionPeriod
		if e = periods.Scan(&p.ID, &p.Subject, &p.Start, &p.End, &p.Basis, &p.BasisVersion, &p.Notice, &p.Source, &p.Revision); e != nil {
			_ = periods.Close()
			return in, e
		}
		if active[p.Subject] {
			in.Periods = append(in.Periods, p)
		}
	}
	if e = periods.Close(); e != nil {
		return in, e
	}
	facts, e := s.journal.QueryContext(ctx, "SELECT id,subject,occurred,name,platform,plan FROM facts WHERE occurred>=? AND occurred<=? ORDER BY occurred,id", in.Reporting-90*86400, in.Reporting)
	if e != nil {
		return in, e
	}
	for facts.Next() {
		var f analyticsdataset.Activity
		if e = facts.Scan(&f.ID, &f.SubjectID, &f.Occurred, &f.Name, &f.Platform, &f.Plan); e != nil {
			_ = facts.Close()
			return in, e
		}
		if active[f.SubjectID] {
			in.Activity = append(in.Activity, f)
		}
	}
	if e = facts.Close(); e != nil {
		return in, e
	}
	for _, p := range in.Periods {
		for _, feature := range events[:5] {
			id, e := NewID("eli")
			if e != nil {
				return in, e
			}
			in.Eligibility = append(in.Eligibility, analyticsdataset.Eligibility{ID: id, SubjectID: p.Subject, Feature: feature, Start: p.Start, End: p.End})
		}
	}
	// Successful personal/import actions prove authority at that commit only.
	for _, f := range in.Activity {
		if f.Name == "personal_recipe_saved" || f.Name == "recipe_import_saved" {
			id, e := NewID("eli")
			if e != nil {
				return in, e
			}
			end := f.Occurred + 1
			in.Eligibility = append(in.Eligibility, analyticsdataset.Eligibility{ID: id, SubjectID: f.SubjectID, Feature: f.Name, Start: f.Occurred, End: &end})
		}
	}
	cover, e := s.journal.QueryContext(ctx, "SELECT start,end,status,source FROM coverage WHERE end>? AND start<=?", in.Reporting-90*86400, in.Reporting)
	if e != nil {
		return in, e
	}
	for cover.Next() {
		var c analyticsdataset.Coverage
		if e = cover.Scan(&c.Start, &c.End, &c.Status, &c.Source); e != nil {
			_ = cover.Close()
			return in, e
		}
		c.End = min(c.End, in.Reporting+1)
		in.Coverage = append(in.Coverage, c)
	}
	if e = cover.Close(); e != nil {
		return in, e
	}
	for _, feature := range events {
		if slices.Contains(events[:5], feature) {
			for _, c := range in.Coverage {
				if c.Source == "activity" {
					in.Capabilities = append(in.Capabilities, analyticsdataset.ProductionCapability{Feature: feature, Start: c.Start, End: c.End, Status: c.Status})
				}
			}
		} else {
			in.Capabilities = append(in.Capabilities, analyticsdataset.ProductionCapability{Feature: feature, Start: in.Reporting - 90*86400, End: in.Reporting + 1, Status: "unavailable"})
		}
	}
	// Deferred inputs remain explicitly unavailable.
	for _, source := range []string{"eligibility", "satisfaction_invitations", "satisfaction_responses", "communications", "demographics"} {
		in.Coverage = append(in.Coverage, analyticsdataset.Coverage{Source: source, Start: in.Reporting - 90*86400, End: in.Reporting + 1, Status: "unavailable"})
	}
	contributions, e := s.journal.QueryContext(ctx, "SELECT subject,month,metric,numerator,denominator,coverage,observation_status,provenance FROM contributions WHERE month<? AND month>=?", analyticsdataset.ProductionHistoryBefore(in.Reporting), monthStart(in.Reporting).AddDate(0, -12, 0).Format("2006-01"))
	if e != nil {
		return in, e
	}
	authorities := map[analyticsdataset.ProductionAuthority]bool{}
	for contributions.Next() {
		var c analyticsdataset.Contribution
		var encoded string
		if e = contributions.Scan(&c.SubjectID, &c.Month, &c.Metric, &c.Numerator, &c.Denominator, &c.Coverage, &c.ObservationStatus, &encoded); e != nil {
			_ = contributions.Close()
			return in, e
		}
		if active[c.SubjectID] {
			c.ID = "con_" + Digest([]byte(c.SubjectID + c.Month + c.Metric))[:32]
			c.Dimension = "all"
			c.Segment = "all"
			c.QueryVersion = 1
			c.Permission = true
			var retained []analyticsdataset.ProductionAuthority
			if json.Unmarshal([]byte(encoded), &retained) != nil || len(retained) == 0 {
				_ = contributions.Close()
				return in, ErrPrivacy
			}
			for _, a := range retained {
				if a.SubjectID != c.SubjectID || a.Month != c.Month {
					_ = contributions.Close()
					return in, ErrPrivacy
				}
				authorities[a] = true
			}
			in.Contributions = append(in.Contributions, c)
		}
	}
	for a := range authorities {
		in.Authorities = append(in.Authorities, a)
	}
	if e = contributions.Close(); e != nil {
		return in, e
	}
	return in, nil
}
func monthStart(at int64) time.Time {
	t := time.Unix(at, 0).UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}
func (s *Service) Refresh(ctx context.Context) (string, error) {
	if e := s.mu.LockContext(ctx); e != nil {
		return "", e
	}
	defer s.mu.Unlock()
	if s.closed {
		return "", ErrPrivacy
	}
	if e := s.maintainLocked(ctx); e != nil {
		return "", e
	}
	if !s.collectionReady() {
		return "", ErrDisabled
	}
	return s.refreshLocked(ctx)
}
func (s *Service) refreshLocked(ctx context.Context) (string, error) {
	id, e := s.uniqueID(ctx, "ext")
	if e != nil {
		return "", e
	}
	path := filepath.Join(s.root, id+".sqlite")
	in, e := s.snapshot(ctx, id)
	if e != nil {
		return "", e
	}
	if _, e = s.privacy.ExecContext(ctx, "INSERT INTO copies(path,kind,reporting,generation,created,state) VALUES(?,'staging',?,?,?,'building')", path, in.Reporting, in.Generation, in.Reporting); e != nil {
		return "", e
	}
	d, e := analyticsdataset.CreateProduction(ctx, path, in)
	if e != nil {
		_, _ = s.privacy.ExecContext(ctx, "UPDATE copies SET state='quarantined' WHERE path=?", path)
		return "", e
	}

	c, e := d.ProductionContributions(ctx)
	if e == nil {
		var authority []analyticsdataset.ProductionAuthority
		authority, e = d.ProductionAuthorities(ctx)
		if e == nil {
			byMonth := map[string][]analyticsdataset.ProductionAuthority{}
			for _, a := range authority {
				key := a.SubjectID + ":" + a.Month
				byMonth[key] = append(byMonth[key], a)
			}
			var tx *sql.Tx
			tx, e = s.journal.BeginTx(ctx, nil)
			if e == nil {
				for _, v := range c {
					var encoded []byte
					values := byMonth[v.SubjectID+":"+v.Month]
					if len(values) == 0 {
						e = ErrPrivacy
						break
					}
					encoded, e = json.Marshal(values)
					if e != nil {
						break
					}
					_, e = tx.ExecContext(ctx, `INSERT INTO contributions VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(subject,month,metric) DO UPDATE SET numerator=excluded.numerator,denominator=excluded.denominator,provenance=excluded.provenance,coverage=excluded.coverage,observation_status=excluded.observation_status`, v.SubjectID, v.Month, v.Metric, v.Numerator, v.Denominator, string(encoded), v.Coverage, v.ObservationStatus)
					if e != nil {
						break
					}
				}
				if e == nil {
					e = tx.Commit()
				} else {
					_ = tx.Rollback()
				}
			}
		}
	}

	e = errors.Join(e, d.Close())
	if e != nil {
		return "", e
	}
	if e = s.checkAnchor(ctx); e != nil {
		return "", e
	}
	if _, e = s.privacy.ExecContext(ctx, "UPDATE copies SET kind='extract',state='ready' WHERE path=?", path); e != nil {
		return "", e
	}
	if e = s.publishCurrent(ctx, path); e != nil {
		return "", e
	}
	return path, nil
}
func (s *Service) registered(ctx context.Context, path string) error {
	if e := privatePath(path, false); e != nil {
		return e
	}
	rel, e := filepath.Rel(s.root, path)
	if e != nil || rel == "." || rel == ".." || len(rel) > 3 && rel[:3] == "../" {
		return ErrPrivacy
	}
	var n int
	if e = s.privacy.QueryRowContext(ctx, "SELECT count(*) FROM copies WHERE path=? AND kind='extract' AND state='ready'", path).Scan(&n); e != nil || n != 1 {
		return ErrPrivacy
	}
	return nil
}
func (s *Service) WithReport(ctx context.Context, path string, start, end int64, output io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, ReadLeaseLimit)
	defer cancel()
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
	if !s.collectionReady() {
		return ErrDisabled
	}
	if e := s.registered(ctx, path); e != nil {
		return e
	}
	d, e := analyticsdataset.OpenProduction(ctx, path, s.binding, true)
	if e != nil {
		return e
	}
	defer func() { _ = d.Close() }()
	r, e := d.Report(ctx, analyticsdataset.ReportOptions{Start: start, End: end, Dimension: "all"})
	if e != nil {
		return e
	}
	b, e := json.Marshal(r)
	if e != nil {
		return e
	}
	_, e = writeOutput(ctx, output, append(b, '\n'))
	return e
}

func (s *Service) confined(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ErrPrivacy
	}
	rel, e := filepath.Rel(s.root, path)
	if e != nil || rel == "." || rel == ".." || len(rel) > 3 && rel[:3] == "../" {
		return ErrPrivacy
	}
	return privatePath(filepath.Dir(path), true)
}

func (s *Service) WithMonthlyReport(ctx context.Context, path string, output io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, ReadLeaseLimit)
	defer cancel()
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
	if !s.collectionReady() {
		return ErrDisabled
	}
	if e := s.registered(ctx, path); e != nil {
		return e
	}
	d, e := analyticsdataset.OpenProduction(ctx, path, s.binding, true)
	if e != nil {
		return e
	}
	defer func() { _ = d.Close() }()
	r, e := d.Monthly(ctx, "all")
	if e != nil {
		return e
	}
	b, e := json.Marshal(r)
	if e != nil {
		return e
	}
	_, e = writeOutput(ctx, output, append(b, '\n'))
	return e
}
