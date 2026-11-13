# Curated SQLite analytics dataset

MyChef's analytics command creates a private SQLite extract with a deterministic
population of 1,000 synthetic participants. Activity, planning, satisfaction,
push/email observations and optional demographic bands share opaque synthetic
keys. The application owns its operational schema separately.

## Reproduce the dataset

Run from the repository root with the Go toolchain declared in `go.mod`.

```sh
umask 077
mkdir -p .test-artifacts/analytics data/analytics
go build -trimpath -o .test-artifacts/analytics/mychef-analytics ./cmd/mychef-analytics
.test-artifacts/analytics/mychef-analytics fixture --db data/analytics/analytics.sqlite --manifest data/analytics/fixture.json
.test-artifacts/analytics/mychef-analytics validate --db data/analytics/analytics.sqlite
.test-artifacts/analytics/mychef-analytics report --db data/analytics/analytics.sqlite > data/analytics/weekly.json
.test-artifacts/analytics/mychef-analytics report --db data/analytics/analytics.sqlite --monthly > data/analytics/monthly.json
.test-artifacts/analytics/mychef-analytics report --db data/analytics/analytics.sqlite --dimension age_band > data/analytics/age-bands.json
```

Initialization requires a fresh destination. The default reporting timestamp is
`2026-11-13T00:00:00Z`. The default weekly report covers the preceding completed
UTC calendar week, `2026-11-02T00:00:00Z` through `2026-11-09T00:00:00Z` for this
extract. The hand-computed activation cohort example uses the earlier week.

```sh
.test-artifacts/analytics/mychef-analytics report --db data/analytics/analytics.sqlite --start 2026-10-26 --end 2026-11-02 --format text
```

That cohort contains 750 meaningfully active participants, 500 first-plan
activations among 500 observable new participants, 250 next-week planning returns
among those 500 participants, and 800 app survey respondents among 1,000 invited
participants. The fixture's country bands include groups of nine and ten to
exercise suppression. Older monthly activity is derived from the same simulated
participants before older event detail is discarded. Other unavailable historical
metrics carry an unavailable status.

Use `--as-of` with `fixture` to create another independently frozen extract.
`import` reads a synthetic JSON manifest into a fresh destination or merges it
atomically into a matching extract. Its extract ID, timestamp and schema must
match. Replaying the same batch is idempotent. Conflicting identities, unapproved
fields, invalid consent and expired events reject the complete import.

```sh
.test-artifacts/analytics/mychef-analytics import --db data/analytics/reproduced.sqlite --manifest data/analytics/fixture.json
.test-artifacts/analytics/mychef-analytics expire --db data/analytics/reproduced.sqlite
.test-artifacts/analytics/mychef-analytics delete-subject --db data/analytics/reproduced.sqlite --subject s_0000000000000001
```

The deletion command demonstrates withdrawal on the reproduced copy. Replaying
an input containing that erased subject is refused. Generated databases,
manifests and reports belong under the ignored `data/` directory. Keep their
directory private and restrict access to the owner/operator. Routine report
consumers receive the aggregate JSON or published monthly SQL output.

## Measurement and interpretation

Each weekly report records its source coverage, period, reporting date and
schema/query versions. The measurement definitions and lifecycle rules follow.
Rates with incomplete observation windows remain unavailable. Cohort-selection
coverage includes the entire requested period, including days without observed
rows. Invitation coverage spans the invitation-selection period. Response
coverage spans the applicable follow-up intervals. Activity and opens
are population counts with null rates. Suppressed cells
contain null counts and rates. A group needs at least ten distinct participants.
Complementary small groups and related satisfaction score bins are suppressed
together where other visible values would reveal the hidden count.

The eight completed core actions are `preferences_saved`, `meal_plan_saved`,
`shopping_list_edited`, `favorite_saved`, `feedback_submitted`,
`personal_recipe_saved`, `recipe_import_saved` and `reminder_enabled`.
`app_active` is reported as an opens observation. Activation requires a first
plan in the half-open seven-day interval after observable eligibility.
Planning retention requires another plan in the next UTC calendar week.
Eligibility intervals determine each feature's adoption denominator.

