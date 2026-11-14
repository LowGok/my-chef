-- Published monthly cells already carry cohort and complementary suppression.
-- Open the database read-only when running these queries.
SELECT month,metric,survey_version,dimension,segment,numerator,denominator,status,
 CASE WHEN metric NOT IN ('activity','opens') AND status='complete' AND denominator>0 THEN 1.0*numerator/denominator END AS rate,
 query_version,refreshed_unix
FROM monthly_metrics
ORDER BY month,metric,survey_version,dimension,segment;

-- Source availability for event-based report windows.
SELECT source,start_unix,end_unix,status
FROM source_coverage ORDER BY source,start_unix,end_unix;

-- Extract identity and frozen reporting reference.
SELECT version,environment,extract_id,reporting_unix FROM analytics_schema WHERE singleton=1;
