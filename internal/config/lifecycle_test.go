package config

import "testing"

func TestLifecycleJournalConfigIsIndependentAndExplicit(t *testing.T) {
	t.Setenv("MYCHEF_ORIGIN", "https://app.example.test")
	t.Setenv("MYCHEF_PROVIDER", "gemini")
	t.Setenv("MYCHEF_DB", "/tmp/lifecycle-config-app.db")
	t.Setenv("MYCHEF_LIFECYCLE_DB", "")
	cfg, err := Load()
	if err != nil || cfg.LifecycleDatabase != "/tmp/lifecycle-config-app.db.lifecycle" {
		t.Fatal("missing independent default journal", err)
	}
	t.Setenv("MYCHEF_LIFECYCLE_DB", "/tmp/independent-journal.db")
	cfg, err = Load()
	if err != nil || cfg.LifecycleDatabase != "/tmp/independent-journal.db" {
		t.Fatal("explicit journal not honored", err)
	}
	t.Setenv("MYCHEF_LIFECYCLE_DB", "/tmp/./lifecycle-config-app.db")
	if _, err := Load(); err == nil {
		t.Fatal("journal aliases application database")
	}
}
