package analyticsdataset

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
)

//go:embed queries.sql
var QueriesSQL string

type ReportOptions struct {
	Start, End int64
	Dimension  string
	audienceAt int64
}
type Metric struct {
	Name           string   `json:"metric"`
	Segment        string   `json:"segment"`
	SurveyVersion  string   `json:"survey_version,omitempty"`
	Status         string   `json:"status"`
	Value          *int     `json:"numerator"`
	Denominator    *int     `json:"denominator"`
	Rate           *float64 `json:"rate"`
	Pending        *int     `json:"pending"`
	Excluded       *int     `json:"excluded"`
	ResponseEvents *int     `json:"response_events,omitempty"`
	smallCell      bool
}
type Report struct {
	Environment   string               `json:"environment"`
	ExtractID     string               `json:"extract_id"`
	ReportingUnix int64                `json:"reporting_unix"`
	Start         int64                `json:"start_unix"`
	End           int64                `json:"end_unix"`
	Dimension     string               `json:"dimension"`
	SchemaVersion int                  `json:"schema_version"`
	QueryVersion  int                  `json:"query_version"`
	TargetFit     *float64             `json:"target_fit"`
	Coverage      []SourceAvailability `json:"source_coverage"`
	Rows          []Metric             `json:"metrics"`
}
type SourceAvailability struct {
	Source string `json:"source"`
	Status string `json:"status"`
	Start  int64  `json:"start_unix"`
	End    int64  `json:"end_unix"`
}
type observation struct {
	subject                string
	numerator, denominator int
	at                     int64
	reason, variant        string
	from, until            int64
	events                 int
}
type queryDefinition struct {
	name, feature, channel, status, scope string
	score                                 int
	sources                               []string
	cohortSources                         []string
}

func queryFor(metric string) queryDefinition {
	q := queryDefinition{name: metric, sources: []string{"activity"}}
	switch {
	case strings.HasPrefix(metric, "adoption_"):
		q.name = "adoption"
		q.feature = strings.TrimPrefix(metric, "adoption_")
		q.sources = append(q.sources, "eligibility")
	case metric == "activation" || metric == "retention":
		q.sources = append(q.sources, "eligibility")
	case strings.HasPrefix(metric, "satisfaction_"):
		parts := strings.Split(metric, "_")
		q.scope = parts[1]
		if strings.HasSuffix(metric, "_response_rate") {
			q.name = "satisfaction_rate"
			q.sources = []string{"satisfaction_invitations", "satisfaction_responses"}
		} else {
			q.name = "satisfaction_score"
			q.score, _ = strconv.Atoi(parts[len(parts)-1])
			q.sources = []string{"satisfaction_responses"}
		}
	case strings.HasPrefix(metric, "communication_"):
		parts := strings.Split(metric, "_")
		q.channel = parts[1]
		q.status = parts[2]
		q.sources = []string{"communications"}
		if q.status == "conversion" {
			q.name = "communication_conversion"
			q.sources = append(q.sources, "activity")
		} else {
			q.name = "communication_status"
		}
	case metric == "audience":
		q.sources = []string{"eligibility"}
	}
	q.cohortSources = append([]string(nil), q.sources...)
	switch q.name {
	case "satisfaction_rate":
		q.cohortSources = []string{"satisfaction_invitations"}
		q.sources = []string{"satisfaction_responses"}
	case "activation":
		q.cohortSources = []string{"eligibility"}
	case "communication_conversion":
		q.cohortSources = []string{"communications"}
	case "audience":
		q.cohortSources = nil
	}
	return q
}

// A complete cohort needs its whole selection period, including days with no observed rows.
func (d *Dataset) cohortCovered(coverage []Coverage, metric string, o ReportOptions) bool {
	sources := queryFor(metric).cohortSources
	if len(sources) == 0 {
		return true
	}
	if o.Start < d.cutoff() || o.End > d.reportingUnix+1 {
		return false
	}
	for _, source := range sources {
		if !covered(coverage, source, o.Start, o.End) {
			return false
		}
	}
	return true
}

