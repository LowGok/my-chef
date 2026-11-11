package imports

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"
)

func policyFixture() (ExtractionPolicy, ExtractionBinding, time.Time) {
	now := time.Date(2026, time.November, 8, 1, 0, 0, 0, time.UTC)
	binding := ExtractionBinding{ProjectID: "synthetic-free-project", Model: "gemini-3.1-flash-lite", APIKey: "synthetic-key-never-sent"}
	digest := sha256.Sum256([]byte(binding.APIKey))
	policy := ExtractionPolicy{
		Enabled: true,
		Evidence: ExtractionEligibility{
			ProjectID: binding.ProjectID, Model: binding.Model, KeySHA256: hex.EncodeToString(digest[:]),
			CheckedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour),
			FreeTier: true, BillingUnlinked: true, ClientUseEligible: true, ImageTextSchemaSupported: true,
			FreeCallsPerMinute: 10, FreeCallsPerDay: 100, FreeTokensPerMinute: 30000,
		},
		Limits: ExtractionLimits{
			MaxImages: 3, MaxImageBytes: 4 << 20, MaxTotalImageBytes: 10 << 20,
			MaxImagePixels: 16000000, MaxTextBytes: 20000,
			MaxInputTokens: 8000, MaxOutputTokens: 4000, MaxThinkingTokens: 0,
			AccountCallsPerDay: 5, SourceCallsPerDay: 10,
			FleetCallsPerMinute: 2, FleetCallsPerDay: 20, TokensPerMinute: 25000, MaxConcurrent: 2,
		},
	}
	return policy, binding, now
}

func TestExtractionPolicyAllowsOnlyBoundEligibleFreeConfiguration(t *testing.T) {
	policy, binding, now := policyFixture()
	if err := policy.Check(now, binding); err != nil {
		t.Fatal("complete synthetic free-only configuration rejected", err)
	}
	policy.Limits.MaxThinkingTokens = 1000
	if err := policy.Check(now, binding); err != nil {
		t.Fatal("explicit bounded thinking allowance rejected", err)
	}
}

