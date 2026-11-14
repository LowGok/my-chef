package analytics

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAssessmentFence(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	if (Config{}).Authorized(now) {
		t.Fatal("default enables capture")
	}
	c := Config{Enabled: true, NoticeVersion: "core-v1", NoticeHash: Digest([]byte(NoticeText)), Assessment: Assessment{Status: "approved", Basis: "legitimate_interest", Version: "assessment-v1", Scope: ScopeDigest(), NoticeVersion: "core-v1", Jurisdiction: "BE", Audience: "adults", DeviceAccess: "server_only_reviewed", ValidUntil: now.Add(time.Hour).Unix()}}
	if !c.Authorized(now) {
		t.Fatal("matched assessment rejected")
	}
	for _, change := range []func(*Config){func(c *Config) { c.Enabled = false }, func(c *Config) { c.Assessment.Status = "pending" }, func(c *Config) { c.Assessment.Scope = "other" }, func(c *Config) { c.Assessment.ValidUntil = now.Unix() }, func(c *Config) { c.NoticeVersion = "new" }, func(c *Config) { c.Assessment.DeviceAccess = "" }} {
		bad := c
		change(&bad)
		if bad.Authorized(now) {
			t.Fatal("invalid assessment authorizes capture")
		}
	}
}
func TestClosedEventContract(t *testing.T) {
	id, err := NewID("evt")
	if err != nil || !ValidID(id, "evt") {
		t.Fatalf("id: %s %v", id, err)
	}
	e := Event{Version: 2, ID: id, Subject: mustID(t, "sub"), Epoch: mustID(t, "per"), Occurred: 1794657600, Name: "meal_plan_saved", Platform: "ios", Plan: "free", Sequence: 1}
	if err = e.Validate(); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(e)
	var fields map[string]any
	_ = json.Unmarshal(b, &fields)
	if len(fields) != 9 {
		t.Fatalf("expanded payload: %s", b)
	}
	for _, s := range []string{"account", "recipe", "email", "properties", "user"} {
		if strings.Contains(string(b), s) {
			t.Fatalf("forbidden field %s", s)
		}
	}
	e.Name = "app_active"
	if e.Validate() == nil {
		t.Fatal("unsupported event accepted")
	}
}
func mustID(t *testing.T, p string) string {
	t.Helper()
	s, e := NewID(p)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
