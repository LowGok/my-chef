PRAGMA application_id=1296253262;
CREATE TABLE analytics_schema (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1), version INTEGER NOT NULL CHECK(version=1),
 checksum TEXT NOT NULL, environment TEXT NOT NULL CHECK(environment='synthetic'),
 extract_id TEXT NOT NULL, reporting_unix INTEGER NOT NULL
) STRICT;
CREATE TABLE import_batches (id TEXT PRIMARY KEY, digest TEXT NOT NULL, imported_unix INTEGER NOT NULL, row_count INTEGER NOT NULL) STRICT;
CREATE TABLE subjects (
 id TEXT PRIMARY KEY, eligible_from INTEGER, cohort_start INTEGER,
 notice_version TEXT NOT NULL, _digest TEXT NOT NULL, CHECK(cohort_start>=eligible_from)
) STRICT;
CREATE TABLE consent_periods (
 id TEXT PRIMARY KEY, subject_id TEXT NOT NULL REFERENCES subjects(id) ON DELETE CASCADE,
 purpose TEXT NOT NULL CHECK(purpose IN ('product','demographics')),
 start_unix INTEGER NOT NULL, end_unix INTEGER, notice_version TEXT NOT NULL, _digest TEXT NOT NULL,
 CHECK(end_unix IS NULL OR end_unix>start_unix)
) STRICT;
CREATE TABLE feature_eligibility (
 id TEXT PRIMARY KEY, subject_id TEXT NOT NULL REFERENCES subjects(id) ON DELETE CASCADE,
 feature TEXT NOT NULL CHECK(feature IN ('preferences_saved','meal_plan_saved','shopping_list_edited','favorite_saved','feedback_submitted','personal_recipe_saved','recipe_import_saved','reminder_enabled')),
 start_unix INTEGER NOT NULL, end_unix INTEGER, _digest TEXT NOT NULL,
 CHECK(end_unix IS NULL OR end_unix>start_unix)
) STRICT;
CREATE TABLE communication_events (
 id TEXT PRIMARY KEY, subject_id TEXT NOT NULL REFERENCES subjects(id) ON DELETE CASCADE,
 occurrence_id TEXT NOT NULL, channel TEXT NOT NULL CHECK(channel IN ('push','email')),
 category TEXT NOT NULL CHECK(category IN ('planning','product_update','satisfaction')),
 status TEXT NOT NULL CHECK(status IN ('queued','accepted','delivered','bounced','failed','tapped','unsubscribed','complained')),
 occurred_unix INTEGER NOT NULL, _digest TEXT NOT NULL
) STRICT;
CREATE TABLE activity_events (
 id TEXT PRIMARY KEY, subject_id TEXT NOT NULL REFERENCES subjects(id) ON DELETE CASCADE,
 occurred_unix INTEGER NOT NULL,
 name TEXT NOT NULL CHECK(name IN ('preferences_saved','meal_plan_saved','shopping_list_edited','favorite_saved','feedback_submitted','personal_recipe_saved','recipe_import_saved','reminder_enabled','app_active','account_verified','premium_started','premium_ended')),
 platform TEXT NOT NULL CHECK(platform IN ('web','ios','android')),
 plan TEXT NOT NULL CHECK(plan IN ('free','premium')), communication_id TEXT, _digest TEXT NOT NULL
) STRICT;
CREATE TABLE satisfaction_invitations (
 id TEXT PRIMARY KEY, subject_id TEXT NOT NULL REFERENCES subjects(id) ON DELETE CASCADE,
 scope TEXT NOT NULL CHECK(scope IN ('app','recipe')), survey_version TEXT NOT NULL,
 invited_unix INTEGER NOT NULL, start_unix INTEGER NOT NULL, end_unix INTEGER NOT NULL,
 _digest TEXT NOT NULL, CHECK(start_unix>=invited_unix AND end_unix>start_unix)
) STRICT;
CREATE TABLE satisfaction_responses (
 id TEXT PRIMARY KEY, subject_id TEXT NOT NULL REFERENCES subjects(id) ON DELETE CASCADE,
 scope TEXT NOT NULL CHECK(scope IN ('app','recipe')), survey_version TEXT NOT NULL,
 occurred_unix INTEGER NOT NULL, score INTEGER NOT NULL CHECK(score BETWEEN 1 AND 5),
 invitation_id TEXT REFERENCES satisfaction_invitations(id) ON DELETE SET NULL, _digest TEXT NOT NULL
) STRICT;
CREATE TABLE demographic_answers (
 id TEXT PRIMARY KEY, subject_id TEXT NOT NULL REFERENCES subjects(id) ON DELETE CASCADE,
 start_unix INTEGER NOT NULL, end_unix INTEGER,
 age_band TEXT NOT NULL CHECK(age_band IN ('18_24','25_34','35_44','45_54','55_64','65_plus','unknown','prefer_not_to_answer')),
 country TEXT NOT NULL, language TEXT NOT NULL CHECK(language IN ('en','fr','nl','other','unknown','prefer_not_to_answer')),
 household_band TEXT NOT NULL CHECK(household_band IN ('1','2','3_4','5_plus','unknown','prefer_not_to_answer')),
 _digest TEXT NOT NULL, CHECK(end_unix IS NULL OR end_unix>start_unix)
) STRICT;
CREATE TABLE source_coverage (
 id TEXT PRIMARY KEY, source TEXT NOT NULL CHECK(source IN ('activity','eligibility','satisfaction_invitations','satisfaction_responses','communications','demographics')),
 start_unix INTEGER NOT NULL, end_unix INTEGER NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('complete','partial','unavailable')), _digest TEXT NOT NULL,
 CHECK(end_unix>start_unix)
) STRICT;
CREATE TABLE monthly_contributions (
 id TEXT PRIMARY KEY, subject_id TEXT NOT NULL REFERENCES subjects(id) ON DELETE CASCADE,
 month TEXT NOT NULL, metric TEXT NOT NULL, dimension TEXT NOT NULL,
 segment TEXT NOT NULL, numerator INTEGER NOT NULL CHECK(numerator BETWEEN 0 AND 1),
 denominator INTEGER NOT NULL CHECK(denominator BETWEEN 0 AND 1), query_version INTEGER NOT NULL CHECK(query_version=1),
 permission_validated INTEGER NOT NULL CHECK(permission_validated=1),
 coverage TEXT NOT NULL CHECK(coverage IN ('complete','partial')), survey_version TEXT NOT NULL,
 observation_status TEXT NOT NULL CHECK(observation_status IN ('eligible','pending','excluded','incomplete')), _digest TEXT NOT NULL,
 origin TEXT NOT NULL DEFAULT 'imported' CHECK(origin IN ('imported','derived')),
 UNIQUE(subject_id,month,metric,survey_version,dimension), CHECK(numerator<=denominator),
 CHECK(denominator=CASE WHEN observation_status='eligible' THEN 1 ELSE 0 END),
 CHECK(metric NOT IN ('activity','opens','audience') OR observation_status<>'eligible' OR numerator=1)
) STRICT;
CREATE TABLE monthly_metrics (
 month TEXT NOT NULL, metric TEXT NOT NULL, survey_version TEXT NOT NULL, dimension TEXT NOT NULL, segment TEXT NOT NULL,
 numerator INTEGER, denominator INTEGER, status TEXT NOT NULL,
 query_version INTEGER NOT NULL, refreshed_unix INTEGER NOT NULL,
 PRIMARY KEY(month,metric,survey_version,dimension,segment)
) STRICT;
CREATE TABLE subject_tombstones (subject_id TEXT PRIMARY KEY, erased_unix INTEGER NOT NULL) STRICT;
CREATE INDEX activity_subject_time ON activity_events(subject_id,occurred_unix,name);
CREATE INDEX activity_time ON activity_events(occurred_unix,name,subject_id);
CREATE INDEX consent_subject_time ON consent_periods(subject_id,purpose,start_unix,end_unix);
CREATE INDEX eligibility_subject ON feature_eligibility(subject_id,feature,start_unix,end_unix);
CREATE INDEX communication_occurrence ON communication_events(subject_id,occurrence_id,occurred_unix);
CREATE INDEX responses_subject_time ON satisfaction_responses(subject_id,scope,survey_version,occurred_unix);
CREATE INDEX contributions_month ON monthly_contributions(month,metric,dimension,segment);
CREATE VIEW consent_spans AS
WITH ordered AS (
 SELECT *, max(coalesce(end_unix,253402300799)) OVER(PARTITION BY subject_id,purpose ORDER BY start_unix,id ROWS BETWEEN UNBOUNDED PRECEDING AND 1 PRECEDING) AS previous_end
 FROM consent_periods
), grouped AS (
 SELECT *,sum(CASE WHEN previous_end IS NULL OR start_unix>previous_end THEN 1 ELSE 0 END) OVER(PARTITION BY subject_id,purpose ORDER BY start_unix,id) AS span
 FROM ordered
)
SELECT subject_id,purpose,min(start_unix) AS start_unix,max(coalesce(end_unix,253402300799)) AS end_unix
FROM grouped GROUP BY subject_id,purpose,span;
CREATE VIEW core_activity AS SELECT * FROM activity_events WHERE name IN
 ('preferences_saved','meal_plan_saved','shopping_list_edited','favorite_saved','feedback_submitted','personal_recipe_saved','recipe_import_saved','reminder_enabled');
CREATE VIEW attributed_actions AS
SELECT a.id,a.subject_id,a.occurred_unix,tap.occurrence_id,tap.id AS tap_event_id,
 tap.occurred_unix AS tap_unix,tap.channel AS tap_channel
FROM core_activity a LEFT JOIN communication_events tap ON tap.id=
 (SELECT c.id FROM communication_events c WHERE c.subject_id=a.subject_id
  AND c.status='tapped' AND c.occurred_unix<=a.occurred_unix AND c.occurred_unix>=a.occurred_unix-86400
  ORDER BY c.occurred_unix DESC,c.id DESC LIMIT 1);
