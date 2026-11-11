CREATE TABLE feedback_reports (
    id INTEGER PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(user_id),
    request_key TEXT NOT NULL,
    request_hash TEXT NOT NULL CHECK(length(request_hash) = 64),
    reference TEXT NOT NULL UNIQUE CHECK(length(reference) = 32),
    topic TEXT NOT NULL CHECK(topic IN ('technical_issue', 'recipe_issue', 'subscription', 'other')),
    message TEXT NOT NULL CHECK(length(message) BETWEEN 20 AND 4000),
    source_kind TEXT,
    source_id INTEGER,
    priority TEXT NOT NULL CHECK(priority IN ('standard', 'priority')),
    created_unix INTEGER NOT NULL,
    outbox_id INTEGER NOT NULL UNIQUE REFERENCES email_outbox(id),
    UNIQUE(user_id, request_key),
    CHECK(
        (source_kind IS NULL AND source_id IS NULL) OR
        (source_kind IS NOT NULL AND source_kind = 'legacy_meal' AND source_id IS NOT NULL AND source_id > 0
            AND topic = 'recipe_issue')
    )
) STRICT;
CREATE INDEX feedback_reports_by_owner_time ON feedback_reports(user_id, created_unix, id);

CREATE TABLE feedback_ratings (
    user_id INTEGER NOT NULL REFERENCES users(user_id),
    source_kind TEXT NOT NULL CHECK(source_kind = 'legacy_meal'),
    source_id INTEGER NOT NULL CHECK(source_id > 0),
    rating INTEGER NOT NULL CHECK(rating BETWEEN 1 AND 5),
    PRIMARY KEY(user_id, source_kind, source_id)
) STRICT;

CREATE TABLE feedback_rating_receipts (
    id INTEGER PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(user_id),
    request_key TEXT NOT NULL,
    request_hash TEXT NOT NULL CHECK(length(request_hash) = 64),
    reference TEXT NOT NULL UNIQUE CHECK(length(reference) = 32),
    created_unix INTEGER NOT NULL,
    UNIQUE(user_id, request_key)
) STRICT;
CREATE INDEX feedback_rating_receipts_by_owner_time ON feedback_rating_receipts(user_id, created_unix, id);