App and recipe satisfaction remain separate by survey version. The latest
response per participant/version supplies the score distribution. Response-event
counts also show repeats. Survey response rates count distinct eligible invitees
with a linked response. An unlinked response contributes to its score
distribution. An open invitation interval contributes to pending observations.

Communication stages describe observed statuses. Each completed action can be
attributed to the latest preceding observed tap within 24 hours, inclusive at
the 24-hour boundary. The greatest stable event ID resolves equal tap times. The attribution result
preserves the selected tap ID and timestamp. Repeated taps on the same
occurrence keep their selected reporting period.
Conversion measures association. Provider acceptance, delivery and taps have
separate counts. Email opens are absent from the registry.

Demographic activity groups use the effective answer at the earliest qualifying
action for that participant and metric. Audience composition uses the permitted
answer at the reporting cutoff. Unknown and declined answers have explicit
bands. Target fit remains unavailable until a target is supplied through an
accepted measurement change. Each report uses one demographic dimension.

## Frozen extracts and lifecycle

R is immutable for a database file. Event facts and invitation creation times
fall in `[R - 90*86400, R]`. Imports enforce this interval under every batch ID.
Thirteen monthly buckets include R's current calendar month and the preceding
twelve months. Monthly contributions retain coarse pseudonymous components for
that period. Current-month derived metrics carry a partial-period status.

Fully retained months derive contributions from their event facts. Closed months
beginning before the event cutoff accept finalized synthetic contributions with
query-version, coverage and permission evidence. Each month has one contribution
source. Missing historical inputs remain unavailable. Source coverage in raw
reports measures the requested event window.

`expire` reapplies the extract's frozen windows. It removes stale facts, expired
closed intervals and older monthly contributions. Subject metadata required
only by old monthly contributions is reduced to its opaque key and minimum
permission provenance when its current eligibility is unavailable. Deletion
removes that subject's facts and monthly contributions and recomputes aggregates
in one transaction. A tombstone prevents resurrection for the extract's lifetime.
Other participants' contributions preserve older report history.

Frozen synthetic extracts remain reproducible when opened later. Real collection
needs its own accepted consent, ingestion, snapshot expiry and backup/deletion
propagation design. The existing application analytics draft owns that integration.
This command accepts the synthetic environment.

## Schema and SQL

[Schema SQL](analytics-dataset-schema.sql) matches the embedded numbered initial
schema. [Published monthly queries](analytics-queries.sql) run directly against
the database and expose thresholded aggregate cells. The parameterized event
queries executed by the report command are in
[`internal/analyticsdataset/queries.sql`](../../internal/analyticsdataset/queries.sql).
Their per-subject intermediate rows are operator inputs to the reporting layer.
The command applies coverage, effective demographic joins and suppression before
publishing output.

The schema uses SQLite `STRICT` tables, foreign keys and explicit checks. The
application ID, version, schema checksum and complete physical schema are
verified before an operation. A read-only connection performs the first
inspection. Report connections use SQLite read-only mode. File permissions
provide the access boundary. SQLite views provide query interfaces.

## Data dictionary

Input JSON uses the typed fields in `internal/analyticsdataset/types.go`.
Unknown and duplicate object keys are rejected. IDs match
`[a-z]_[0-9a-f]{16}`. Notice and survey versions match `v[1-9][0-9]{0,2}`.
Timestamps use UTC Unix seconds between 2000-01-01 and 2101-01-01, exclusive
at the upper bound. Intervals are half-open. A null end denotes an open interval.
The fixture accepts reporting dates from 2002 onward to provide its older history.

Age bands are `18_24`, `25_34`, `35_44`, `45_54`, `55_64`, `65_plus`, `unknown`
and `prefer_not_to_answer`. Household bands are `1`, `2`, `3_4`, `5_plus`,
`unknown` and `prefer_not_to_answer`. Languages are `en`, `fr`, `nl`, `other`,
`unknown` and `prefer_not_to_answer`. Country uses ISO alpha-2 country codes or
the two missing-answer states. Product and demographic consent are separate.
Raw birthdates, sex, addresses, recipe or preference content, message content,
provider identifiers and free-form properties are excluded from the contract.

