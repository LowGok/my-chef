// Package analyticsdataset implements the synthetic, frozen SQLite analytical extract.
package analyticsdataset

type Batch struct {
	Version        int             `json:"version"`
	Environment    string          `json:"environment"`
	ExtractID      string          `json:"extract_id"`
	ReportingUnix  int64           `json:"reporting_unix"`
	ID             string          `json:"batch_id"`
	Subjects       []Subject       `json:"subjects"`
	Consent        []Consent       `json:"consent"`
	Eligibility    []Eligibility   `json:"eligibility"`
	Activity       []Activity      `json:"activity"`
	Invitations    []Invitation    `json:"invitations"`
	Responses      []Response      `json:"responses"`
	Demographics   []Demographic   `json:"demographics"`
	Communications []Communication `json:"communications"`
	Coverage       []Coverage      `json:"coverage"`
	Contributions  []Contribution  `json:"contributions"`
}

type Subject struct {
	ID            string `json:"id" db:"id"`
	EligibleFrom  int64  `json:"eligible_from" db:"eligible_from"`
	CohortStart   int64  `json:"cohort_start" db:"cohort_start"`
	NoticeVersion string `json:"notice_version" db:"notice_version"`
}
type Consent struct {
	ID            string `json:"id" db:"id"`
	SubjectID     string `json:"subject_id" db:"subject_id"`
	Purpose       string `json:"purpose" db:"purpose"`
	Start         int64  `json:"start_unix" db:"start_unix"`
	End           *int64 `json:"end_unix" db:"end_unix"`
	NoticeVersion string `json:"notice_version" db:"notice_version"`
}
type Eligibility struct {
	ID        string `json:"id" db:"id"`
	SubjectID string `json:"subject_id" db:"subject_id"`
	Feature   string `json:"feature" db:"feature"`
	Start     int64  `json:"start_unix" db:"start_unix"`
	End       *int64 `json:"end_unix" db:"end_unix"`
}
type Activity struct {
	ID              string  `json:"id" db:"id"`
	SubjectID       string  `json:"subject_id" db:"subject_id"`
	Occurred        int64   `json:"occurred_unix" db:"occurred_unix"`
	Name            string  `json:"name" db:"name"`
	Platform        string  `json:"platform" db:"platform"`
	Plan            string  `json:"plan" db:"plan"`
	CommunicationID *string `json:"communication_id" db:"communication_id"`
}
type Invitation struct {
	ID            string `json:"id" db:"id"`
	SubjectID     string `json:"subject_id" db:"subject_id"`
	Scope         string `json:"scope" db:"scope"`
	SurveyVersion string `json:"survey_version" db:"survey_version"`
	Invited       int64  `json:"invited_unix" db:"invited_unix"`
	Start         int64  `json:"start_unix" db:"start_unix"`
	End           int64  `json:"end_unix" db:"end_unix"`
}
type Response struct {
	ID            string  `json:"id" db:"id"`
	SubjectID     string  `json:"subject_id" db:"subject_id"`
	Scope         string  `json:"scope" db:"scope"`
	SurveyVersion string  `json:"survey_version" db:"survey_version"`
	Occurred      int64   `json:"occurred_unix" db:"occurred_unix"`
	Score         int     `json:"score" db:"score"`
	InvitationID  *string `json:"invitation_id" db:"invitation_id"`
}
type Demographic struct {
	ID            string `json:"id" db:"id"`
	SubjectID     string `json:"subject_id" db:"subject_id"`
	Start         int64  `json:"start_unix" db:"start_unix"`
	End           *int64 `json:"end_unix" db:"end_unix"`
	AgeBand       string `json:"age_band" db:"age_band"`
	Country       string `json:"country" db:"country"`
	Language      string `json:"language" db:"language"`
	HouseholdBand string `json:"household_band" db:"household_band"`
}
type Communication struct {
	ID           string `json:"id" db:"id"`
	SubjectID    string `json:"subject_id" db:"subject_id"`
	OccurrenceID string `json:"occurrence_id" db:"occurrence_id"`
	Channel      string `json:"channel" db:"channel"`
	Category     string `json:"category" db:"category"`
	Status       string `json:"status" db:"status"`
	Occurred     int64  `json:"occurred_unix" db:"occurred_unix"`
}
type Coverage struct {
	ID     string `json:"id" db:"id"`
	Source string `json:"source" db:"source"`
	Start  int64  `json:"start_unix" db:"start_unix"`
	End    int64  `json:"end_unix" db:"end_unix"`
	Status string `json:"status" db:"status"`
}
type Contribution struct {
	ID                string `json:"id" db:"id"`
	SubjectID         string `json:"subject_id" db:"subject_id"`
	Month             string `json:"month" db:"month"`
	Metric            string `json:"metric" db:"metric"`
	Dimension         string `json:"dimension" db:"dimension"`
	Segment           string `json:"segment" db:"segment"`
	Numerator         int    `json:"numerator" db:"numerator"`
	Denominator       int    `json:"denominator" db:"denominator"`
	QueryVersion      int    `json:"query_version" db:"query_version"`
	Permission        bool   `json:"permission_validated" db:"permission_validated"`
	Coverage          string `json:"coverage" db:"coverage"`
	SurveyVersion     string `json:"survey_version" db:"survey_version"`
	ObservationStatus string `json:"observation_status" db:"observation_status"`
}

var coreActions = []string{"preferences_saved", "meal_plan_saved", "shopping_list_edited", "favorite_saved", "feedback_submitted", "personal_recipe_saved", "recipe_import_saved", "reminder_enabled"}
var eventNames = append(append([]string{}, coreActions...), "app_active", "account_verified", "premium_started", "premium_ended")
var sources = []string{"activity", "eligibility", "satisfaction_invitations", "satisfaction_responses", "communications", "demographics"}
var dimensions = []string{"all", "age_band", "country", "language", "household_band"}
var ageBands = []string{"18_24", "25_34", "35_44", "45_54", "55_64", "65_plus", "unknown", "prefer_not_to_answer"}
var householdBands = []string{"1", "2", "3_4", "5_plus", "unknown", "prefer_not_to_answer"}
var languages = []string{"en", "fr", "nl", "other", "unknown", "prefer_not_to_answer"}
var communicationStatuses = []string{"queued", "accepted", "delivered", "bounced", "failed", "tapped", "unsubscribed", "complained"}
