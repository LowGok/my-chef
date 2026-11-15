# Curated SQLite analytics dataset

MyChef's default analytics command creates a private SQLite extract with a deterministic
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

Frozen synthetic extracts remain reproducible when opened later. The original
commands accept version-1 synthetic files. The separate `live` commands below
operate on production version 2 through the first-party journal and copy registry.
Live capture starts disabled.

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


## Production integration and disabled rollout

The app retains GORM and SQLite. Explicit app migrations 019–023 add account
preferences and receipts, a bounded transactional outbox, admission context for
background plans, a restore marker, pending signup choices and source gap windows.
They preserve operational rows and create zero historical events. Run the existing
explicit app migration before starting this build; startup performs no migration.

Production stores use different SQLite application IDs: journal `1296253263`,
independent privacy controls `1296253264` and extracts `1296253265`. Version 2
validates its environment, app lifecycle binding, schema checksum, migration ledger
and physical schema fingerprint. The production DDL is constructed in
`internal/analyticsdataset/production.go`. The linked schema SQL above remains the
exact version-1 compatibility reference.

The app owns account-to-subject bindings. The independent privacy journal owns
restore-safe cleanup links, current objection/revision controls and the registered
copy inventory. Its high-water anchor records monotonic maintenance time and a
digest of those controls. Keep that journal and anchor outside application and
analytics recovery snapshots. Missing, older or changed controls stop analytical
access. App and operator processes share the same file lease through report output,
export, preference changes, delivery and copy publication.

A production extract contains `processing_periods` with the assessed basis,
version, notice, enrollment source and revision. Compatibility views feed the
shared metric SQL. Default enrollment creates a `registration_default` processing
receipt. Explicit later enrollment uses `explicit_enable`; Off records an objection.
Version-1 synthetic consent records keep their original meaning.

Seven server commit hooks capture preferences, saved plans, material shopping
edits, favorites, feedback reports, personal recipes and saved reviewed imports.
A reviewed import emits both applicable save events. Profile-only edits, unchanged
saves, read reconciliation, ratings, failed transactions and receipt replays emit
none. Event payloads contain version, independent random event/subject/period IDs,
UTC occurrence second, action enum, platform, authoritative plan and sequence.
Domain receipts remain app-only. No recipe contents or IDs, dietary/profile
answers, contact details, raw network/device data, feedback content or payment
information enter the analytical payload.

The initial production authority supports the five ordinary core capabilities.
Personal-recipe/import authority remains unavailable for real users while the
app's entitlement adapter is fixture-scoped. Reports disclose capability coverage;
fixture grants never establish real Premium status. Opens, reminders, surveys,
communications and demographics remain unavailable collection sources. Reports
cover enrolled observable participants, including default and explicit enrollment,
and exclude opt-outs. They do not establish a full signup funnel or representative
coverage of all accounts.

### Operator configuration

Keep `MYCHEF_ANALYTICS_CONFIG` unset during the disabled rollout. This local
implementation supplies no live activation file or approved assessment. Public
and native clients present Off when the matching assessed configuration is absent.
Existing accounts require an informed explicit enable. A missing/stale signup
notice creates the account with analytics Off. Pending signup Off choices win
across retries and competing proofs.

An operator runtime file has these fields:

| Field | Contract |
| --- | --- |
| `root` | Absolute private analytics directory. Directories are 0700 and files 0600; symlink paths are refused. |
| `privacyPath`, `anchorPath` | Independent private files outside the analytics root and recovery snapshots. |
| `backupKeyFile` | Existing operator secret-storage route supplying exactly 32 binary key bytes, outside backup material. |
| `collection.Enabled` | False for preparation and rollback. |
| `collection.NoticeVersion`, `collection.NoticeHash` | Current version and SHA-256 of the exact server `NoticeText`. |
| `collection.Assessment` | `status`, `basis`, `version`, exact `scope` digest, `noticeVersion`, jurisdiction, audience, device-access assessment and future `validUntil` UTC second. |
| `collection.OutboxCap` | Optional positive bound; the default is 10,000 pending events. A full queue creates a coverage gap. |

Collection requires Enabled, a matching approved unexpired assessment, the current
notice hash, private storage/binding and a backup key. The assessment and exact
live environment, build, notice, storage inventory and backup rehearsal require the
owner's later activation decision. A configuration flag is a technical gate and
supplies no finding that the proposed lawful basis is valid.

Initialize empty control storage explicitly with a fresh root and matching migrated
app, using the operator file. Initialization records no product history:

