package analyticsdataset

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

type ProductionPeriod struct {
	ID, Subject                         string
	Start                               int64
	End                                 *int64
	Notice, Basis, BasisVersion, Source string
	Revision                            int64
}
type ProductionCapability struct {
	Feature    string
	Start, End int64
	Status     string
}
type ProductionAuthority struct {
	SubjectID, Month, BasisVersion string
	Revision                       int64
}
type ProductionSnapshot struct {
	Authorities                      []ProductionAuthority
	Capabilities                     []ProductionCapability
	Binding, ExtractID               string
	Reporting, Generation, Watermark int64
	Periods                          []ProductionPeriod
	Activity                         []Activity
	Eligibility                      []Eligibility
	Coverage                         []Coverage
	Contributions                    []Contribution
}

func productionSchema() string {
	s := strings.Replace(SchemaSQL, "application_id=1296253262", "application_id=1296253265", 1)
	s = strings.Replace(s, "CHECK(version=1)", "CHECK(version=2)", 1)
	s = strings.Replace(s, "CHECK(environment='synthetic')", "CHECK(environment='production')", 1)
	s = strings.Replace(s, "extract_id TEXT NOT NULL, reporting_unix INTEGER NOT NULL", "extract_id TEXT NOT NULL, reporting_unix INTEGER NOT NULL, binding TEXT NOT NULL, maintenance_unix INTEGER NOT NULL, privacy_revision INTEGER NOT NULL, watermark INTEGER NOT NULL, content_revision INTEGER NOT NULL", 1)
	start := strings.Index(s, "CREATE TABLE consent_periods (")
	end := strings.Index(s[start:], "CREATE TABLE feature_eligibility") + start
	s = s[:start] + `CREATE TABLE processing_periods (
 id TEXT PRIMARY KEY, subject_id TEXT NOT NULL REFERENCES subjects(id) ON DELETE CASCADE,
 start_unix INTEGER NOT NULL, end_unix INTEGER, notice_version TEXT NOT NULL, _digest TEXT NOT NULL,
 basis TEXT NOT NULL CHECK(basis='legitimate_interest'), basis_version TEXT NOT NULL,
 enrollment_source TEXT NOT NULL CHECK(enrollment_source IN('registration_default','explicit_enable')),
 enrollment_revision INTEGER NOT NULL CHECK(enrollment_revision>0),
 CHECK(end_unix IS NULL OR end_unix>start_unix)
 ) STRICT;
 CREATE VIEW consent_periods AS SELECT id,subject_id,'product' AS purpose,start_unix,end_unix,notice_version,_digest FROM processing_periods;
 ` + s[end:]
	s = strings.Replace(s, "ON consent_periods(subject_id,purpose,start_unix,end_unix)", "ON processing_periods(subject_id,start_unix,end_unix)", 1)
	s += `CREATE TABLE feature_source_coverage(feature TEXT NOT NULL,start_unix INTEGER NOT NULL,end_unix INTEGER NOT NULL,status TEXT NOT NULL CHECK(status IN('complete','partial','unavailable')),PRIMARY KEY(feature,start_unix),CHECK(end_unix>start_unix)) STRICT;
CREATE TABLE analytics_migrations(version INTEGER PRIMARY KEY CHECK(version=2),checksum TEXT NOT NULL) STRICT;
 CREATE TABLE contribution_provenance(subject_id TEXT NOT NULL REFERENCES subjects(id) ON DELETE CASCADE,month TEXT NOT NULL,basis_version TEXT NOT NULL,enrollment_revision INTEGER NOT NULL,PRIMARY KEY(subject_id,month,basis_version,enrollment_revision)) STRICT;`
	return s
}
func productionDigest() string {
	h := sha256.Sum256([]byte(productionSchema()))
	return fmt.Sprintf("%x", h)
}