func namedQueries() map[string]string {
	queries := map[string]string{}
	for _, part := range strings.Split(QueriesSQL, "-- name: ")[1:] {
		name, query, _ := strings.Cut(part, "\n")
		queries[strings.TrimSpace(name)] = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(query), ";"))
	}
	return queries
}

const parameters = `WITH p AS (SELECT ? AS lo,? AS hi,? AS asof,? AS cutoff,? AS feature,? AS channel,? AS status,? AS scope,? AS score),spans AS MATERIALIZED (SELECT * FROM consent_spans) `

func (d *Dataset) observations(ctx context.Context, tx *sql.Tx, metric string, o ReportOptions) ([]observation, error) {
	q := queryFor(metric)
	query, ok := namedQueries()[q.name]
	if !ok {
		return nil, ErrDataset
	}
	asof := d.reportingUnix
	if metric == "audience" && o.audienceAt != 0 {
		asof = o.audienceAt
	}
	rows, err := tx.QueryContext(ctx, parameters+query, o.Start, o.End, asof, d.cutoff(), q.feature, q.channel, q.status, q.scope, q.score)
	if err != nil {
		return nil, fmt.Errorf("analytics query %s failed: %w", q.name, err)
	}
	defer func() { _ = rows.Close() }()
	var out []observation
	for rows.Next() {
		var v observation
		if err := rows.Scan(&v.subject, &v.numerator, &v.denominator, &v.at, &v.reason, &v.variant, &v.from, &v.until, &v.events); err != nil {
			return nil, ErrDataset
		}
		out = append(out, v)
	}
	if rows.Err() != nil {
		return nil, ErrDataset
	}
	return out, nil
}

