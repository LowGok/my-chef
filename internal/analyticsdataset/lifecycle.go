package analyticsdataset

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"slices"
	"sort"
)

func derivedID(value string) string { return fmt.Sprintf("z_%x", sha256.Sum256([]byte(value)))[:18] }

func (d *Dataset) refresh(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM monthly_contributions WHERE origin='derived'"); err != nil {
		return ErrDataset
	}
	coverage, err := loadCoverage(ctx, tx)
	if err != nil {
		return err
	}
	lookup, err := loadSegmentation(ctx, tx)
	if err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO monthly_contributions
 (id,subject_id,month,metric,dimension,segment,numerator,denominator,query_version,permission_validated,coverage,survey_version,observation_status,_digest,origin)
 VALUES(?,?,?,?,?,?,?,?,1,1,?,?,?,'derived','derived')`)
	if err != nil {
		return ErrDataset
	}
	defer func() { _ = stmt.Close() }()
	for month := monthStart(d.reportingUnix).AddDate(0, -12, 0); !month.After(monthStart(d.reportingUnix)); month = month.AddDate(0, 1, 0) {
		if month.Unix() < d.cutoff() {
			continue
		}
		o := ReportOptions{Start: month.Unix(), End: min(month.AddDate(0, 1, 0).Unix(), d.reportingUnix+1), audienceAt: min(month.AddDate(0, 1, 0).Unix()-1, d.reportingUnix)}
		for _, metric := range metricNames() {
			raw, err := d.observations(ctx, tx, metric, o)
			if err != nil {
				return err
			}
			for _, v := range raw {
				status := "complete"
				if month.Equal(monthStart(d.reportingUnix)) || v.reason != "eligible" || !d.cohortCovered(coverage, metric, o) || v.from < d.cutoff() || v.until > d.reportingUnix+1 {
					status = "partial"
				}
				for _, source := range queryFor(metric).sources {
					if !covered(coverage, source, v.from, v.until) {
						status = "partial"
					}
				}
				for _, dimension := range dimensions {
					segment := lookup.at(v.subject, dimension, v.at)
					segmentCoverage := status
					if dimension != "all" && !covered(coverage, "demographics", v.from, v.until) {
						segmentCoverage = "partial"
					}
					n, den := v.numerator, v.denominator
					if v.reason != "eligible" {
						n = 0
						den = 0
					}
					id := derivedID(month.Format("2006-01") + "/" + metric + "/" + v.variant + "/" + dimension + "/" + v.subject)
					c := Contribution{ID: id, SubjectID: v.subject, Month: month.Format("2006-01"), Metric: metric, Dimension: dimension, Segment: segment, Numerator: n, Denominator: den, QueryVersion: 1, Permission: true, Coverage: segmentCoverage, SurveyVersion: v.variant, ObservationStatus: v.reason}
					if err := insertContribution(ctx, stmt, c); err != nil {
						return err
					}
				}
			}
		}
	}
	return d.rebuildMetrics(ctx, tx)
}

func insertContribution(ctx context.Context, stmt *sql.Stmt, c Contribution) error {
	_, err := stmt.ExecContext(ctx, c.ID, c.SubjectID, c.Month, c.Metric, c.Dimension, c.Segment, c.Numerator, c.Denominator, c.Coverage, c.SurveyVersion, c.ObservationStatus)
	if err != nil {
		return ErrDataset
	}
	return nil
}

type monthlyGroup struct {
	month, metric, variant, dimension, segment      string
	numerator, denominator, nSubjects, participants int
	partial                                         bool
}

func (d *Dataset) rebuildMetrics(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT month,metric,survey_version,dimension,segment,sum(numerator),sum(denominator),sum(CASE WHEN numerator>0 THEN 1 ELSE 0 END),count(*),max(CASE WHEN coverage='partial' OR observation_status<>'eligible' THEN 1 ELSE 0 END)
 FROM monthly_contributions GROUP BY month,metric,survey_version,dimension,segment ORDER BY month,metric,survey_version,dimension,segment`)
	if err != nil {
		return ErrDataset
	}
	var groups []monthlyGroup
	for rows.Next() {
		var g monthlyGroup
		var partial int
		if rows.Scan(&g.month, &g.metric, &g.variant, &g.dimension, &g.segment, &g.numerator, &g.denominator, &g.nSubjects, &g.participants, &partial) != nil {
			_ = rows.Close()
			return ErrDataset
		}
		g.partial = partial != 0
		groups = append(groups, g)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return ErrDataset
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM monthly_metrics"); err != nil {
		return ErrDataset
	}
	unsafe := map[string]bool{}
	unsafeFamilies := map[string]bool{}
	audienceTotals := map[string]int{}
	groupKey := func(g monthlyGroup) string { return g.month + "/" + g.metric + "/" + g.variant + "/" + g.dimension }
	for _, g := range groups {
		if g.metric == "audience" {
			audienceTotals[groupKey(g)] += g.denominator
		}
	}
	for i := range groups {
		g := &groups[i]
		if g.metric == "audience" {
			g.denominator = audienceTotals[groupKey(*g)]
		}
		if g.participants < 10 || (g.nSubjects > 0 && g.nSubjects < 10) || (g.denominator-g.nSubjects > 0 && g.denominator-g.nSubjects < 10) {
			unsafe[groupKey(*g)] = true
			if family := scoreFamily(g.metric); family != "" {
				unsafeFamilies[g.month+"/"+family+"/"+g.variant+"/"+g.dimension] = true
			}
		}
	}
	for _, g := range groups {
		status := "complete"
		if g.partial {
			status = "partial"
		}
		if g.denominator == 0 {
			status = "unavailable"
		}
		var n, den any = g.numerator, g.denominator
		if unsafe[groupKey(g)] || unsafeFamilies[g.month+"/"+scoreFamily(g.metric)+"/"+g.variant+"/"+g.dimension] || g.nSubjects == 0 {
			status = "suppressed"
			n = nil
			den = nil
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO monthly_metrics VALUES(?,?,?,?,?,?,?,?,1,?)", g.month, g.metric, g.variant, g.dimension, g.segment, n, den, status, d.reportingUnix); err != nil {
			return ErrDataset
		}
	}
	return nil
}