The table dictionary below includes internal ledger and publication columns.

### activity_events

One completed-action or admitted observation ID.

| Field | SQLite type | Required | Meaning |
| --- | --- | --- | --- |
| `id` | TEXT | yes | Immutable opaque record identity. Primary-key component. |
| `subject_id` | TEXT | yes | Opaque synthetic analytical subject key. |
| `occurred_unix` | INTEGER | yes | Observed event time within the extract event window. |
| `name` | TEXT | yes | Closed event registry including completed actions, app_active and account/subscription state events. |
| `platform` | TEXT | yes | web, ios or android. |
| `plan` | TEXT | yes | free or premium at event time. |
| `communication_id` | TEXT | no | Optional opaque occurrence binding. Attribution is computed from taps. |
| `_digest` | TEXT | yes | Canonical imported-row digest. Derived monthly rows use the internal derived marker. |

### analytics_schema

One singleton schema and extract identity.

| Field | SQLite type | Required | Meaning |
| --- | --- | --- | --- |
| `singleton` | INTEGER | yes | Singleton identity fixed to 1. Primary-key component. |
| `version` | INTEGER | yes | Schema version fixed to 1. |
| `checksum` | TEXT | yes | SHA-256 of the embedded schema SQL. |
| `environment` | TEXT | yes | The closed value synthetic. |
| `extract_id` | TEXT | yes | Immutable opaque extract identity. |
| `reporting_unix` | INTEGER | yes | Immutable UTC reporting reference R. |

### communication_events

One occurrence/status observation ID.

| Field | SQLite type | Required | Meaning |
| --- | --- | --- | --- |
| `id` | TEXT | yes | Immutable opaque record identity. Primary-key component. |
| `subject_id` | TEXT | yes | Opaque synthetic analytical subject key. |
| `occurrence_id` | TEXT | yes | Opaque communication occurrence shared by its status observations. |
| `channel` | TEXT | yes | push or email. |
| `category` | TEXT | yes | planning, product_update or satisfaction. |
| `status` | TEXT | yes | A closed status enum appropriate to the table, described below. |
| `occurred_unix` | INTEGER | yes | Observed event time within the extract event window. |
| `_digest` | TEXT | yes | Canonical imported-row digest. Derived monthly rows use the internal derived marker. |

Status values are `queued`, `accepted`, `delivered`, `bounced`, `failed`,
`tapped`, `unsubscribed` and `complained`.

### consent_periods

One subject/purpose/permission interval.

| Field | SQLite type | Required | Meaning |
| --- | --- | --- | --- |
| `id` | TEXT | yes | Immutable opaque record identity. Primary-key component. |
| `subject_id` | TEXT | yes | Opaque synthetic analytical subject key. |
| `purpose` | TEXT | yes | product or demographics. |
| `start_unix` | INTEGER | yes | Inclusive start of permission, eligibility, answer or observation coverage. |
| `end_unix` | INTEGER | no | Exclusive interval end. Null denotes an open interval. |
| `notice_version` | TEXT | yes | Versioned permission notice using the v1-style closed grammar. |
| `_digest` | TEXT | yes | Canonical imported-row digest. Derived monthly rows use the internal derived marker. |

### demographic_answers

One non-overlapping answer interval per subject.

| Field | SQLite type | Required | Meaning |
| --- | --- | --- | --- |
| `id` | TEXT | yes | Immutable opaque record identity. Primary-key component. |
| `subject_id` | TEXT | yes | Opaque synthetic analytical subject key. |
| `start_unix` | INTEGER | yes | Inclusive start of permission, eligibility, answer or observation coverage. |
| `end_unix` | INTEGER | no | Exclusive interval end. Null denotes an open interval. |
| `age_band` | TEXT | yes | Approved optional age-band enum. |
| `country` | TEXT | yes | Approved ISO country code or missing-answer state. |
| `language` | TEXT | yes | Approved optional language enum. |
| `household_band` | TEXT | yes | Approved optional household-size enum. |
| `_digest` | TEXT | yes | Canonical imported-row digest. Derived monthly rows use the internal derived marker. |

