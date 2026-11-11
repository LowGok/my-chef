package imports

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"
)

// ErrExtractionUnavailable deliberately omits credential and project metadata.
var ErrExtractionUnavailable = errors.New("recipe extraction is unavailable")

var extractionModelName = regexp.MustCompile(`^gemini-[a-z0-9]+([.-][a-z0-9]+)*$`)
var extractionProjectID = regexp.MustCompile(`^[a-z][a-z0-9-]{4,62}$`)

// ExtractionBinding describes the worker's configured provider, not client input.
// The project association must be checked by the operator: an API key cannot
// establish its own project's billing state.
type ExtractionBinding struct {
	ProjectID string
	Model     string
	APIKey    string
}

// ExtractionEligibility records out-of-band operator evidence. It is not a
// Google-issued billing attestation and never authorizes enabling billing.
type ExtractionEligibility struct {
	ProjectID                string
	Model                    string
	KeySHA256                string
	CheckedAt                time.Time
	ExpiresAt                time.Time
	FreeTier                 bool
	BillingUnlinked          bool
	ClientUseEligible        bool
	ImageTextSchemaSupported bool
	FreeCallsPerMinute       int64
	FreeCallsPerDay          int64
	FreeTokensPerMinute      int64
}

// ExtractionLimits are conservative ceilings, not observed provider usage.
// The dispatch adapter must enforce the input bounds and admission must reserve
// the complete token allowance, including output and any thinking allowance.
type ExtractionLimits struct {
	MaxImages           int
	MaxImageBytes       int64
	MaxTotalImageBytes  int64
	MaxImagePixels      int64
	MaxTextBytes        int64
	MaxInputTokens      int64
	MaxOutputTokens     int64
	MaxThinkingTokens   int64
	AccountCallsPerDay  int64
	SourceCallsPerDay   int64
	FleetCallsPerMinute int64
	FleetCallsPerDay    int64
	TokensPerMinute     int64
	MaxConcurrent       int64
}

// ExtractionPolicy belongs to the worker. Its enabled state is the kill control;
// the web app must have no write access to this policy or its admission ledger.
type ExtractionPolicy struct {
	Enabled  bool
	Evidence ExtractionEligibility
	Limits   ExtractionLimits
}

// Check validates a free-only dispatch configuration without any I/O. It must
// run again before admission, so expiry or a kill cannot be bypassed by a policy
// that was valid at worker startup. A successful check is not a reservation and
// does not attest that the cloud administrator has left billing unchanged.
func (p ExtractionPolicy) Check(now time.Time, binding ExtractionBinding) error {
	if !p.Enabled || !p.Evidence.valid(now, binding) || !p.Limits.valid(p.Evidence) {
		return ErrExtractionUnavailable
	}
	return nil
}

func (e ExtractionEligibility) valid(now time.Time, binding ExtractionBinding) bool {
	if now.IsZero() || e.CheckedAt.IsZero() || e.ExpiresAt.IsZero() ||
		e.CheckedAt.After(now) || !now.Before(e.ExpiresAt) ||
		e.ExpiresAt.Sub(e.CheckedAt) > 24*time.Hour ||
		!e.FreeTier || !e.BillingUnlinked || !e.ClientUseEligible || !e.ImageTextSchemaSupported {
		return false
	}
	if !extractionProjectID.MatchString(binding.ProjectID) || e.ProjectID != binding.ProjectID ||
		len(binding.Model) > 128 || !extractionModelName.MatchString(binding.Model) ||
		strings.Contains(binding.Model, "image") || strings.Contains(binding.Model, "tts") ||
		e.Model != binding.Model || !validExtractionKey(binding.APIKey) {
		return false
	}
	fingerprint, err := hex.DecodeString(e.KeySHA256)
	if err != nil || len(fingerprint) != sha256.Size {
		return false
	}
	digest := sha256.Sum256([]byte(binding.APIKey))
	return subtle.ConstantTimeCompare(fingerprint, digest[:]) == 1 &&
		e.FreeCallsPerMinute > 0 && e.FreeCallsPerDay > 0 && e.FreeTokensPerMinute > 0
}

func validExtractionKey(key string) bool {
	if len(key) == 0 || len(key) > 512 {
		return false
	}
	for _, b := range []byte(key) {
		if b <= ' ' || b >= 127 {
			return false
		}
	}
	return true
}

func (l ExtractionLimits) valid(e ExtractionEligibility) bool {
	if l.MaxImages < 1 || l.MaxImages > 3 ||
		l.MaxImageBytes < 1 || l.MaxImageBytes > 10<<20 ||
		l.MaxTotalImageBytes < l.MaxImageBytes || l.MaxTotalImageBytes > 12<<20 ||
		l.MaxImagePixels < 1 || l.MaxImagePixels > 40000000 ||
		l.MaxTextBytes < 1 || l.MaxTextBytes > 256<<10 ||
		l.MaxInputTokens < 1 || l.MaxInputTokens > 65536 ||
		l.MaxOutputTokens < 1 || l.MaxOutputTokens > 16384 ||
		l.MaxThinkingTokens < 0 || l.MaxThinkingTokens > 16384 {
		return false
	}
	if l.AccountCallsPerDay < 1 || l.SourceCallsPerDay < 1 ||
		l.FleetCallsPerMinute < 1 || l.FleetCallsPerDay < 1 || l.TokensPerMinute < 1 ||
		l.MaxConcurrent < 1 || l.MaxConcurrent > 2 || l.MaxConcurrent > l.FleetCallsPerMinute ||
		l.AccountCallsPerDay > l.FleetCallsPerDay || l.SourceCallsPerDay > l.FleetCallsPerDay ||
		l.FleetCallsPerMinute > l.FleetCallsPerDay ||
		l.FleetCallsPerMinute > e.FreeCallsPerMinute || l.FleetCallsPerDay > e.FreeCallsPerDay ||
		l.TokensPerMinute > e.FreeTokensPerMinute {
		return false
	}
	// Request token bounds are small enough to sum without integer overflow.
	return l.MaxInputTokens+l.MaxOutputTokens+l.MaxThinkingTokens <= l.TokensPerMinute
}