func (d *Dataset) DeleteSubject(ctx context.Context, subject string) error {
	if !opaqueID(subject) {
		return ErrInput
	}
	if d.readOnly {
		return ErrDataset
	}
	if err := d.Validate(ctx); err != nil {
		return err
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrDataset
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, "INSERT INTO subject_tombstones VALUES(?,?) ON CONFLICT(subject_id) DO NOTHING", subject, d.reportingUnix); err != nil {
		return ErrDataset
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM subjects WHERE id=?", subject); err != nil {
		return ErrDataset
	}
	if err = d.rebuildMetrics(ctx, tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return ErrDataset
	}
	return nil
}

func (d *Dataset) Expire(ctx context.Context) error {
	if d.readOnly {
		return ErrDataset
	}
	if err := d.Validate(ctx); err != nil {
		return err
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrDataset
	}
	defer func() { _ = tx.Rollback() }()
	changed := false
	apply := func(query string, args ...any) error {
		result, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return ErrDataset
		}
		n, err := result.RowsAffected()
		if err != nil {
			return ErrDataset
		}
		changed = changed || n > 0
		return nil
	}
	for _, v := range []struct{ table, column string }{{"activity_events", "occurred_unix"}, {"satisfaction_responses", "occurred_unix"}, {"communication_events", "occurred_unix"}, {"satisfaction_invitations", "invited_unix"}} {
		if err = apply("DELETE FROM "+v.table+" WHERE "+v.column+"<? OR "+v.column+">?", d.cutoff(), d.reportingUnix); err != nil {
			return ErrDataset
		}
	}
	for _, table := range []string{"demographic_answers", "feature_eligibility", "consent_periods"} {
		if err = apply("DELETE FROM "+table+" WHERE end_unix<=?", d.cutoff()); err != nil {
			return ErrDataset
		}
	}
	if err = apply("DELETE FROM monthly_contributions WHERE month<? OR month>?", d.firstMonth(), monthStart(d.reportingUnix).Format("2006-01")); err != nil {
		return ErrDataset
	}
	if err = apply(`UPDATE subjects SET eligible_from=NULL,cohort_start=NULL WHERE eligible_from<?
 AND NOT EXISTS(SELECT 1 FROM consent_spans c WHERE c.subject_id=subjects.id AND c.purpose='product' AND c.start_unix<=? AND c.end_unix>?)
 AND NOT EXISTS(SELECT 1 FROM activity_events a WHERE a.subject_id=subjects.id)
 AND NOT EXISTS(SELECT 1 FROM satisfaction_invitations a WHERE a.subject_id=subjects.id)
 AND NOT EXISTS(SELECT 1 FROM satisfaction_responses a WHERE a.subject_id=subjects.id)
 AND NOT EXISTS(SELECT 1 FROM communication_events a WHERE a.subject_id=subjects.id)`, d.cutoff(), d.reportingUnix, d.reportingUnix); err != nil {
		return ErrDataset
	}
	if changed {
		if err = d.refresh(ctx, tx); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return ErrDataset
	}
	return nil
}