### feature_eligibility

One subject/feature eligibility interval.

| Field | SQLite type | Required | Meaning |
| --- | --- | --- | --- |
| `id` | TEXT | yes | Immutable opaque record identity. Primary-key component. |
| `subject_id` | TEXT | yes | Opaque synthetic analytical subject key. |
| `feature` | TEXT | yes | One of the eight completed core feature names. |
| `start_unix` | INTEGER | yes | Inclusive start of permission, eligibility, answer or observation coverage. |
| `end_unix` | INTEGER | no | Exclusive interval end. Null denotes an open interval. |
| `_digest` | TEXT | yes | Canonical imported-row digest. Derived monthly rows use the internal derived marker. |

### import_batches

One immutable batch ID and committed receipt.

| Field | SQLite type | Required | Meaning |
| --- | --- | --- | --- |
| `id` | TEXT | yes | Immutable opaque record identity. Primary-key component. |
| `digest` | TEXT | yes | SHA-256 of canonical typed batch content sorted by row identity. |
| `imported_unix` | INTEGER | yes | Frozen reporting reference recorded with the import receipt. |
| `row_count` | INTEGER | yes | Number of submitted typed rows in the batch. |

### monthly_contributions

One subject/month/metric/survey-version/dimension contribution. A subject has one segment in that grain.

| Field | SQLite type | Required | Meaning |
| --- | --- | --- | --- |
| `id` | TEXT | yes | Immutable opaque record identity. Primary-key component. |
| `subject_id` | TEXT | yes | Opaque synthetic analytical subject key. |
| `month` | TEXT | yes | UTC YYYY-MM bucket inside the thirteen-month window. |
| `metric` | TEXT | yes | One of the 43 registered metrics enumerated below. |
| `dimension` | TEXT | yes | all, age_band, country, language or household_band. |
| `segment` | TEXT | yes | all or one valid value of the selected dimension. |
| `numerator` | INTEGER | yes | Non-null binary outcome flag, 0 or 1. Its per-metric meaning is listed below. Always 0 for pending, excluded or incomplete observations. |
| `denominator` | INTEGER | yes | Non-null membership flag. Exactly 1 for eligible observations and 0 for pending, excluded or incomplete observations. |
| `query_version` | INTEGER | yes | Metric computation version fixed to 1. |
| `permission_validated` | INTEGER | yes | Required true/1 permission assertion for the contribution period. |
| `coverage` | TEXT | yes | complete or partial contribution provenance. |
| `survey_version` | TEXT | yes | Survey definition version. Empty for non-satisfaction monthly metrics. |
| `observation_status` | TEXT | yes | eligible, pending, excluded or incomplete. Eligible contributions have denominator 1, others 0. |
| `_digest` | TEXT | yes | Canonical imported-row digest. Derived monthly rows use the internal derived marker. |
| `origin` | TEXT | yes | imported for finalized history or derived for retained event inputs. |

### monthly_metrics

One month/metric/survey-version/dimension/segment aggregate.

| Field | SQLite type | Required | Meaning |
| --- | --- | --- | --- |
| `month` | TEXT | yes | UTC YYYY-MM bucket inside the thirteen-month window. Primary-key component. |
| `metric` | TEXT | yes | Closed metric registry shared with the report command. Primary-key component. |
| `survey_version` | TEXT | yes | Survey definition version. Empty for non-satisfaction monthly metrics. Primary-key component. |
| `dimension` | TEXT | yes | all, age_band, country, language or household_band. Primary-key component. |
| `segment` | TEXT | yes | all or one valid value of the selected dimension. Primary-key component. |
| `numerator` | INTEGER | no | Published sum of eligible outcome flags. Null in suppressed cells. |
| `denominator` | INTEGER | no | Published eligible-subject count, with the audience denominator pooled across segments of its dimension. Null in suppressed cells. |
| `status` | TEXT | yes | A closed status enum appropriate to the table, described below. |
| `query_version` | INTEGER | yes | Metric computation version fixed to 1. |
| `refreshed_unix` | INTEGER | yes | Frozen reporting reference used to compute this cache. |

