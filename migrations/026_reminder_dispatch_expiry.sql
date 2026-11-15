ALTER TABLE reminder_dispatches ADD COLUMN submission_deadline INTEGER
    CHECK(submission_deadline IS NULL OR submission_deadline>0);
ALTER TABLE reminder_dispatches ADD COLUMN schedule_checked INTEGER NOT NULL DEFAULT 0
    CHECK(schedule_checked>=0);