func loadCoverage(ctx context.Context, tx *sql.Tx) ([]Coverage, error) {
	rows, err := tx.QueryContext(ctx, "SELECT source,start_unix,end_unix,status FROM source_coverage ORDER BY source,start_unix,end_unix")
	if err != nil {
		return nil, ErrDataset
	}
	defer func() { _ = rows.Close() }()
	var out []Coverage
	for rows.Next() {
		var c Coverage
		if rows.Scan(&c.Source, &c.Start, &c.End, &c.Status) != nil {
			return nil, ErrDataset
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func covered(coverage []Coverage, source string, start, end int64) bool {
	if end <= start {
		return false
	}
	position := start
	for _, c := range coverage {
		if c.Source != source || c.End <= start || c.Start >= end {
			continue
		}
		if c.Status != "complete" {
			return false
		}
		if c.Start > position {
			return false
		}
		if c.End > position {
			position = c.End
		}
	}
	return position >= end
}

type segmentation struct {
	answers     map[string][]Demographic
	permissions map[string][][2]int64
}

func loadSegmentation(ctx context.Context, tx *sql.Tx) (segmentation, error) {
	lookup := segmentation{answers: map[string][]Demographic{}, permissions: map[string][][2]int64{}}
	rows, err := tx.QueryContext(ctx, "SELECT subject_id,start_unix,end_unix FROM consent_spans WHERE purpose='demographics' ORDER BY subject_id,start_unix")
	if err != nil {
		return lookup, ErrDataset
	}
	for rows.Next() {
		var subject string
		var start, end int64
		if rows.Scan(&subject, &start, &end) != nil {
			_ = rows.Close()
			return lookup, ErrDataset
		}
		lookup.permissions[subject] = append(lookup.permissions[subject], [2]int64{start, end})
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return lookup, ErrDataset
	}
	rows, err = tx.QueryContext(ctx, "SELECT subject_id,start_unix,end_unix,age_band,country,language,household_band FROM demographic_answers ORDER BY subject_id,start_unix")
	if err != nil {
		return lookup, ErrDataset
	}
	for rows.Next() {
		var answer Demographic
		var end sql.NullInt64
		if rows.Scan(&answer.SubjectID, &answer.Start, &end, &answer.AgeBand, &answer.Country, &answer.Language, &answer.HouseholdBand) != nil {
			_ = rows.Close()
			return lookup, ErrDataset
		}
		if end.Valid {
			value := end.Int64
			answer.End = &value
		}
		lookup.answers[answer.SubjectID] = append(lookup.answers[answer.SubjectID], answer)
	}
	err = rows.Err()
	_ = rows.Close()
	return lookup, err
}

func (lookup segmentation) at(subject, dimension string, at int64) string {
	if dimension == "all" {
		return "all"
	}
	permitted := false
	for _, span := range lookup.permissions[subject] {
		if span[0] <= at && at < span[1] {
			permitted = true
			break
		}
	}
	if !permitted {
		return "unknown"
	}
	for _, answer := range lookup.answers[subject] {
		if answer.Start > at || (answer.End != nil && at >= *answer.End) {
			continue
		}
		switch dimension {
		case "age_band":
			return answer.AgeBand
		case "country":
			return answer.Country
		case "language":
			return answer.Language
		case "household_band":
			return answer.HouseholdBand
		}
	}
	return "unknown"
}

type metricGroup struct {
	variant, segment  string
	eligible          map[string]observation
	pending, excluded map[string]bool
	partial           bool
}

func newGroup(variant, segment string) *metricGroup {
	return &metricGroup{variant: variant, segment: segment, eligible: map[string]observation{}, pending: map[string]bool{}, excluded: map[string]bool{}}
}

func (d *Dataset) groupObservations(metric string, o ReportOptions, raw []observation, coverage []Coverage, lookup segmentation) ([]Metric, error) {
	groups := map[string]*metricGroup{}
	sources := queryFor(metric).sources
	if o.Dimension != "all" {
		sources = append(sources, "demographics")
	}
	for _, v := range raw {
		segment := lookup.at(v.subject, o.Dimension, v.at)
		k := v.variant + "/" + segment
		g := groups[k]
		if g == nil {
			g = newGroup(v.variant, segment)
			groups[k] = g
		}
		if v.reason == "pending" {
			g.pending[v.subject] = true
			g.partial = true
			continue
		}
		if v.reason != "eligible" {
			g.excluded[v.subject] = true
			g.partial = true
			continue
		}
		ok := d.cohortCovered(coverage, metric, o) && v.from >= d.cutoff() && v.until <= d.reportingUnix+1
		for _, source := range sources {
			ok = ok && covered(coverage, source, v.from, v.until)
		}
		if !ok {
			g.partial = true
		}
		previous := g.eligible[v.subject]
		v.numerator = max(v.numerator, previous.numerator)
		v.denominator = max(v.denominator, previous.denominator)
		v.events = max(v.events, previous.events)
		g.eligible[v.subject] = v
	}
	if len(groups) == 0 {
		return []Metric{{Name: metric, Segment: "all", Status: "unavailable"}}, nil
	}
	var result []Metric
	unsafeVariants := map[string]bool{}
	totalAudience := 0
	if metric == "audience" {
		seen := map[string]bool{}
		for _, g := range groups {
			for s := range g.eligible {
				seen[s] = true
			}
		}
		totalAudience = len(seen)
	}
	for _, g := range groups {
		n, den, events, nSubjects := 0, 0, 0, 0
		for _, v := range g.eligible {
			n += v.numerator
			den += v.denominator
			events += v.events
			if v.numerator > 0 {
				nSubjects++
			}
		}
		if metric == "audience" {
			den = totalAudience
		}
		pending, excluded := len(g.pending), len(g.excluded)
		m := Metric{Name: metric, Segment: g.segment, SurveyVersion: g.variant, Status: "complete", Value: &n, Denominator: &den, Pending: &pending, Excluded: &excluded}
		if g.partial {
			m.Status = "partial"
		}
		if den == 0 {
			m.Status = "unavailable"
			m.Value = nil
			m.Denominator = nil
		}
		if den > 0 && !g.partial && metric != "activity" && metric != "opens" {
			rate := float64(n) / float64(den)
			m.Rate = &rate
		}
		if scoreFamily(metric) != "" {
			m.ResponseEvents = &events
		}
		if (len(g.eligible) > 0 && len(g.eligible) < 10) || (nSubjects > 0 && nSubjects < 10) || (den-nSubjects > 0 && den-nSubjects < 10) || (pending > 0 && pending < 10) || (excluded > 0 && excluded < 10) {
			unsafeVariants[g.variant] = true
		}
		if nSubjects == 0 && den > 0 {
			m.Status = "suppressed"
			clearCounts(&m)
		}
		result = append(result, m)
	}
	for i := range result {
		if unsafeVariants[result[i].SurveyVersion] {
			result[i].smallCell = true
			result[i].Status = "suppressed"
			clearCounts(&result[i])
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].SurveyVersion != result[j].SurveyVersion {
			return result[i].SurveyVersion < result[j].SurveyVersion
		}
		return result[i].Segment < result[j].Segment
	})
	return result, nil
}
func clearCounts(m *Metric) {
	m.Value = nil
	m.Denominator = nil
	m.Rate = nil
	m.Pending = nil
	m.Excluded = nil
	m.ResponseEvents = nil
}

func (d *Dataset) Report(ctx context.Context, o ReportOptions) (Report, error) {
	if err := d.Validate(ctx); err != nil {
		return Report{}, err
	}
	if !validTime(o.Start) || !validTime(o.End) || o.End <= o.Start || o.End > d.reportingUnix+1 || !slices.Contains(dimensions, o.Dimension) {
		return Report{}, ErrInput
	}
	tx, err := d.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Report{}, ErrDataset
	}
	defer func() { _ = tx.Rollback() }()
	coverage, err := loadCoverage(ctx, tx)
	if err != nil {
		return Report{}, err
	}
	lookup, err := loadSegmentation(ctx, tx)
	if err != nil {
		return Report{}, err
	}
	r := Report{Environment: "synthetic", ExtractID: d.extractID, ReportingUnix: d.reportingUnix, Start: o.Start, End: o.End, Dimension: o.Dimension, SchemaVersion: 1, QueryVersion: 1, Rows: []Metric{}}
	for _, source := range sources {
		status := "unavailable"
		for _, c := range coverage {
			if c.Source == source && c.Start < o.End && c.End > o.Start && c.Status != "unavailable" {
				status = "partial"
			}
		}
		if o.Start >= d.cutoff() && covered(coverage, source, o.Start, o.End) {
			status = "complete"
		}
		r.Coverage = append(r.Coverage, SourceAvailability{Source: source, Status: status, Start: o.Start, End: o.End})
	}
	for _, metric := range metricNames() {
		raw, err := d.observations(ctx, tx, metric, o)
		if err != nil {
			return Report{}, err
		}
		rows, err := d.groupObservations(metric, o, raw, coverage, lookup)
		if err != nil {
			return Report{}, err
		}
		r.Rows = append(r.Rows, rows...)
	}
	unsafeFamilies := map[string]bool{}
	for _, m := range r.Rows {
		if family := scoreFamily(m.Name); family != "" && m.smallCell {
			unsafeFamilies[family+"/"+m.SurveyVersion] = true
		}
	}
	for i := range r.Rows {
		m := &r.Rows[i]
		if unsafeFamilies[scoreFamily(m.Name)+"/"+m.SurveyVersion] {
			m.Status = "suppressed"
			clearCounts(m)
		}
	}
	return r, nil
}

func scoreFamily(metric string) string {
	if !strings.HasPrefix(metric, "satisfaction_") || !strings.Contains(metric, "_score_") {
		return ""
	}
	i := strings.LastIndex(metric, "_")
	return metric[:i]
}
