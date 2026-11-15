// Package analytics contains the first-party, content-free capture contract.
// Configuration is supplied by the operator; requests cannot supply assessment evidence.
package analytics

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

var ErrInput = errors.New("invalid analytics input")
var ErrDisabled = errors.New("analytics collection unavailable")
var ErrConflict = errors.New("analytics preference changed")
var ErrPrivacy = errors.New("analytics privacy reconciliation required")

var events = []string{"preferences_saved", "meal_plan_saved", "shopping_list_edited", "favorite_saved", "feedback_submitted", "personal_recipe_saved", "recipe_import_saved"}

const Scope = "mychef:core-actions:v2;server-only;random-subject;utc-second;event-enum;platform;authorized-plan;events=90d-inclusive;contributions=current-plus-12-utc-months;backup-grace=7d;minimum-group=10;signup-optout"

func ScopeDigest() string    { return Digest([]byte(Scope)) }
func Digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

type Assessment struct {
	Status        string `json:"status"`
	Basis         string `json:"basis"`
	Version       string `json:"version"`
	Scope         string `json:"scope"`
	NoticeVersion string `json:"noticeVersion"`
	Jurisdiction  string `json:"jurisdiction"`
	Audience      string `json:"audience"`
	DeviceAccess  string `json:"deviceAccess"`
	ValidUntil    int64  `json:"validUntil"`
}
type Config struct {
	Enabled       bool
	NoticeVersion string
	NoticeHash    string
	Assessment    Assessment
	OutboxCap     int
}

func (c Config) Authorized(now time.Time) bool {
	a := c.Assessment
	return c.Enabled && c.NoticeVersion != "" && c.NoticeHash == Digest([]byte(NoticeText)) && a.Status == "approved" && a.Basis == "legitimate_interest" && a.Version != "" && a.Scope == ScopeDigest() && a.NoticeVersion == c.NoticeVersion && a.Jurisdiction != "" && a.Audience != "" && a.DeviceAccess != "" && a.ValidUntil > now.Unix()
}
func NewID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(b[:]), nil
}
func ValidID(value, prefix string) bool {
	if len(value) != len(prefix)+33 || value[:len(prefix)+1] != prefix+"_" {
		return false
	}
	b, e := hex.DecodeString(value[len(prefix)+1:])
	return e == nil && len(b) == 16 && hex.EncodeToString(b) == value[len(prefix)+1:]
}

// Event deliberately has no extensible properties or domain-object identifiers.
type Event struct {
	Version  int    `json:"version"`
	ID       string `json:"id"`
	Subject  string `json:"subject"`
	Epoch    string `json:"epoch"`
	Occurred int64  `json:"occurred"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
	Plan     string `json:"plan"`
	Sequence int64  `json:"sequence"`
}

func (e Event) Validate() error {
	if e.Version != 2 || !ValidID(e.ID, "evt") || !ValidID(e.Subject, "sub") || !ValidID(e.Epoch, "per") || e.Occurred <= 0 || e.Sequence <= 0 || !slices.Contains(events, e.Name) || !slices.Contains([]string{"web", "ios", "android"}, e.Platform) || !slices.Contains([]string{"free", "premium"}, e.Plan) {
		return ErrInput
	}
	return nil
}
func (e Event) Digest() string { b, _ := json.Marshal(e); return Digest(b) }

// Capture is server-established request context, never a client event payload.
type Capture struct {
	Revision int64
	Platform string
	Epoch    string
}
type captureKey struct{}
