CREATE TABLE email_rate_events (
    bucket TEXT NOT NULL,
    stamp INTEGER NOT NULL,
    reservation TEXT NOT NULL,
    PRIMARY KEY (bucket, reservation)
) WITHOUT ROWID;

CREATE INDEX email_rate_events_bucket_stamp ON email_rate_events(bucket, stamp DESC);
CREATE INDEX email_rate_events_stamp ON email_rate_events(stamp);
