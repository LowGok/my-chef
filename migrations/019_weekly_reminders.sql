CREATE TABLE reminder_preferences (
    incarnation TEXT PRIMARY KEY REFERENCES account_incarnations(incarnation) ON DELETE CASCADE,
    zone TEXT NOT NULL CHECK(length(zone) BETWEEN 1 AND 100),
    current_enabled INTEGER NOT NULL CHECK(current_enabled IN (0,1)),
    advance_enabled INTEGER NOT NULL CHECK(advance_enabled IN (0,1)),
    advance_day INTEGER NOT NULL CHECK(advance_day BETWEEN 0 AND 6),
    advance_minute INTEGER NOT NULL CHECK(advance_minute BETWEEN 0 AND 1439),
    revision INTEGER NOT NULL CHECK(revision>0),
    next_evaluation INTEGER NOT NULL
) STRICT;
CREATE INDEX reminder_preferences_due ON reminder_preferences(next_evaluation,incarnation)
    WHERE current_enabled=1 OR advance_enabled=1;

CREATE TABLE reminder_installations (
    family_id TEXT PRIMARY KEY REFERENCES native_families(id) ON DELETE CASCADE,
    incarnation TEXT NOT NULL REFERENCES account_incarnations(incarnation) ON DELETE CASCADE,
    platform TEXT NOT NULL CHECK(platform IN ('ios','android')),
    provider TEXT NOT NULL CHECK(provider IN ('apns','fcm')),
    token_tag BLOB NOT NULL UNIQUE CHECK(length(token_tag)=32),
    token_cipher BLOB NOT NULL CHECK(length(token_cipher)>28),
    refreshed_unix INTEGER NOT NULL
) STRICT;
CREATE INDEX reminder_installations_owner ON reminder_installations(incarnation,family_id);

CREATE TABLE reminder_occurrences (
    id TEXT PRIMARY KEY CHECK(length(id)=32),
    incarnation TEXT NOT NULL REFERENCES reminder_preferences(incarnation) ON DELETE CASCADE,
    purpose TEXT NOT NULL CHECK(purpose IN ('current','advance')),
    target_week TEXT NOT NULL CHECK(length(target_week)=10),
    scheduled INTEGER NOT NULL,
    deadline INTEGER NOT NULL CHECK(deadline>scheduled),
    revision INTEGER NOT NULL,
    frozen INTEGER NOT NULL DEFAULT 0 CHECK(frozen IN (0,1)),
    next_check INTEGER NOT NULL DEFAULT 0,
    UNIQUE(incarnation,purpose,target_week)
) STRICT;
CREATE INDEX reminder_occurrences_due ON reminder_occurrences(next_check,scheduled,id) WHERE frozen=0;

CREATE TABLE reminder_dispatches (
    reference TEXT PRIMARY KEY CHECK(length(reference)=32),
    occurrence TEXT NOT NULL REFERENCES reminder_occurrences(id) ON DELETE CASCADE,
    family_id TEXT NOT NULL CHECK(length(family_id)=32),
    state TEXT NOT NULL CHECK(state IN ('pending','sending','accepted','uncertain','permanent','suppressed')),
    next_attempt INTEGER NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 5),
    UNIQUE(occurrence,family_id)
) STRICT;
CREATE INDEX reminder_dispatches_due ON reminder_dispatches(next_attempt,reference) WHERE state='pending';