type MonthlyMetric struct {
	Month string `json:"month"`
	Metric
}
type MonthlyReport struct {
	ExtractID     string          `json:"extract_id"`
	SchemaVersion int             `json:"schema_version"`
	StartMonth    string          `json:"start_month"`
	EndMonth      string          `json:"end_month"`
	Environment   string          `json:"environment"`
	ReportingUnix int64           `json:"reporting_unix"`
	Dimension     string          `json:"dimension"`
	QueryVersion  int             `json:"query_version"`
	Rows          []MonthlyMetric `json:"metrics"`
}

func (d *Dataset) Monthly(ctx context.Context, dimension string) (MonthlyReport, error) {
	if !slices.Contains(dimensions, dimension) {
		return MonthlyReport{}, ErrInput
	}
	if err := d.Validate(ctx); err != nil {
		return MonthlyReport{}, err
	}
	rows, err := d.db.QueryContext(ctx, "SELECT month,metric,survey_version,segment,numerator,denominator,status FROM monthly_metrics WHERE dimension=? ORDER BY month,metric,survey_version,segment", dimension)
	if err != nil {
		return MonthlyReport{}, ErrDataset
	}
	defer func() { _ = rows.Close() }()
	r := MonthlyReport{ExtractID: d.extractID, SchemaVersion: 1, StartMonth: d.firstMonth(), EndMonth: monthStart(d.reportingUnix).Format("2006-01"), Environment: "synthetic", ReportingUnix: d.reportingUnix, Dimension: dimension, QueryVersion: 1, Rows: []MonthlyMetric{}}
	seen := map[string]bool{}
	for rows.Next() {
		var m MonthlyMetric
		var n, den sql.NullInt64
		if rows.Scan(&m.Month, &m.Name, &m.SurveyVersion, &m.Segment, &n, &den, &m.Status) != nil {
			return MonthlyReport{}, ErrDataset
		}
		if n.Valid {
			v := int(n.Int64)
			m.Value = &v
		}
		if den.Valid {
			v := int(den.Int64)
			m.Denominator = &v
		}
		if n.Valid && den.Valid && den.Int64 > 0 && m.Status == "complete" && m.Name != "activity" && m.Name != "opens" {
			v := float64(n.Int64) / float64(den.Int64)
			m.Rate = &v
		}
		seen[m.Month+"/"+m.Name] = true
		r.Rows = append(r.Rows, m)
	}
	if rows.Err() != nil {
		return MonthlyReport{}, ErrDataset
	}
	for month := monthStart(d.reportingUnix).AddDate(0, -12, 0); !month.After(monthStart(d.reportingUnix)); month = month.AddDate(0, 1, 0) {
		for _, metric := range metricNames() {
			m := month.Format("2006-01")
			if !seen[m+"/"+metric] {
				r.Rows = append(r.Rows, MonthlyMetric{Month: m, Metric: Metric{Name: metric, Segment: "all", Status: "unavailable"}})
			}
		}
	}
	sort.Slice(r.Rows, func(i, j int) bool {
		a, b := r.Rows[i], r.Rows[j]
		return a.Month+"/"+a.Name+"/"+a.SurveyVersion+"/"+a.Segment < b.Month+"/"+b.Name+"/"+b.SurveyVersion+"/"+b.Segment
	})
	return r, nil
}