var productionFingerprint = sync.OnceValues(func() (string, error) {
	db, e := sql.Open("sqlite3", ":memory:")
	if e != nil {
		return "", e
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	if _, e = db.Exec(productionSchema()); e != nil {
		return "", e
	}
	return physicalFingerprint(context.Background(), db)
})

func productionID(v, p string) bool {
	if len(v) != len(p)+33 || !strings.HasPrefix(v, p+"_") {
		return false
	}
	b, e := hex.DecodeString(v[len(p)+1:])
	return e == nil && len(b) == 16 && hex.EncodeToString(b) == v[len(p)+1:]
}
func validateProductionInput(in ProductionSnapshot) error {
	if in.Binding == "" || !productionID(in.ExtractID, "ext") || !validTime(in.Reporting) || in.Generation < 0 {
		return ErrInput
	}
	periods := map[string]ProductionPeriod{}
	ids := map[string]bool{}
	for _, p := range in.Periods {
		if !productionID(p.ID, "per") || !productionID(p.Subject, "sub") || p.Start > in.Reporting || p.Start <= 0 || p.End != nil && *p.End <= p.Start || p.Basis != "legitimate_interest" || p.BasisVersion == "" || p.Notice == "" || !slices.Contains([]string{"registration_default", "explicit_enable"}, p.Source) || p.Revision <= 0 || ids[p.ID] {
			return ErrInput
		}
		ids[p.ID] = true
		periods[p.ID] = p
	}
	for _, a := range in.Activity {
		if !productionID(a.ID, "evt") || !productionID(a.SubjectID, "sub") || a.Occurred < in.Reporting-90*86400 || a.Occurred > in.Reporting || a.CommunicationID != nil || !slices.Contains([]string{"preferences_saved", "meal_plan_saved", "shopping_list_edited", "favorite_saved", "feedback_submitted", "personal_recipe_saved", "recipe_import_saved"}, a.Name) || !slices.Contains([]string{"web", "ios", "android"}, a.Platform) || !slices.Contains([]string{"free", "premium"}, a.Plan) {
			return ErrInput
		}
		authorized := false
		for _, p := range periods {
			if p.Subject == a.SubjectID && p.Start <= a.Occurred && (p.End == nil || *p.End > a.Occurred) {
				authorized = true
			}
		}
		if !authorized {
			return ErrInput
		}
	}
	for _, e := range in.Eligibility {
		if !productionID(e.ID, "eli") || !productionID(e.SubjectID, "sub") || !slices.Contains([]string{"preferences_saved", "meal_plan_saved", "shopping_list_edited", "favorite_saved", "feedback_submitted", "personal_recipe_saved", "recipe_import_saved"}, e.Feature) || e.Start > in.Reporting || e.End != nil && *e.End <= e.Start {
			return ErrInput
		}
	}
	for _, c := range in.Coverage {
		if !slices.Contains(sources, c.Source) || !slices.Contains([]string{"complete", "partial", "unavailable"}, c.Status) || c.End <= c.Start || c.End > in.Reporting+1 {
			return ErrInput
		}
	}
	for _, c := range in.Capabilities {
		if !slices.Contains(coreActions, c.Feature) || c.End <= c.Start || c.End > in.Reporting+1 || !slices.Contains([]string{"complete", "partial", "unavailable"}, c.Status) {
			return ErrInput
		}
	}
	for _, c := range in.Contributions {
		if !productionID(c.SubjectID, "sub") || c.Month < monthStart(in.Reporting).AddDate(0, -12, 0).Format("2006-01") || c.Month >= ProductionHistoryBefore(in.Reporting) || c.Dimension != "all" || c.Segment != "all" || !slices.Contains(metricNames(), c.Metric) || c.Numerator < 0 || c.Numerator > c.Denominator || c.Denominator < 0 || c.Denominator > 1 || !c.Permission || !slices.Contains([]string{"complete", "partial"}, c.Coverage) || !slices.Contains([]string{"eligible", "pending", "excluded", "incomplete"}, c.ObservationStatus) {
			return ErrInput
		}
	}
	expected := map[ProductionAuthority]bool{}
	for _, c := range in.Contributions {
		first, err := time.Parse("2006-01", c.Month)
		if err != nil {
			return ErrInput
		}
		found := false
		for _, p := range in.Periods {
			end := in.Reporting + 1
			if p.End != nil {
				end = *p.End
			}
			if p.Subject == c.SubjectID && p.Start < first.AddDate(0, 1, 0).Unix() && end > first.Unix() {
				expected[ProductionAuthority{c.SubjectID, c.Month, p.BasisVersion, p.Revision}] = true
				found = true
			}
		}
		if !found {
			return ErrInput
		}
	}
	if len(in.Authorities) > 0 {
		observed := map[ProductionAuthority]bool{}
		for _, a := range in.Authorities {
			if !expected[a] {
				return ErrInput
			}
			observed[a] = true
		}
		if len(observed) != len(expected) {
			return ErrInput
		}
	}

	return nil
}
func CreateProduction(ctx context.Context, path string, in ProductionSnapshot) (*Dataset, error) {
	if e := validateProductionInput(in); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if e != nil {
		return nil, ErrDataset
	}
	if e = f.Close(); e != nil {
		return nil, e
	}
	d, e := connect(path, false)
	if e != nil {
		_ = os.Remove(path)
		return nil, e
	}
	d.production = true
	d.binding = in.Binding
	d.extractID = in.ExtractID
	d.reportingUnix = in.Reporting
	d.maintenance = in.Reporting
	d.privacyRevision = in.Generation
	ok := false
	defer func() {
		if !ok {
			_ = d.Close()
			_ = os.Remove(path)
		}
	}()
	tx, e := d.db.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer func() { _ = tx.Rollback() }()
	if _, e = tx.ExecContext(ctx, productionSchema()); e != nil {
		return nil, e
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO analytics_schema VALUES(1,2,?,'production',?,?,?,?,?,?,0)", productionDigest(), in.ExtractID, in.Reporting, in.Binding, in.Reporting, in.Generation, in.Watermark); e != nil {
		return nil, e
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO analytics_migrations VALUES(2,?)", productionDigest()); e != nil {
		return nil, e
	}
	for _, p := range in.Periods {
		if _, e = tx.ExecContext(ctx, `INSERT INTO subjects VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET eligible_from=min(eligible_from,excluded.eligible_from),cohort_start=min(cohort_start,excluded.cohort_start)`, p.Subject, p.Start, p.Start, p.Notice, "production"); e != nil {
			return nil, e
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO processing_periods VALUES(?,?,?,?,?,?,?,?,?,?)", p.ID, p.Subject, p.Start, p.End, p.Notice, "production", p.Basis, p.BasisVersion, p.Source, p.Revision); e != nil {
			return nil, e
		}
	}
	for _, a := range in.Activity {
		if _, e = tx.ExecContext(ctx, "INSERT INTO activity_events VALUES(?,?,?,?,?,?,NULL,?)", a.ID, a.SubjectID, a.Occurred, a.Name, a.Platform, a.Plan, "production"); e != nil {
			return nil, e
		}
	}
	for _, f := range in.Eligibility {
		if _, e = tx.ExecContext(ctx, "INSERT INTO feature_eligibility VALUES(?,?,?,?,?,?)", f.ID, f.SubjectID, f.Feature, f.Start, f.End, "production"); e != nil {
			return nil, e
		}
	}
	for i, c := range in.Coverage {
		if _, e = tx.ExecContext(ctx, "INSERT INTO source_coverage VALUES(?,?,?,?,?,?)", fmt.Sprintf("cov_%032x", i), c.Source, c.Start, c.End, c.Status, "production"); e != nil {
			return nil, e
		}
	}
	for _, c := range in.Capabilities {
		if _, e = tx.ExecContext(ctx, "INSERT INTO feature_source_coverage VALUES(?,?,?,?)", c.Feature, c.Start, c.End, c.Status); e != nil {
			return nil, e
		}
	}
	for _, c := range in.Contributions {
		if _, e = tx.ExecContext(ctx, `INSERT INTO monthly_contributions VALUES(?,?,?,?,?,?,?,?,1,1,?,?,?,?, 'imported')`, c.ID, c.SubjectID, c.Month, c.Metric, c.Dimension, c.Segment, c.Numerator, c.Denominator, c.Coverage, c.SurveyVersion, c.ObservationStatus, "production"); e != nil {
			return nil, e
		}
	}
	if e = d.refresh(ctx, tx); e != nil {
		return nil, e
	}
	// Contribution provenance is processing authority, never a fabricated consent receipt.
	if _, e = tx.ExecContext(ctx, `INSERT INTO contribution_provenance SELECT c.subject_id,c.month,p.basis_version,p.enrollment_revision FROM monthly_contributions c JOIN processing_periods p ON p.subject_id=c.subject_id AND p.start_unix<CAST(strftime('%s',c.month||'-01','+1 month') AS INTEGER) AND coalesce(p.end_unix,?)>CAST(strftime('%s',c.month||'-01') AS INTEGER) GROUP BY c.subject_id,c.month,p.basis_version,p.enrollment_revision`, in.Reporting+1); e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	if e = d.Validate(ctx); e != nil {
		return nil, e
	}
	ok = true
	return d, nil
}
func OpenProduction(ctx context.Context, path, binding string, readOnly bool) (*Dataset, error) {
	d, e := connect(path, true)
	if e != nil {
		return nil, e
	}
	d.production = true
	d.binding = binding
	if e = d.Validate(ctx); e != nil {
		_ = d.Close()
		return nil, e
	}
	if readOnly {
		return d, nil
	}
	_ = d.Close()
	d, e = connect(path, false)
	if e != nil {
		return nil, e
	}
	d.production = true
	d.binding = binding
	if e = d.Validate(ctx); e != nil {
		_ = d.Close()
		return nil, e
	}
	return d, nil
}
func (d *Dataset) validateProduction(ctx context.Context) error {
	var app, version, rows int
	var environment, digest, binding, id string
	var r, n, g int64
	if d.db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&app) != nil || app != 1296253265 {
		return ErrDataset
	}
	if d.db.QueryRowContext(ctx, "SELECT count(*) FROM analytics_schema").Scan(&rows) != nil || rows != 1 {
		return ErrDataset
	}
	if e := d.db.QueryRowContext(ctx, "SELECT version,checksum,environment,extract_id,reporting_unix,binding,maintenance_unix,privacy_revision,content_revision FROM analytics_schema WHERE singleton=1").Scan(&version, &digest, &environment, &id, &r, &binding, &n, &g, &d.contentRevision); e != nil || version != 2 || digest != productionDigest() || environment != "production" || binding != d.binding || !productionID(id, "ext") || !validTime(r) || n < r || g < 0 {
		return ErrDataset
	}
	if d.extractID != "" && (d.extractID != id || d.reportingUnix != r) {
		return ErrDataset
	}
	d.extractID = id
	d.reportingUnix = r
	d.maintenance = n
	d.privacyRevision = g
	a, e := productionFingerprint()
	if e != nil {
		return ErrDataset
	}
	b, e := physicalFingerprint(ctx, d.db)
	if e != nil || a != b {
		return ErrDataset
	}
	var checksum, integrity string
	if d.db.QueryRowContext(ctx, "SELECT checksum FROM analytics_migrations WHERE version=2").Scan(&checksum) != nil || checksum != digest {
		return ErrDataset
	}
	if d.db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity) != nil || integrity != "ok" {
		return ErrDataset
	}
	fk, e := d.db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if e != nil {
		return ErrDataset
	}
	defer func() { _ = fk.Close() }()
	if fk.Next() {
		return ErrDataset
	}
	return fk.Err()
}
func (d *Dataset) MaintainProduction(ctx context.Context, now, generation int64, revoked []string) error {
	if !d.production || d.readOnly {
		return ErrDataset
	}
	if e := d.Validate(ctx); e != nil {
		return e
	}
	if now < d.maintenance || generation < d.privacyRevision {
		return ErrInput
	}
	tx, e := d.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback() }()
	for _, id := range revoked {
		if !productionID(id, "sub") {
			return ErrInput
		}
		if _, e = tx.ExecContext(ctx, "INSERT OR IGNORE INTO subject_tombstones VALUES(?,?)", id, now); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "DELETE FROM subjects WHERE id=?", id); e != nil {
			return e
		}
	}
	cutoff := max(d.reportingUnix-90*86400, now-90*86400)
	first := monthStart(now).AddDate(0, -12, 0).Format("2006-01")
	if _, e = tx.ExecContext(ctx, "DELETE FROM activity_events WHERE occurred_unix<? OR occurred_unix>?", cutoff, d.reportingUnix); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, "DELETE FROM monthly_contributions WHERE month<?", first); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, "DELETE FROM contribution_provenance WHERE month<?", first); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, "DELETE FROM processing_periods WHERE end_unix IS NOT NULL AND end_unix<? AND NOT EXISTS(SELECT 1 FROM monthly_contributions c WHERE c.subject_id=processing_periods.subject_id AND c.month>=strftime('%Y-%m',processing_periods.start_unix,'unixepoch') AND c.month<=strftime('%Y-%m',processing_periods.end_unix-1,'unixepoch'))", cutoff); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, "DELETE FROM feature_eligibility WHERE end_unix<=?", cutoff); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, "DELETE FROM subjects WHERE NOT EXISTS(SELECT 1 FROM processing_periods p WHERE p.subject_id=subjects.id) AND NOT EXISTS(SELECT 1 FROM monthly_contributions c WHERE c.subject_id=subjects.id) AND NOT EXISTS(SELECT 1 FROM activity_events a WHERE a.subject_id=subjects.id)"); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, "UPDATE analytics_schema SET maintenance_unix=?,privacy_revision=?,content_revision=content_revision+1", now, generation); e != nil {
		return e
	}
	if e = d.rebuildMetrics(ctx, tx); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	d.maintenance = now
	d.privacyRevision = generation
	return d.ReclaimProduction(ctx)
}
func (d *Dataset) ReclaimProduction(ctx context.Context) error {
	if !d.production || d.readOnly {
		return ErrDataset
	}
	if _, e := d.db.ExecContext(ctx, "PRAGMA secure_delete=ON; PRAGMA temp_store=MEMORY; VACUUM;"); e != nil {
		return e
	}
	var busy, log, done int
	if e := d.db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &done); e != nil || busy != 0 {
		return ErrDataset
	}
	return nil
}
func (d *Dataset) ProductionContributions(ctx context.Context) ([]Contribution, error) {
	if !d.production {
		return nil, ErrDataset
	}
	rows, e := d.db.QueryContext(ctx, "SELECT id,subject_id,month,metric,dimension,segment,numerator,denominator,query_version,permission_validated,coverage,survey_version,observation_status FROM monthly_contributions WHERE dimension='all'")
	if e != nil {
		return nil, e
	}
	defer func() { _ = rows.Close() }()
	var out []Contribution
	for rows.Next() {
		var c Contribution
		if e = rows.Scan(&c.ID, &c.SubjectID, &c.Month, &c.Metric, &c.Dimension, &c.Segment, &c.Numerator, &c.Denominator, &c.QueryVersion, &c.Permission, &c.Coverage, &c.SurveyVersion, &c.ObservationStatus); e != nil {
			return nil, e
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ProductionHistoryBefore separates finalized months from months whose full
// observation window is still present in the ninety-day raw snapshot.
func ProductionHistoryBefore(reporting int64) string {
	cutoff := reporting - 90*86400
	start := monthStart(cutoff)
	if start.Unix() < cutoff {
		start = start.AddDate(0, 1, 0)
	}
	return start.Format("2006-01")
}
func (d *Dataset) ProductionReportingUnix() int64 { return d.reportingUnix }

func (d *Dataset) productionMetricCoverage(ctx context.Context, tx *sql.Tx, metric string, coverage []Coverage) ([]Coverage, error) {
	if !d.production {
		return coverage, nil
	}
	feature := strings.TrimPrefix(metric, "adoption_")
	if metric == "activation" || metric == "retention" || metric == "audience" {
		feature = "meal_plan_saved"
	}
	if !slices.Contains(coreActions, feature) {
		return coverage, nil
	}
	out := make([]Coverage, 0, len(coverage))
	for _, c := range coverage {
		if c.Source != "eligibility" {
			out = append(out, c)
		}
	}
	rows, e := tx.QueryContext(ctx, "SELECT start_unix,end_unix,status FROM feature_source_coverage WHERE feature=? ORDER BY start_unix,end_unix", feature)
	if e != nil {
		return nil, e
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		c := Coverage{Source: "eligibility"}
		if e = rows.Scan(&c.Start, &c.End, &c.Status); e != nil {
			return nil, e
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (d *Dataset) ProductionSubjects(ctx context.Context, includeTombstones bool) ([]string, error) {
	if !d.production {
		return nil, ErrDataset
	}
	query := "SELECT id FROM subjects"
	if includeTombstones {
		query += " UNION SELECT subject_id FROM subject_tombstones"
	}
	rows, e := d.db.QueryContext(ctx, query)
	if e != nil {
		return nil, e
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ProductionAuthorities returns the complete set of processing revisions that
// overlap each retained contribution month. Several revisions may be required.
func (d *Dataset) ProductionAuthorities(ctx context.Context) ([]ProductionAuthority, error) {
	if !d.production {
		return nil, ErrDataset
	}
	rows, e := d.db.QueryContext(ctx, "SELECT subject_id,month,basis_version,enrollment_revision FROM contribution_provenance ORDER BY subject_id,month,enrollment_revision,basis_version")
	if e != nil {
		return nil, e
	}
	defer func() { _ = rows.Close() }()
	var out []ProductionAuthority
	for rows.Next() {
		var a ProductionAuthority
		if e = rows.Scan(&a.SubjectID, &a.Month, &a.BasisVersion, &a.Revision); e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
