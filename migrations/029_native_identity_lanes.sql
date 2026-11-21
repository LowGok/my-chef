ALTER TABLE native_families ADD COLUMN identity_lane TEXT NOT NULL DEFAULT 'preview'
    CHECK(identity_lane IN ('preview','app'));
