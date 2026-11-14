package auth

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/analytics"
)

func signupAnalytics(t *testing.T, s *SignUp, now *time.Time) {
	t.Helper()
	root := t.TempDir()
	_ = os.Chmod(root, 0700)
	a, e := analytics.Open(t.Context(), s.Store.DB, analytics.OpenOptions{Root: filepath.Join(root, "data"), PrivacyPath: filepath.Join(root, "privacy.sqlite"), AnchorPath: filepath.Join(root, "anchor"), Create: true, Now: func() time.Time { return *now }, BackupKey: bytes.Repeat([]byte{5}, 32), Config: analytics.Config{Enabled: true, NoticeVersion: "core-v1", NoticeHash: analytics.Digest([]byte(analytics.NoticeText)), Assessment: analytics.Assessment{Status: "approved", Basis: "legitimate_interest", Version: "fictional", Scope: analytics.ScopeDigest(), NoticeVersion: "core-v1", Jurisdiction: "fictional", Audience: "fictional", DeviceAccess: "fictional", ValidUntil: now.AddDate(1, 0, 0).Unix()}}})
	if e != nil {
		t.Fatal(e)
	}
	s.Store.Analytics = a
	t.Cleanup(func() { _ = a.Close() })
}
func TestSignupAnalyticsDefaultAndPendingOffWins(t *testing.T) {
	for _, off := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "pending_off"}[off], func(t *testing.T) {
			s, now := signUpFixture(t)
			signupAnalytics(t, s, now)
			yes, no := true, false
			details := SignUpDetails{FirstName: "New", LastName: "Fixture", Username: "newfixture", AnalyticsEnabled: &yes, AnalyticsNoticeVersion: "core-v1"}
			request := SignUpRequest{SignUpDetails: details, Email: "newfixture@example.invalid"}
			if _, e := s.Request(t.Context(), "192.0.2.1", request); e != nil {
				t.Fatal(e)
			}
			if off {
				request.AnalyticsEnabled = &no
				if _, e := s.Request(t.Context(), "192.0.2.1", request); e != nil {
					t.Fatal(e)
				}
			}
			for {
				worked, e := s.Step(t.Context())
				if e != nil {
					t.Fatal(e)
				}
				if !worked {
					break
				}
			}
			proof := signUpLink(t, s, *now)
			inspected, e := s.Inspect(t.Context(), proof)
			if e != nil || inspected.AnalyticsEnabled == nil || *inspected.AnalyticsEnabled == off {
				t.Fatalf("inspect %+v %v", inspected, e)
			}
			anonymous, e := New(s.Store).Rotate(t.Context(), nil, "")
			if e != nil {
				t.Fatal(e)
			}
			if e = s.Complete(t.Context(), anonymous, SignUpCompletion{SignUpDetails: details, Proof: proof, Password: "FixtureSafePassword2026!"}); e != nil {
				t.Fatal(e)
			}
			var enabled bool
			var source string
			if e = s.Store.SQL.QueryRow("SELECT enabled,source FROM analytics_preferences").Scan(&enabled, &source); e != nil || enabled == off {
				t.Fatalf("enrollment %v %s %v", enabled, source, e)
			}
			if enabled && source != "registration_default" {
				t.Fatal("default fabricated consent", source)
			}
		})
	}
}

func TestSignupMissingOrStaleMetadataStaysOff(t *testing.T) {
	for _, notice := range []string{"", "old-notice"} {
		t.Run("notice_"+notice, func(t *testing.T) {
			s, now := signUpFixture(t)
			signupAnalytics(t, s, now)
			yes := true
			details := SignUpDetails{FirstName: "New", LastName: "Fixture", Username: "oldclient", AnalyticsNoticeVersion: notice}
			if notice != "" {
				details.AnalyticsEnabled = &yes
			}
			if _, e := s.Request(t.Context(), "192.0.2.2", SignUpRequest{Email: "oldclient@example.invalid", SignUpDetails: details}); e != nil {
				t.Fatal(e)
			}
			if _, e := s.Step(t.Context()); e != nil {
				t.Fatal(e)
			}
			proof := signUpLink(t, s, *now)
			anonymous, e := New(s.Store).Rotate(t.Context(), nil, "")
			if e != nil {
				t.Fatal(e)
			}
			details.AnalyticsEnabled = &yes
			details.AnalyticsNoticeVersion = "core-v1"
			if e = s.Complete(t.Context(), anonymous, SignUpCompletion{SignUpDetails: details, Proof: proof, Password: "FixtureSafePassword2026!"}); e != nil {
				t.Fatal(e)
			}
			var enabled bool
			if e = s.Store.SQL.QueryRow("SELECT enabled FROM analytics_preferences").Scan(&enabled); e != nil || enabled {
				t.Fatal("stale signup enrolled", e)
			}
		})
	}
}
