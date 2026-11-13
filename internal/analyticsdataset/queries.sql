-- Each query follows the parameters CTE supplied by the report runner.
-- Columns are subject, numerator, denominator, segment time, reason, version,
-- observation start, observation end, and response event count.

-- name: activity
SELECT a.subject_id,1,1,min(a.occurred_unix),'eligible','',p.lo,p.hi,0
FROM core_activity a,p WHERE a.occurred_unix>=p.lo AND a.occurred_unix<p.hi GROUP BY a.subject_id;

-- name: opens
SELECT a.subject_id,1,1,min(a.occurred_unix),'eligible','',p.lo,p.hi,0
FROM activity_events a,p WHERE a.name='app_active' AND a.occurred_unix>=p.lo AND a.occurred_unix<p.hi GROUP BY a.subject_id;

-- name: adoption
SELECT a.subject_id,
 EXISTS(SELECT 1 FROM core_activity x WHERE x.subject_id=a.subject_id AND x.name=p.feature AND x.occurred_unix>=p.lo AND x.occurred_unix<p.hi),
 1,min(a.occurred_unix),'eligible','',p.lo,p.hi,0
FROM core_activity a,p WHERE a.occurred_unix>=p.lo AND a.occurred_unix<p.hi
 AND EXISTS(SELECT 1 FROM feature_eligibility f WHERE f.subject_id=a.subject_id AND f.feature=p.feature
 AND f.start_unix<=a.occurred_unix AND coalesce(f.end_unix,253402300799)>a.occurred_unix)
GROUP BY a.subject_id;

-- name: activation
SELECT s.id,
 EXISTS(SELECT 1 FROM activity_events a WHERE a.subject_id=s.id AND a.name='meal_plan_saved' AND a.occurred_unix>=s.cohort_start AND a.occurred_unix<s.cohort_start+604800),
 1,s.cohort_start,
 CASE WHEN s.cohort_start+604800>p.asof THEN 'pending'
 WHEN s.cohort_start<p.cutoff THEN 'incomplete'
 WHEN NOT EXISTS(SELECT 1 FROM spans c WHERE c.subject_id=s.id AND c.purpose='product' AND c.start_unix<=s.cohort_start AND c.end_unix>=s.cohort_start+604800) THEN 'excluded' ELSE 'eligible' END,
 '',s.cohort_start,s.cohort_start+604800,0
FROM subjects s,p WHERE s.cohort_start>=p.lo AND s.cohort_start<p.hi;

-- name: retention
,first_plans AS (
 SELECT s.id,s.cohort_start,min(a.occurred_unix) AS first_plan
 FROM subjects s JOIN activity_events a ON a.subject_id=s.id AND a.name='meal_plan_saved' AND a.occurred_unix>=s.cohort_start
 GROUP BY s.id
),cohorts AS (
 SELECT *,unixepoch(date(first_plan,'unixepoch'))-((cast(strftime('%w',first_plan,'unixepoch') AS INTEGER)+6)%7)*86400 AS week_start FROM first_plans
)
SELECT c.id,
 EXISTS(SELECT 1 FROM activity_events a WHERE a.subject_id=c.id AND a.name='meal_plan_saved' AND a.occurred_unix>=c.week_start+604800 AND a.occurred_unix<c.week_start+1209600),
 1,c.first_plan,
 CASE WHEN c.cohort_start<p.cutoff THEN 'incomplete' WHEN c.week_start+1209600>p.asof THEN 'pending'
 WHEN NOT EXISTS(SELECT 1 FROM spans x WHERE x.subject_id=c.id AND x.purpose='product' AND x.start_unix<=c.cohort_start AND x.end_unix>=c.week_start+1209600) THEN 'excluded' ELSE 'eligible' END,
 '',c.cohort_start,c.week_start+1209600,0
FROM cohorts c,p WHERE c.first_plan>=p.lo AND c.first_plan<p.hi;

-- name: satisfaction_score
,ranked AS (
 SELECT r.*,row_number() OVER(PARTITION BY r.subject_id,r.scope,r.survey_version ORDER BY occurred_unix DESC,id DESC) AS rank,
 count(*) OVER(PARTITION BY r.subject_id,r.scope,r.survey_version) AS response_events
 FROM satisfaction_responses r,p WHERE r.scope=p.scope AND r.occurred_unix>=p.lo AND r.occurred_unix<p.hi
)
SELECT r.subject_id,cast(r.score=p.score AS INTEGER),1,r.occurred_unix,'eligible',r.survey_version,p.lo,p.hi,r.response_events
FROM ranked r,p WHERE r.rank=1;

-- name: satisfaction_rate
,invited AS (
 SELECT i.subject_id,i.scope,i.survey_version,min(i.invited_unix) AS invited,
 max(CASE WHEN i.end_unix<=p.asof THEN 1 ELSE 0 END) AS closed,
 max(CASE WHEN i.end_unix<=p.asof THEN i.end_unix ELSE 0 END) AS observed_end
 FROM satisfaction_invitations i,p WHERE i.scope=p.scope AND i.invited_unix>=p.lo AND i.invited_unix<p.hi
 GROUP BY i.subject_id,i.scope,i.survey_version
)
SELECT i.subject_id,
 EXISTS(SELECT 1 FROM satisfaction_responses r JOIN satisfaction_invitations x ON x.id=r.invitation_id
 WHERE x.subject_id=i.subject_id AND x.scope=i.scope AND x.survey_version=i.survey_version
 AND x.invited_unix>=p.lo AND x.invited_unix<p.hi AND x.end_unix<=p.asof),
 1,i.invited,CASE WHEN i.closed=0 THEN 'pending' ELSE 'eligible' END,i.survey_version,i.invited,i.observed_end,0
FROM invited i,p;

-- name: communication_status
SELECT c.subject_id,max(c.status=p.status),1,min(c.occurred_unix),'eligible','',p.lo,p.hi,0
FROM communication_events c,p WHERE c.channel=p.channel AND c.occurred_unix>=p.lo AND c.occurred_unix<p.hi
GROUP BY c.subject_id;

-- name: communication_conversion
SELECT c.subject_id,
 EXISTS(SELECT 1 FROM attributed_actions a
 WHERE a.subject_id=c.subject_id AND a.tap_channel=p.channel AND a.tap_unix>=p.lo AND a.tap_unix<p.hi),
 1,min(c.occurred_unix),CASE WHEN max(c.occurred_unix)+86400>p.asof THEN 'pending' ELSE 'eligible' END,'',min(c.occurred_unix),max(c.occurred_unix)+86401,0
FROM communication_events c,p WHERE c.channel=p.channel AND c.status='tapped' AND c.occurred_unix>=p.lo AND c.occurred_unix<p.hi GROUP BY c.subject_id;

-- name: audience
SELECT s.id,1,1,p.asof,'eligible','',p.asof,p.asof+1,0 FROM subjects s,p WHERE s.eligible_from<=p.asof
 AND EXISTS(SELECT 1 FROM spans c WHERE c.subject_id=s.id AND c.purpose='product' AND c.start_unix<=p.asof AND c.end_unix>p.asof);