```sh
mychef-analytics live init --app /private/app.sqlite --config /private/analytics-config.json
mychef-analytics live validate --app /private/app.sqlite --config /private/analytics-config.json
mychef-analytics live maintenance --app /private/app.sqlite --config /private/analytics-config.json
```

Approved collection uses the existing service. It drains at most 100 records per
batch every 60 seconds, records source heartbeats/gaps and acknowledgement
watermarks, and builds a new extract after each completed UTC day. Successful
product work survives delivery/curation outages. The worker retains privacy
maintenance when collection is disabled. Health output contains coarse codes,
queue count and oldest age. Analytical identifiers and content stay out of logs.

These commands operate only on the configured journal and registered copies:

```sh
mychef-analytics live refresh --app /private/app.sqlite --config /private/analytics-config.json
mychef-analytics live report --app /private/app.sqlite --config /private/analytics-config.json --start 1793577600 --end 1794182400
mychef-analytics live report --app /private/app.sqlite --config /private/analytics-config.json --monthly
mychef-analytics live backup --app /private/app.sqlite --config /private/analytics-config.json --copy /private/analytics/current-extract.sqlite --destination /private/analytics/recovery.enc
mychef-analytics live restore --app /private/app.sqlite --config /private/analytics-config.json --copy /private/analytics/recovery.enc --destination /private/analytics/restored.sqlite
mychef-analytics live backup-app --app /private/app.sqlite --config /private/analytics-config.json --lifecycle-db /private/app.lifecycle --destination /private/app-backup.sqlite
mychef-analytics live restore-app --app /private/restored-app.sqlite --config /private/analytics-config.json --lifecycle-db /private/app.lifecycle --copy /private/app-backup.sqlite
```

Replace the example paths and report seconds with the approved operator inventory
and desired interval. Reports run under a privacy lease and retain the original
reporting date, maintenance timestamp, privacy/content revisions and suppression.
The default copy is the registered current-extract pointer. Report consumers receive
thresholded output. Report and account-export reads have a thirty-second limit, with cancellable process/file-lock waits. The fence stays held until the output writer has stopped. Report output supports memory buffers, regular files and deadline-capable pipes. On the Linux service, inherited stdout pipes receive a separate nonblocking descriptor for the same output, verified by file identity; the caller's descriptor flags stay unchanged. Unsupported writers are refused. Redirect CLI output to a private regular file when the terminal cannot provide write deadlines. HTTP exports use the server write deadline and request cancellation. Keep database files operator-only. Synthetic manifests, app
files, unknown schemas, wrong bindings and unregistered copies are refused.

### Retention, export and recovery

For reporting date R and monotonic maintenance time N, active event membership is
`[max(R-90*86400,N-90*86400),R]`. The journal and outbox use the current inclusive
90-day window. Monthly storage intersects the 13 UTC month buckets ending in R
with the 13 ending in N. Finalized closed-month contributions survive raw expiry;
missing historical coverage remains unavailable. Each subject/month retains every processing-basis version and enrollment revision whose period overlaps that month. Refresh carries and validates that historical authority; the current assessment cannot relabel earlier months. Expired periods and closed app receipts are removed when no retained observations or monthly authority need them, while the current objection remains as minimal control state. Every new refresh gets a new R.
Retention and objection cleanup preserve an existing R and advance its content
revision. The worker maintains at startup and at most 60-second healthy scheduling
intervals; report/export/backup/restore access maintains before releasing output.

Off cancels the outbox and records the durable collection/report fence before
acknowledgement. Active raw facts and monthly contributions are erased and
aggregates/suppression rebuilt. Backup work remains pending separately. A later
explicit On creates a fresh subject after objection; an unobjected restore pause
resumes a retained subject with a new epoch and a higher independent revision.
Old offline actions cannot become current after either transition.

The authenticated account ZIP includes `analytics.json`: current preference,
retained basis/change history, retrievable events and monthly contributions with
definitions. It uses the existing password/native authorization and no-store
incomplete-transfer contract. Account deletion invokes the same independent fence.

Application snapshots register private raw staging before the online copy, then strip analytics payloads, local bindings/preferences,
receipts, admission context and signup choices, then reclaim deleted bytes. Their
restore marker requires the current independent privacy journal and anchor.
Restored collection remains Off. Use `live backup-app` and `live restore-app` with the independent account
lifecycle journal and existing email-proof key route; retained history stays discoverable for personal export,
opt-out and account deletion.

Analytical recovery files use AES-GCM with the external operator key. Every copy
is registered before its first write and expires at creation plus seven days.
Copying or restoring it cannot extend that deadline. Restored extracts reconcile
current objections and retention before publication; expired or unreachable copies
cannot serve reports. Active erasure uses secure deletion, vacuum and verified
WAL checkpoint/truncation. An unresolved expiry or privacy failure remains an
operator error and an unfinished cleanup record.