Published status is `complete`, `partial`, `unavailable` or `suppressed`.
Missing month/metric inputs are emitted as unavailable by the monthly report command.

### satisfaction_invitations

One survey invitation ID.

| Field | SQLite type | Required | Meaning |
| --- | --- | --- | --- |
| `id` | TEXT | yes | Immutable opaque record identity. Primary-key component. |
| `subject_id` | TEXT | yes | Opaque synthetic analytical subject key. |
| `scope` | TEXT | yes | app or recipe satisfaction. |
| `survey_version` | TEXT | yes | Survey definition version. Empty for non-satisfaction monthly metrics. |
| `invited_unix` | INTEGER | yes | Observed invitation time within the event window. |
| `start_unix` | INTEGER | yes | Inclusive start of permission, eligibility, answer or observation coverage. |
| `end_unix` | INTEGER | yes | Required exclusive interval end. Invitations use it as the response deadline. |
| `_digest` | TEXT | yes | Canonical imported-row digest. Derived monthly rows use the internal derived marker. |

### satisfaction_responses

One response ID.

| Field | SQLite type | Required | Meaning |
| --- | --- | --- | --- |
| `id` | TEXT | yes | Immutable opaque record identity. Primary-key component. |
| `subject_id` | TEXT | yes | Opaque synthetic analytical subject key. |
| `scope` | TEXT | yes | app or recipe satisfaction. |
| `survey_version` | TEXT | yes | Survey definition version. Empty for non-satisfaction monthly metrics. |
| `occurred_unix` | INTEGER | yes | Observed event time within the extract event window. |
| `score` | INTEGER | yes | Integer from 1 through 5. |
| `invitation_id` | TEXT | no | Optional matching invitation with the same subject, scope and survey version. |
| `_digest` | TEXT | yes | Canonical imported-row digest. Derived monthly rows use the internal derived marker. |

### source_coverage

One source and interval observation.

| Field | SQLite type | Required | Meaning |
| --- | --- | --- | --- |
| `id` | TEXT | yes | Immutable opaque record identity. Primary-key component. |
| `source` | TEXT | yes | activity, eligibility, satisfaction_invitations, satisfaction_responses, communications or demographics. |
| `start_unix` | INTEGER | yes | Inclusive start of permission, eligibility, answer or observation coverage. |
| `end_unix` | INTEGER | yes | Required exclusive source-coverage interval end. |
| `status` | TEXT | yes | A closed status enum appropriate to the table, described below. |
| `_digest` | TEXT | yes | Canonical imported-row digest. Derived monthly rows use the internal derived marker. |

Coverage status is `complete`, `partial` or `unavailable`. Any overlapping
partial/unavailable declaration prevents a complete observation claim.

### subject_tombstones

One erased subject, retained for the synthetic extract lifetime.

| Field | SQLite type | Required | Meaning |
| --- | --- | --- | --- |
| `subject_id` | TEXT | yes | Opaque synthetic analytical subject key. Primary-key component. |
| `erased_unix` | INTEGER | yes | Frozen reporting reference recorded with the erasure tombstone. |

### subjects

One opaque participant.

| Field | SQLite type | Required | Meaning |
| --- | --- | --- | --- |
| `id` | TEXT | yes | Immutable opaque record identity. Primary-key component. |
| `eligible_from` | INTEGER | no | Observed application eligibility start. Nullable after old context expires. |
| `cohort_start` | INTEGER | no | Later of app eligibility and first product consent. Nullable after old context expires. |
| `notice_version` | TEXT | yes | Versioned permission notice using the v1-style closed grammar. |
| `_digest` | TEXT | yes | Canonical imported-row digest. Derived monthly rows use the internal derived marker. |

## Monthly component registry

