package analyticsdataset

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"golang.org/x/text/language"
)

var idPattern = regexp.MustCompile(`^[a-z]_[0-9a-f]{16}$`)
var versionPattern = regexp.MustCompile(`^v[1-9][0-9]{0,2}$`)

func opaqueID(s string) bool { return idPattern.MatchString(s) }
func country(s string) bool {
	if s == "unknown" || s == "prefer_not_to_answer" {
		return true
	}
	if len(s) != 2 || strings.ToUpper(s) != s {
		return false
	}
	r, err := language.ParseRegion(s)
	return err == nil && r.IsCountry()
}

// Decode rejects unknown fields, duplicate object keys and trailing JSON values.
func Decode(r io.Reader) (Batch, error) {
	data, err := io.ReadAll(io.LimitReader(r, 32*1024*1024+1))
	if err != nil || len(data) > 32*1024*1024 {
		return Batch{}, ErrInput
	}
	check := json.NewDecoder(bytes.NewReader(data))
	if uniqueJSON(check, 0, reflect.TypeOf(Batch{})) != nil {
		return Batch{}, ErrInput
	}
	if _, err = check.Token(); err != io.EOF {
		return Batch{}, ErrInput
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var b Batch
	if dec.Decode(&b) != nil {
		return Batch{}, ErrInput
	}
	return b, nil
}

func uniqueJSON(d *json.Decoder, depth int, schema reflect.Type) error {
	if depth > 32 {
		return ErrInput
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		if depth == 0 {
			return ErrInput
		}
		return nil
	}
	for schema.Kind() == reflect.Pointer {
		schema = schema.Elem()
	}
	if (delim == '{' && schema.Kind() != reflect.Struct) || (delim == '[' && schema.Kind() != reflect.Slice) {
		return ErrInput
	}
	keys := map[string]bool{}
	for d.More() {
		child := schema
		if delim == '{' {
			k, err := d.Token()
			if err != nil {
				return err
			}
			s, ok := k.(string)
			if !ok || keys[s] {
				return ErrInput
			}
			keys[s] = true
			found := false
			for i := 0; i < schema.NumField(); i++ {
				field := schema.Field(i)
				if field.Tag.Get("json") == s {
					child = field.Type
					found = true
					break
				}
			}
			if !found {
				return ErrInput
			}
		} else {
			child = schema.Elem()
		}
		if err := uniqueJSON(d, depth+1, child); err != nil {
			return err
		}
	}
	end, err := d.Token()
	if err != nil {
		return err
	}
	if (delim == '{' && end != json.Delim('}')) || (delim == '[' && end != json.Delim(']')) {
		return ErrInput
	}
	return nil
}

type importTable struct {
	name string
	rows any
}

func (b Batch) tables() []importTable {
	return []importTable{
		{"subjects", b.Subjects}, {"consent_periods", b.Consent}, {"feature_eligibility", b.Eligibility},
		{"communication_events", b.Communications}, {"activity_events", b.Activity},
		{"satisfaction_invitations", b.Invitations}, {"satisfaction_responses", b.Responses},
		{"demographic_answers", b.Demographics}, {"source_coverage", b.Coverage}, {"monthly_contributions", b.Contributions},
	}
}

func (d *Dataset) validateBatch(b Batch) error {
	if b.Version != 1 || b.Environment != "synthetic" || b.ExtractID != d.extractID || b.ReportingUnix != d.reportingUnix || !opaqueID(b.ID) {
		return ErrInput
	}
	for _, table := range b.tables() {
		rows := reflect.ValueOf(table.rows)
		for i := 0; i < rows.Len(); i++ {
			row := rows.Index(i)
			for j := 0; j < row.NumField(); j++ {
				field := row.Type().Field(j).Tag.Get("db")
				v := row.Field(j)
				if v.Kind() == reflect.Pointer {
					if v.IsNil() {
						continue
					}
					v = v.Elem()
				}
				if field == "id" || strings.HasSuffix(field, "_id") {
					if !opaqueID(v.String()) {
						return ErrInput
					}
				}
				if strings.HasSuffix(field, "_version") && field != "query_version" {
					optionalSurveyVersion := table.name == "monthly_contributions" && field == "survey_version" && v.String() == ""
					if !optionalSurveyVersion && !versionPattern.MatchString(v.String()) {
						return ErrInput
					}
				}
				if strings.HasSuffix(field, "_unix") || field == "cohort_start" || field == "eligible_from" {
					if !validTime(v.Int()) {
						return ErrInput
					}
				}
			}
		}
	}
	inWindow := func(t int64) bool { return t >= d.cutoff() && t <= d.reportingUnix }
	for _, s := range b.Subjects {
		if s.CohortStart < s.EligibleFrom || s.CohortStart > d.reportingUnix {
			return ErrInput
		}
	}
	for _, a := range b.Activity {
		if !inWindow(a.Occurred) || !slices.Contains(eventNames, a.Name) || !slices.Contains([]string{"web", "ios", "android"}, a.Platform) || !slices.Contains([]string{"free", "premium"}, a.Plan) {
			return ErrInput
		}
	}
	for _, r := range b.Responses {
		if !inWindow(r.Occurred) || r.Score < 1 || r.Score > 5 || !slices.Contains([]string{"app", "recipe"}, r.Scope) {
			return ErrInput
		}
	}
	for _, i := range b.Invitations {
		if !inWindow(i.Invited) || i.Start < i.Invited || i.End <= i.Start || !slices.Contains([]string{"app", "recipe"}, i.Scope) {
			return ErrInput
		}
	}
	for _, c := range b.Communications {
		if !inWindow(c.Occurred) || !slices.Contains([]string{"push", "email"}, c.Channel) || !slices.Contains([]string{"planning", "product_update", "satisfaction"}, c.Category) || !slices.Contains(communicationStatuses, c.Status) {
			return ErrInput
		}
	}
	for _, c := range b.Consent {
		if !slices.Contains([]string{"product", "demographics"}, c.Purpose) || c.Start > d.reportingUnix || (c.End != nil && *c.End <= c.Start) {
			return ErrInput
		}
	}
	for _, e := range b.Eligibility {
		if !slices.Contains(coreActions, e.Feature) || e.Start > d.reportingUnix || (e.End != nil && *e.End <= e.Start) {
			return ErrInput
		}
	}
	for _, v := range b.Demographics {
		if !slices.Contains(ageBands, v.AgeBand) || !slices.Contains(householdBands, v.HouseholdBand) || !slices.Contains(languages, v.Language) || !country(v.Country) || v.Start > d.reportingUnix || (v.End != nil && *v.End <= v.Start) {
			return ErrInput
		}
	}
	for _, v := range b.Coverage {
		if !slices.Contains(sources, v.Source) || !slices.Contains([]string{"complete", "partial", "unavailable"}, v.Status) || v.End <= v.Start || v.End > d.reportingUnix+1 {
			return ErrInput
		}
	}
	for _, v := range b.Contributions {
		month, err := time.Parse("2006-01", v.Month)
		if err != nil || v.Month < d.firstMonth() || !month.Before(monthStart(d.reportingUnix)) || month.Unix() >= d.cutoff() || !slices.Contains(metricNames(), v.Metric) || !validSegment(v.Dimension, v.Segment) || v.Numerator < 0 || v.Numerator > 1 || v.Denominator < 0 || v.Denominator > 1 || v.Numerator > v.Denominator || !v.Permission || v.QueryVersion != 1 || !slices.Contains([]string{"complete", "partial"}, v.Coverage) || !slices.Contains([]string{"eligible", "pending", "excluded", "incomplete"}, v.ObservationStatus) {
			return ErrInput
		}
		if (v.ObservationStatus == "eligible") != (v.Denominator == 1) {
			return ErrInput
		}
		if v.ObservationStatus == "eligible" && slices.Contains([]string{"activity", "opens", "audience"}, v.Metric) && v.Numerator != 1 {
			return ErrInput
		}
		if strings.HasPrefix(v.Metric, "satisfaction_") {
			if !versionPattern.MatchString(v.SurveyVersion) {
				return ErrInput
			}
		} else if v.SurveyVersion != "" {
			return ErrInput
		}
	}
	return nil
}

func validSegment(dimension, segment string) bool {
	switch dimension {
	case "all":
		return segment == "all"
	case "age_band":
		return slices.Contains(ageBands, segment)
	case "household_band":
		return slices.Contains(householdBands, segment)
	case "language":
		return slices.Contains(languages, segment)
	case "country":
		return country(segment)
	}
	return false
}

func metricNames() []string {
	metrics := []string{"activity", "opens", "activation", "retention", "audience"}
	for _, a := range coreActions {
		metrics = append(metrics, "adoption_"+a)
	}
	for _, scope := range []string{"app", "recipe"} {
		metrics = append(metrics, "satisfaction_"+scope+"_response_rate")
		for score := 1; score <= 5; score++ {
			metrics = append(metrics, fmt.Sprintf("satisfaction_%s_score_%d", scope, score))
		}
	}
	for _, channel := range []string{"push", "email"} {
		for _, status := range communicationStatuses {
			metrics = append(metrics, "communication_"+channel+"_"+status)
		}
		metrics = append(metrics, "communication_"+channel+"_conversion")
	}
	return metrics
}

func batchDigest(b Batch) string {
	// Sort each typed collection by immutable row identity before hashing.
	value := reflect.ValueOf(&b).Elem()
	for i := 0; i < value.NumField(); i++ {
		field := value.Field(i)
		if field.Kind() != reflect.Slice {
			continue
		}
		copy := reflect.MakeSlice(field.Type(), field.Len(), field.Len())
		reflect.Copy(copy, field)
		sort.Slice(copy.Interface(), func(a, b int) bool { return copy.Index(a).Field(0).String() < copy.Index(b).Field(0).String() })
		field.Set(copy)
	}
	data, _ := json.Marshal(b)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func (d *Dataset) Import(ctx context.Context, b Batch) (bool, error) {
	if d.readOnly {
		return false, ErrDataset
	}
	if err := d.Validate(ctx); err != nil {
		return false, err
	}
	b.Contributions = append([]Contribution(nil), b.Contributions...)
	for i := range b.Contributions {
		if b.Contributions[i].ObservationStatus == "" {
			b.Contributions[i].ObservationStatus = "eligible"
		}
	}
	if err := d.validateBatch(b); err != nil {
		return false, err
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return false, ErrDataset
	}
	defer func() { _ = tx.Rollback() }()
	// Erasure is checked even for a previously committed batch replay.
	for _, table := range b.tables() {
		rows := reflect.ValueOf(table.rows)
		for i := 0; i < rows.Len(); i++ {
			row := rows.Index(i)
			subject := row.FieldByName("SubjectID")
			if table.name == "subjects" {
				subject = row.FieldByName("ID")
			}
			if !subject.IsValid() {
				continue
			}
			var n int
			if tx.QueryRowContext(ctx, "SELECT count(*) FROM subject_tombstones WHERE subject_id=?", subject.String()).Scan(&n) != nil {
				return false, ErrDataset
			}
			if n != 0 {
				return false, ErrConflict
			}
		}
	}
	digest := batchDigest(b)
	var old string
	err = tx.QueryRowContext(ctx, "SELECT digest FROM import_batches WHERE id=?", b.ID).Scan(&old)
	if err == nil {
		if old != digest {
			return false, ErrConflict
		}
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, ErrDataset
	}
	n := 0
	for _, table := range b.tables() {
		rows := reflect.ValueOf(table.rows)
		if err := insertRows(ctx, tx, table.name, rows); err != nil {
			return false, err
		}
		n += rows.Len()
	}
	if err := validateRelations(ctx, tx); err != nil {
		return false, err
	}
	if err := d.refresh(ctx, tx); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO import_batches VALUES(?,?,?,?)", b.ID, digest, d.reportingUnix, n); err != nil {
		return false, ErrConflict
	}
	if err = tx.Commit(); err != nil {
		return false, ErrDataset
	}
	return true, nil
}

func insertRows(ctx context.Context, tx *sql.Tx, table string, rows reflect.Value) error {
	if rows.Len() == 0 {
		return nil
	}
	rowType := rows.Index(0).Type()
	columns := []string{}
	for j := 0; j < rowType.NumField(); j++ {
		columns = append(columns, rowType.Field(j).Tag.Get("db"))
	}
	columns = append(columns, "_digest")
	updates := "id=excluded.id"
	if table == "subjects" {
		updates += ",eligible_from=coalesce(subjects.eligible_from,excluded.eligible_from),cohort_start=coalesce(subjects.cohort_start,excluded.cohort_start),notice_version=excluded.notice_version"
	}
	query := "INSERT INTO " + table + " (" + strings.Join(columns, ",") + ") VALUES (" + strings.TrimSuffix(strings.Repeat("?,", len(columns)), ",") + ") ON CONFLICT(id) DO UPDATE SET " + updates + " WHERE " + table + "._digest=excluded._digest"
	stmt, err := tx.PrepareContext(ctx, query)
	if err != nil {
		return ErrDataset
	}
	defer func() { _ = stmt.Close() }()
	for i := 0; i < rows.Len(); i++ {
		row := rows.Index(i)
		args := make([]any, 0, len(columns))
		for j := 0; j < row.NumField(); j++ {
			args = append(args, row.Field(j).Interface())
		}
		encoded, _ := json.Marshal(row.Interface())
		args = append(args, fmt.Sprintf("%x", sha256.Sum256(encoded)))
		result, err := stmt.ExecContext(ctx, args...)
		if err != nil {
			return ErrConflict
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return ErrDataset
		}
		if changed != 1 {
			return ErrConflict
		}
	}
	return nil
}

func validateRelations(ctx context.Context, tx *sql.Tx) error {
	checks := []string{
		`SELECT count(*) FROM monthly_contributions m JOIN subjects s ON s.id=m.subject_id WHERE s.cohort_start>=unixepoch(date(m.month||'-01','+1 month'))`,
		`SELECT count(*) FROM subjects s WHERE s.cohort_start<>max(s.eligible_from,coalesce((SELECT min(start_unix) FROM consent_periods c WHERE c.subject_id=s.id AND c.purpose='product'),s.eligible_from))`,
		`SELECT count(*) FROM activity_events a JOIN subjects s ON s.id=a.subject_id WHERE a.occurred_unix<s.eligible_from OR NOT EXISTS(SELECT 1 FROM consent_spans c WHERE c.subject_id=a.subject_id AND c.purpose='product' AND c.start_unix<=a.occurred_unix AND c.end_unix>a.occurred_unix)`,
		`SELECT count(*) FROM activity_events a WHERE a.name NOT IN ('app_active','account_verified','premium_started','premium_ended') AND NOT EXISTS(SELECT 1 FROM feature_eligibility f WHERE f.subject_id=a.subject_id AND f.feature=a.name AND f.start_unix<=a.occurred_unix AND coalesce(f.end_unix,253402300799)>a.occurred_unix)`,
		`SELECT count(*) FROM activity_events a WHERE a.communication_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM communication_events c WHERE c.subject_id=a.subject_id AND c.occurrence_id=a.communication_id)`,
		`SELECT count(*) FROM communication_events a JOIN communication_events b ON a.occurrence_id=b.occurrence_id WHERE a.subject_id<>b.subject_id OR a.channel<>b.channel OR a.category<>b.category`,
		`SELECT count(*) FROM satisfaction_responses r JOIN satisfaction_invitations i ON i.id=r.invitation_id WHERE r.subject_id<>i.subject_id OR r.scope<>i.scope OR r.survey_version<>i.survey_version OR r.occurred_unix<i.start_unix OR r.occurred_unix>=i.end_unix`,
		`SELECT count(*) FROM demographic_answers a JOIN demographic_answers b ON a.subject_id=b.subject_id AND a.id<b.id WHERE a.start_unix<coalesce(b.end_unix,253402300799) AND b.start_unix<coalesce(a.end_unix,253402300799)`,
		`SELECT count(*) FROM demographic_answers a WHERE NOT EXISTS(SELECT 1 FROM consent_spans c WHERE c.subject_id=a.subject_id AND c.purpose='demographics' AND c.start_unix<=a.start_unix AND c.end_unix>=coalesce(a.end_unix,253402300799))`,
	}
	for _, table := range []string{"satisfaction_responses", "communication_events", "satisfaction_invitations"} {
		column := "occurred_unix"
		if table == "satisfaction_invitations" {
			column = "invited_unix"
		}
		checks = append(checks, "SELECT count(*) FROM "+table+" a JOIN subjects s ON s.id=a.subject_id WHERE a."+column+"<s.eligible_from OR NOT EXISTS(SELECT 1 FROM consent_spans c WHERE c.subject_id=a.subject_id AND c.purpose='product' AND c.start_unix<=a."+column+" AND c.end_unix>a."+column+")")
	}
	for _, query := range checks {
		if strings.Contains(query, "consent_spans") {
			query = "WITH spans AS MATERIALIZED (SELECT * FROM consent_spans) " + strings.ReplaceAll(query, "consent_spans", "spans")
		}
		var n int
		if tx.QueryRowContext(ctx, query).Scan(&n) != nil {
			return ErrDataset
		}
		if n != 0 {
			return ErrInput
		}
	}
	return nil
}