func TestExtractionPolicyRejectsUntrustedOrIncompleteConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		change func(*ExtractionPolicy, *ExtractionBinding, *time.Time)
	}{
		{"kill control", func(p *ExtractionPolicy, _ *ExtractionBinding, _ *time.Time) { p.Enabled = false }},
		{"unconfirmed free tier", func(p *ExtractionPolicy, _ *ExtractionBinding, _ *time.Time) { p.Evidence.FreeTier = false }},
		{"billing not confirmed unlinked", func(p *ExtractionPolicy, _ *ExtractionBinding, _ *time.Time) { p.Evidence.BillingUnlinked = false }},
		{"ineligible client region", func(p *ExtractionPolicy, _ *ExtractionBinding, _ *time.Time) { p.Evidence.ClientUseEligible = false }},
		{"unverified model capabilities", func(p *ExtractionPolicy, _ *ExtractionBinding, _ *time.Time) {
			p.Evidence.ImageTextSchemaSupported = false
		}},
		{"missing evidence", func(p *ExtractionPolicy, _ *ExtractionBinding, _ *time.Time) { p.Evidence = ExtractionEligibility{} }},
		{"missing project", func(_ *ExtractionPolicy, b *ExtractionBinding, _ *time.Time) { b.ProjectID = "" }},
		{"different project", func(_ *ExtractionPolicy, b *ExtractionBinding, _ *time.Time) { b.ProjectID = "other-project" }},
		{"missing key", func(_ *ExtractionPolicy, b *ExtractionBinding, _ *time.Time) { b.APIKey = "" }},
		{"different key", func(_ *ExtractionPolicy, b *ExtractionBinding, _ *time.Time) {
			b.APIKey = "rotated-without-new-evidence"
		}},
		{"malformed key binding", func(p *ExtractionPolicy, _ *ExtractionBinding, _ *time.Time) { p.Evidence.KeySHA256 = "not-a-digest" }},
		{"different model", func(_ *ExtractionPolicy, b *ExtractionBinding, _ *time.Time) { b.Model = "gemini-image-generation" }},
		{"missing model", func(_ *ExtractionPolicy, b *ExtractionBinding, _ *time.Time) { b.Model = "" }},
		{"image generation model", func(p *ExtractionPolicy, b *ExtractionBinding, _ *time.Time) {
			b.Model = "gemini-3.1-flash-image"
			p.Evidence.Model = b.Model
		}},
		{"invalid model path", func(p *ExtractionPolicy, b *ExtractionBinding, _ *time.Time) {
			b.Model = "../other-provider"
			p.Evidence.Model = b.Model
		}},
		{"future evidence", func(p *ExtractionPolicy, _ *ExtractionBinding, now *time.Time) {
			p.Evidence.CheckedAt = now.Add(time.Second)
		}},
		{"expired evidence", func(p *ExtractionPolicy, _ *ExtractionBinding, now *time.Time) { p.Evidence.ExpiresAt = *now }},
		{"missing check time", func(p *ExtractionPolicy, _ *ExtractionBinding, _ *time.Time) { p.Evidence.CheckedAt = time.Time{} }},
		{"indefinite evidence", func(p *ExtractionPolicy, _ *ExtractionBinding, _ *time.Time) {
			p.Evidence.ExpiresAt = p.Evidence.CheckedAt.Add(25 * time.Hour)
		}},
		{"clock unavailable", func(_ *ExtractionPolicy, _ *ExtractionBinding, now *time.Time) { *now = time.Time{} }},
		{"missing image count", func(p *ExtractionPolicy, _ *ExtractionBinding, _ *time.Time) { p.Limits.MaxImages = 0 }},
		{"too many photos", func(p *ExtractionPolicy, _ *ExtractionBinding, _ *time.Time) { p.Limits.MaxImages = 4 }},
		{"missing image byte bound", func(p *ExtractionPolicy, _ *ExtractionBinding, _ *time.Time) { p.Limits.MaxImageBytes = 0 }},
		{"image exceeds aggregate bound", func(p *ExtractionPolicy, _ *ExtractionBinding, _ *time.Time) {
			p.Limits.MaxImageBytes = p.Limits.MaxTotalImageBytes + 1
		}},
		{"missing pixel bound", func(p *ExtractionPolicy, _ *ExtractionBinding, _ *time.Time) { p.Limits.MaxImagePixels = 0 }},
		{"missing text bound", func(p *ExtractionPolicy, _ *ExtractionBinding, _ *time.Time) { p.Limits.MaxTextBytes = 0 }},
		{"missing input token bound", func(p *ExtractionPolicy, _ *ExtractionBinding, _ *time.Time) { p.Limits.MaxInputTokens = 0 }},
		{"missing output token bound", func(p *ExtractionPolicy, _ *ExtractionBinding, _ *time.Time) { p.Limits.MaxOutputTokens = 0 }},
		{"unbounded automatic thinking", func(p *ExtractionPolicy, _ *ExtractionBinding, _ *time.Time) { p.Limits.MaxThinkingTokens = -1 }},
		{"missing account limit", func(p *ExtractionPolicy, _ *ExtractionBinding, _ *time.Time) { p.Limits.AccountCallsPerDay = 0 }},
		{"missing source limit", func(p *ExtractionPolicy, _ *ExtractionBinding, _ *time.Time) { p.Limits.SourceCallsPerDay = 0 }},
		{"missing fleet window", func(p *ExtractionPolicy, _ *ExtractionBinding, _ *time.Time) { p.Limits.FleetCallsPerMinute = 0 }},
		{"missing fleet daily cap", func(p *ExtractionPolicy, _ *ExtractionBinding, _ *time.Time) { p.Limits.FleetCallsPerDay = 0 }},
		{"missing token window", func(p *ExtractionPolicy, _ *ExtractionBinding, _ *time.Time) { p.Limits.TokensPerMinute = 0 }},
		{"missing concurrency bound", func(p *ExtractionPolicy, _ *ExtractionBinding, _ *time.Time) { p.Limits.MaxConcurrent = 0 }},
		{"concurrency exceeds call window", func(p *ExtractionPolicy, _ *ExtractionBinding, _ *time.Time) {
			p.Limits.MaxConcurrent = p.Limits.FleetCallsPerMinute + 1
		}},
		{"account exceeds fleet daily", func(p *ExtractionPolicy, _ *ExtractionBinding, _ *time.Time) {
			p.Limits.AccountCallsPerDay = p.Limits.FleetCallsPerDay + 1
		}},
		{"source exceeds fleet daily", func(p *ExtractionPolicy, _ *ExtractionBinding, _ *time.Time) {
			p.Limits.SourceCallsPerDay = p.Limits.FleetCallsPerDay + 1
		}},
		{"call window exceeds free quota", func(p *ExtractionPolicy, _ *ExtractionBinding, _ *time.Time) {
			p.Limits.FleetCallsPerMinute = p.Evidence.FreeCallsPerMinute + 1
		}},
		{"daily calls exceed free quota", func(p *ExtractionPolicy, _ *ExtractionBinding, _ *time.Time) {
			p.Limits.FleetCallsPerDay = p.Evidence.FreeCallsPerDay + 1
		}},
		{"token window exceeds free quota", func(p *ExtractionPolicy, _ *ExtractionBinding, _ *time.Time) {
			p.Limits.TokensPerMinute = p.Evidence.FreeTokensPerMinute + 1
		}},
		{"one call exceeds token window", func(p *ExtractionPolicy, _ *ExtractionBinding, _ *time.Time) {
			p.Limits.MaxInputTokens = p.Limits.TokensPerMinute
		}},
		{"missing provider quota evidence", func(p *ExtractionPolicy, _ *ExtractionBinding, _ *time.Time) { p.Evidence.FreeCallsPerDay = 0 }},
		{"token count overflow", func(p *ExtractionPolicy, _ *ExtractionBinding, _ *time.Time) { p.Limits.MaxInputTokens = 1<<63 - 1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy, binding, now := policyFixture()
			tt.change(&policy, &binding, &now)
			if err := policy.Check(now, binding); !errors.Is(err, ErrExtractionUnavailable) {
				t.Fatal("unsafe or incomplete policy was accepted", err)
			}
		})
	}
}

func TestExtractionPolicyFailureDoesNotExposeCredentialOrAccountMetadata(t *testing.T) {
	policy, binding, now := policyFixture()
	policy.Evidence.Model = "private-model-canary"
	err := policy.Check(now, binding)
	if !errors.Is(err, ErrExtractionUnavailable) {
		t.Fatal("mismatched policy accepted")
	}
	for _, value := range []string{binding.APIKey, binding.ProjectID, policy.Evidence.Model, policy.Evidence.KeySHA256} {
		if strings.Contains(err.Error(), value) {
			t.Fatal("configuration error exposed private metadata")
		}
	}
}