Every contribution is non-null and binary, with `0 <= numerator <= denominator <= 1`.
`observation_status=eligible` requires denominator 1. `pending`, `excluded` and
`incomplete` observations use numerator 0 and denominator 0. Their status and
coverage preserve the reason a complete rate is unavailable. For the population
metrics `activity`, `opens` and `audience`, an eligible row requires numerator 1.
A participant outside those observed populations has no eligible contribution row.

The registry contains exactly these 43 metric names. The denominator column
below describes a contribution with denominator 1. The numerator column
explains when that participant has numerator 1. Otherwise an eligible rate
contribution has numerator 0. The week-based definitions also apply to monthly
cohort summaries, grouped by observable eligibility-start month for activation
and first-plan month for planning retention.

| Metric | Denominator membership | Numerator outcome |
| --- | --- | --- |
| `activity` | A participant with a completed core action in the month. | Always 1 for that observed population member. |
| `opens` | A participant with an app_active observation in the month. | Always 1 for that observed population member. |
| `audience` | An eligible product-analytics participant at the monthly cutoff. | Always 1, in the permitted demographic segment or missing-answer band. |
| `activation` | An observable new participant in this start month with a closed seven-day observation interval and continuous eligibility/consent. | A first meal plan was saved in the half-open seven-day interval after cohort start. |
| `retention` | A participant whose observable first plan belongs to this month, with the following calendar week closed and continuously eligible/consented. | Another meal plan was saved in that following calendar week. |
| `adoption_preferences_saved` | A meaningful active participant with this feature eligible at a qualifying action in the month. | The participant completed preferences_saved while eligible during the month. |
| `adoption_meal_plan_saved` | A meaningful active participant with this feature eligible at a qualifying action in the month. | The participant completed meal_plan_saved while eligible during the month. |
| `adoption_shopping_list_edited` | A meaningful active participant with this feature eligible at a qualifying action in the month. | The participant completed shopping_list_edited while eligible during the month. |
| `adoption_favorite_saved` | A meaningful active participant with this feature eligible at a qualifying action in the month. | The participant completed favorite_saved while eligible during the month. |
| `adoption_feedback_submitted` | A meaningful active participant with this feature eligible at a qualifying action in the month. | The participant completed feedback_submitted while eligible during the month. |
| `adoption_personal_recipe_saved` | A meaningful active participant with this feature eligible at a qualifying action in the month. | The participant completed personal_recipe_saved while eligible during the month. |
| `adoption_recipe_import_saved` | A meaningful active participant with this feature eligible at a qualifying action in the month. | The participant completed recipe_import_saved while eligible during the month. |
| `adoption_reminder_enabled` | A meaningful active participant with this feature eligible at a qualifying action in the month. | The participant completed reminder_enabled while eligible during the month. |
| `satisfaction_app_response_rate` | A distinct app survey invitee selected in this month whose applicable response interval closed. | At least one valid linked response exists for an included invitation with the same scope and survey version. |
| `satisfaction_app_score_1` | A distinct app respondent in the month and survey version, using their latest response. | That latest response has score 1. |
| `satisfaction_app_score_2` | A distinct app respondent in the month and survey version, using their latest response. | That latest response has score 2. |
| `satisfaction_app_score_3` | A distinct app respondent in the month and survey version, using their latest response. | That latest response has score 3. |
| `satisfaction_app_score_4` | A distinct app respondent in the month and survey version, using their latest response. | That latest response has score 4. |
| `satisfaction_app_score_5` | A distinct app respondent in the month and survey version, using their latest response. | That latest response has score 5. |
| `satisfaction_recipe_response_rate` | A distinct recipe survey invitee selected in this month whose applicable response interval closed. | At least one valid linked response exists for an included invitation with the same scope and survey version. |
| `satisfaction_recipe_score_1` | A distinct recipe respondent in the month and survey version, using their latest response. | That latest response has score 1. |
| `satisfaction_recipe_score_2` | A distinct recipe respondent in the month and survey version, using their latest response. | That latest response has score 2. |
| `satisfaction_recipe_score_3` | A distinct recipe respondent in the month and survey version, using their latest response. | That latest response has score 3. |
| `satisfaction_recipe_score_4` | A distinct recipe respondent in the month and survey version, using their latest response. | That latest response has score 4. |
| `satisfaction_recipe_score_5` | A distinct recipe respondent in the month and survey version, using their latest response. | That latest response has score 5. |
| `communication_push_queued` | A distinct recipient with an observed push communication stage in the month. | This recipient has an observed queued stage on that channel in the month. |
| `communication_push_accepted` | A distinct recipient with an observed push communication stage in the month. | This recipient has an observed accepted stage on that channel in the month. |
| `communication_push_delivered` | A distinct recipient with an observed push communication stage in the month. | This recipient has an observed delivered stage on that channel in the month. |
| `communication_push_bounced` | A distinct recipient with an observed push communication stage in the month. | This recipient has an observed bounced stage on that channel in the month. |
| `communication_push_failed` | A distinct recipient with an observed push communication stage in the month. | This recipient has an observed failed stage on that channel in the month. |
| `communication_push_tapped` | A distinct recipient with an observed push communication stage in the month. | This recipient has an observed tapped stage on that channel in the month. |
| `communication_push_unsubscribed` | A distinct recipient with an observed push communication stage in the month. | This recipient has an observed unsubscribed stage on that channel in the month. |
| `communication_push_complained` | A distinct recipient with an observed push communication stage in the month. | This recipient has an observed complained stage on that channel in the month. |
| `communication_push_conversion` | A distinct recipient with an observed push tap in the month and a closed 24-hour follow-up interval. | A completed core action is attributed to a selected tap on that channel in this month, within 24 hours inclusive. |
| `communication_email_queued` | A distinct recipient with an observed email communication stage in the month. | This recipient has an observed queued stage on that channel in the month. |
| `communication_email_accepted` | A distinct recipient with an observed email communication stage in the month. | This recipient has an observed accepted stage on that channel in the month. |
| `communication_email_delivered` | A distinct recipient with an observed email communication stage in the month. | This recipient has an observed delivered stage on that channel in the month. |
| `communication_email_bounced` | A distinct recipient with an observed email communication stage in the month. | This recipient has an observed bounced stage on that channel in the month. |
| `communication_email_failed` | A distinct recipient with an observed email communication stage in the month. | This recipient has an observed failed stage on that channel in the month. |
| `communication_email_tapped` | A distinct recipient with an observed email communication stage in the month. | This recipient has an observed tapped stage on that channel in the month. |
| `communication_email_unsubscribed` | A distinct recipient with an observed email communication stage in the month. | This recipient has an observed unsubscribed stage on that channel in the month. |
| `communication_email_complained` | A distinct recipient with an observed email communication stage in the month. | This recipient has an observed complained stage on that channel in the month. |
| `communication_email_conversion` | A distinct recipient with an observed email tap in the month and a closed 24-hour follow-up interval. | A completed core action is attributed to a selected tap on that channel in this month, within 24 hours inclusive. |

A table uniqueness constraint admits one contribution per subject, month,
metric, survey version and demographic dimension. That row chooses one segment.
Summing binary numerators counts distinct outcome participants. Summing binary
denominators counts distinct eligible participants. Repeated actions, taps,
invitations and responses cannot increase either component for that grain.
Survey versions remain separate. Raw response-event counts are available within
the event window and are separate from the monthly latest-response flags.

For audience composition, publication uses the total denominator across the
segments of the selected dimension. Other metrics use the denominator in each
segment. Population counts have null rates. Partial coverage preserves observed
counts and prevents a complete rate. Suppression applies to published sums and
rates, including small complementary groups and related score bins. The raw
contribution fields remain non-null binary values in private operator storage.

Cohort source coverage must cover the whole selection period. Survey invitation
coverage spans that period and response coverage spans the eligible response
intervals. Activation needs the eligibility-selection period. Communication
conversion needs the complete tap-selection period and its action/tap follow-up
coverage. Missing observations before the first observed row or after the last
observed row keep the rate incomplete in both raw and monthly outputs.