Rollback keeps the runtime file with collection Enabled false so erasure, expiry
and copy reconciliation continue. Keep independent controls and keys available
until all cleanup is verified. Schema downgrade requires an explicitly verified
sanitized restore onto a fresh destination. Build publication, real data migration
and live collection remain later owner decisions.

### Lawful-basis assessment before real collection

[Issue 24](https://github.com/LowGok/my-chef/issues/24) tracks the remaining
assessment, deployment, provider and simulator/emulator evidence, followed by
separate approval and verification of live activation. Merging the disabled
integration does not close that issue or enable collection.

Live collection remains disabled. The controller must record a decision for the
actual deployment before enabling the proposed default-On signup preference.
The decision is pending; the fictional approved configurations in tests provide
technical evidence only.

| Assessment | Evidence needed |
| --- | --- |
| Purpose | The product decision supported by each event and the controller responsible for it. |
| Necessity | Why account-linked pseudonymous observations, each field, ninety days of detail and thirteen monthly buckets are needed, compared with aggregated or shorter-lived alternatives. |
| Balance | Actual countries, audience and age groups, reasonable expectations, potential effects and the safeguards that protect users' rights. |
| Device access | Review the actual browser/native cookies, storage, headers and other device access. Determine separately whether any operation requires affirmative consent under applicable ePrivacy rules. |
| Deployment | Identify storage, recipients, operator access, registered backups and the tested withdrawal, deletion and restore paths. |
| Recorded decision | Responsible controller, reviewer, date, conclusion, basis/scope version, exact notice and review/expiry date. No approval is recorded by this pull request. |

The proposed GDPR route is Article 6(1)(f), subject to its purpose, necessity and
balancing tests. Account creation or a preset On switch is default enrollment,
not an affirmative consent record. The technical configuration checks that an
operator-supplied assessment matches the collection; it cannot establish the
legal conclusion. See the [EDPB legitimate-interest guidelines](https://www.edpb.europa.eu/system/files/2026-11/edpb_guidelines_202401_legitimateinterest_v2_en.pdf).

The Belgian DPA states that audience-measurement cookies require consent,
including first-party cookies. The server-side capture design therefore needs
its actual client/device operations assessed separately; it supplies no blanket
exemption. See the [Belgian DPA audience-cookie guidance](https://www.autoriteprotectiondonnees.be/citoyen/faq/si-vous-souhaitez-installer-etou-lire-des-cookies-de-mesure-daudience-encore-connus-comme-cookies-statistiques-ou-analytiques-devez-vous-obtenir-le-consentement-de-lutilisateur-de-votre-site-internet-ou-de-votre-application).

### Read-only activation preflight

Use `live preflight` to check the proposed build and private evidence before an
activation decision is carried out:

```sh
mychef-analytics live preflight \
  --app /private/app.sqlite \
  --config /private/proposed-analytics.json \
  --evidence /private/activation-evidence.json \
  --binary /private/candidate/mychef \
  --source FULL_SOURCE_COMMIT \
  --environment arch-1-mychef
```

This command runs before the service-opening path. It does not initialize,
maintain, migrate, checkpoint, start a worker, change Enabled, write a notice or
send a network request. `live validate` still performs maintenance and must not
be used as its read-only substitute. Keep the proposed file separate from the
running service configuration until activation is authorized.

The generated manifest has three fields: `schemaVersion: 1`, `identity`, and
`evidence`. Its identity contains these exact names:

| Field | Meaning |
| --- | --- |
| `sourceCommit`, `environment`, `appBinding` | Exact source SHA, explicit environment and private app installation binding. |
| `binarySha256`, `configSha256` | SHA-256 of the proposed application binary and runtime file bytes. |
| `apiSha256`, `androidArtifactSha256`, `iosArtifactSha256` | API contract and installed native artifact digests established by the candidate's verification. |
| `noticeVersion`, `noticeSha256`, `scopeSha256`, `assessmentVersion` | The versions and digests shared by the assessed configuration and its evidence. |

The `evidence` object must contain all eleven references: `controller`,
`privacy`, `providers`, `deviceAccess`, `nativeAndroid`, `nativeIOS`, `ci`,
`storage`, `recovery`, `implementationReview`, and `activation`. Each reference
contains a private absolute `path` and its file's `sha256`. Files must be regular,
private and accessible without symlink components. Records and their supporting
documents stay outside public Git history and CI artifacts.

Each referenced record has `schemaVersion: 1`, its `kind`, `status`,
`bindingSha256`, `verifiedUnix`, `expiresUnix`, and a `document` reference with
`path` and `sha256`. The document is the actual conclusion, inventory, review,
receipt or rehearsal supporting the record. The binding digest is the SHA-256
of the identity encoded as compact JSON with keys sorted. Times are UTC Unix
seconds. Future, expired or inconsistent dates refuse readiness; record expiry
cannot exceed the assessment expiry.

The controller and activation records require status `approved`; the other
records require `passed`. Privacy also requires an empty `unresolved` array.
Each native record requires `fullJourneys: true` and
`spokenAccessibility: true`, backed by observed results for the same candidate.
Incomplete installed probes cannot supply these outcomes. Other record kinds
cannot claim native outcomes. Unknown fields, duplicate keys, missing
records and changed file hashes refuse readiness.

Use a quiescent source during this check. It reads committed WAL data through
read-only file handles and a private in-memory WAL index. It never creates or
updates SHM files. File identities, modes, timestamps and digests are checked
again before returning; input changes refuse readiness. The app schema, app and
analytics bindings, privacy anchor, registered inventory and overdue recovery
copies must also pass their read-only checks. No failed check repairs storage.
The private WAL reader follows SQLite's
[WAL-without-shared-memory mechanism](https://sqlite.org/wal.html#noshm).

Inputs are bounded: 64 KiB per evidence record/runtime file, 1 MiB per manifest,
16 MiB per supporting document, 256 MiB per binary and 512 MiB per SQLite file
or sidecar. Oversized files refuse readiness. SQLite file and document hashing
streams bytes without retaining their contents as evidence.

Standard output contains only `status` (`ready` or `blocked`), coarse `codes`
and evidence `digests`. A blocked result exits nonzero. Paths, binding values,
account rows, subject identifiers, supporting prose and key material are absent.
Ready validates recorded evidence at that time. The controller supplies the
legal conclusion and the separate activation authorization; the command never
turns collection on. Actual controller, native and deployment evidence remains
pending for this release.

### Assessment boundaries

Approval must explain why each field and retained window is necessary compared
with coarser, aggregate or shorter-lived alternatives. The ninety-day event and
thirteen-bucket contribution windows are maximum implementation limits, not
standalone legal justifications. A pseudonymous record remains personal data.

Inventory cookies, RAM state, request headers, offline queue metadata, native
protected storage and enabled SDKs separately. A default On choice is not consent
for optional terminal access. A nonempty assessment field cannot establish an
exemption. Unresolved purpose, device-access, provider, notice, age/health or DPIA
conditions belong in the privacy record and block preflight readiness.

The seven-day isolated-backup rule covers MyChef's analytics recovery copies.
Provider email logs, installation identifiers, operational storage and newsletter
records have separate retention/deletion duties. Verify and disclose the actual
provider behavior before approving the customer notice; do not promise that an
analytics withdrawal erases every provider record within seven days.

### Disposable recovery verification

After building the app, `python3 scripts/test_analytics_rehearsal.py` creates and
removes a fresh fictional installation without accepting any customer database
path. It exercises real migration, HTTP action, process restart and analytics/app
backup and restore commands. The private clock/worker overlays are test-only;
this command supplies no deployed-host, provider or native accessibility proof.
Its bounded result records source/artifact hashes, dirty-source status, fixed
outcomes and hashed registered-copy inventory. Re-run against the final committed
candidate, and repeat the existing SQLite upgrade rehearsal for any changed app
schema. Live activation remains a separate decision.

### Remaining simulator and emulator verification

The integration remains in progress. Local checks passed the Go suites,
web/native unit and Java-boundary tests, nine analytics/native browser journeys,
builds, lint and the disposable SQLite upgrade/restore rehearsal. Browser fixtures
covered a 320-pixel viewport and 200-percent text. These results do not establish
native operating-system behavior.

The following evidence remains unverified on both an iOS simulator and Android
emulator:

- Signup notice and proposed On/Off choice before account completion.
- Profile enable/opt-out, stale-revision conflicts and truthful pending deletion
  and backup-cleanup states.
- Offline opt-out feedback, reconnect/resume and queued actions retaining their
  original analytics revision.
- Accessible labels, keyboard/screen-reader behavior, phone widths and enlarged
  text throughout those flows.
- Account export/deletion and session/cache cleanup through the packaged app.

The existing native workflow is restricted to another approved PR/branch and
does not automatically verify this integration. Compilation or browser bridge
checks must not be recorded as simulator/emulator results. Record the exact
candidate, runtime/device configuration and observed journey results before
closing the native verification criterion. This PR enables no signing,
distribution, live storage, real-data migration or collection.
