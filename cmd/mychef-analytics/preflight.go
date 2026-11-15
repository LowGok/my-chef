package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/LowGok/my-chef/internal/analytics"
	"github.com/LowGok/my-chef/internal/store"
)

type preflightReference struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}
type preflightIdentity struct {
	SourceCommit          string `json:"sourceCommit"`
	Environment           string `json:"environment"`
	AppBinding            string `json:"appBinding"`
	BinarySHA256          string `json:"binarySha256"`
	ConfigSHA256          string `json:"configSha256"`
	APISHA256             string `json:"apiSha256"`
	AndroidArtifactSHA256 string `json:"androidArtifactSha256"`
	IOSArtifactSHA256     string `json:"iosArtifactSha256"`
	NoticeVersion         string `json:"noticeVersion"`
	NoticeSHA256          string `json:"noticeSha256"`
	ScopeSHA256           string `json:"scopeSha256"`
	AssessmentVersion     string `json:"assessmentVersion"`
}
type preflightManifest struct {
	SchemaVersion int                           `json:"schemaVersion"`
	Identity      preflightIdentity             `json:"identity"`
	Evidence      map[string]preflightReference `json:"evidence"`
}
type preflightRecord struct {
	SchemaVersion       int                `json:"schemaVersion"`
	Kind                string             `json:"kind"`
	Status              string             `json:"status"`
	BindingSHA256       string             `json:"bindingSha256"`
	VerifiedUnix        int64              `json:"verifiedUnix"`
	ExpiresUnix         int64              `json:"expiresUnix"`
	Document            preflightReference `json:"document"`
	FullJourneys        *bool              `json:"fullJourneys,omitempty"`
	SpokenAccessibility *bool              `json:"spokenAccessibility,omitempty"`
	Unresolved          *[]string          `json:"unresolved,omitempty"`
}
type preflightResult struct {
	Status  string            `json:"status"`
	Codes   []string          `json:"codes"`
	Digests map[string]string `json:"digests"`
}

type preflightValidity struct {
	clock    func() time.Time
	started  time.Time
	deadline int64
}

func (v *preflightValidity) current() bool {
	end := v.clock().UTC()
	return !v.started.IsZero() && !end.Before(v.started) && end.Unix() < v.deadline
}

var preflightKinds = []string{"controller", "privacy", "providers", "deviceAccess", "nativeAndroid", "nativeIOS", "ci", "storage", "recovery", "implementationReview", "activation"}
var preflightSHA = regexp.MustCompile(`^[a-f0-9]{64}$`)
var preflightSource = regexp.MustCompile(`^[a-f0-9]{40}$`)
var preflightEnvironment = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func strictPreflightJSON(raw []byte, target any) error {
	tokens := json.NewDecoder(bytes.NewReader(raw))
	if uniquePreflightJSON(tokens, 0) != nil {
		return analytics.ErrInput
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return analytics.ErrInput
	}
	if decoder.Decode(new(any)) != io.EOF {
		return analytics.ErrInput
	}
	return nil
}

func uniquePreflightJSON(decoder *json.Decoder, depth int) error {
	if depth > 64 {
		return analytics.ErrInput
	}
	token, err := decoder.Token()
	if err != nil {
		return analytics.ErrInput
	}
	delimiter, container := token.(json.Delim)
	if !container {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			key, err := decoder.Token()
			name, ok := key.(string)
			if err != nil || !ok || seen[strings.ToLower(name)] {
				return analytics.ErrInput
			}
			seen[strings.ToLower(name)] = true
			if uniquePreflightJSON(decoder, depth+1) != nil {
				return analytics.ErrInput
			}
		}
	case '[':
		for decoder.More() {
			if uniquePreflightJSON(decoder, depth+1) != nil {
				return analytics.ErrInput
			}
		}
	default:
		return analytics.ErrInput
	}
	if _, err = decoder.Token(); err != nil {
		return analytics.ErrInput
	}
	return nil
}

func preflightIdentityDigest(value preflightIdentity) string {
	raw, _ := json.Marshal(value)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	raw, _ = json.Marshal(fields)
	return analytics.Digest(raw)
}

func runLivePreflight(ctx context.Context, appPath, configPath, evidencePath, binaryPath, source, environment string, out io.Writer) error {
	result := preflightResult{Status: "blocked", Codes: []string{}, Digests: map[string]string{}}
	inputs := preflightInputs{}
	validity := &preflightValidity{clock: time.Now}
	code := checkLivePreflight(ctx, appPath, configPath, evidencePath, binaryPath, source, environment, inputs, result.Digests, validity)
	if !inputs.unchanged() {
		code = "input_changed"
	}
	if ctx.Err() != nil {
		code = "check_cancelled"
	}
	if code == "" && !validity.current() {
		code = "approval_unavailable"
	}
	if code == "" {
		result.Status = "ready"
	} else {
		result.Codes = append(result.Codes, code)
	}
	if err := json.NewEncoder(out).Encode(result); err != nil {
		return analytics.ErrInput
	}
	if code != "" {
		return analytics.ErrInput
	}
	return nil
}

func checkLivePreflight(ctx context.Context, appPath, configPath, evidencePath, binaryPath, source, environment string, inputs preflightInputs, digests map[string]string, validity *preflightValidity) string {
	if ctx.Err() != nil || !preflightSource.MatchString(source) || !preflightEnvironment.MatchString(environment) {
		return "input_invalid"
	}
	raw, err := inputs.read(evidencePath, 1<<20)
	if err != nil {
		return "manifest_unavailable"
	}
	var manifest preflightManifest
	if strictPreflightJSON(raw, &manifest) != nil || manifest.SchemaVersion != 1 || len(manifest.Evidence) != len(preflightKinds) {
		return "manifest_invalid"
	}
	digests["manifest"] = analytics.Digest(raw)
	identity := manifest.Identity
	if identity.SourceCommit != source || identity.Environment != environment || !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(identity.AppBinding) {
		return "candidate_mismatch"
	}
	for _, hash := range []string{identity.BinarySHA256, identity.ConfigSHA256, identity.APISHA256, identity.AndroidArtifactSHA256,
		identity.IOSArtifactSHA256, identity.NoticeSHA256, identity.ScopeSHA256} {
		if !preflightSHA.MatchString(hash) {
			return "candidate_invalid"
		}
	}
	configRaw, err := inputs.read(configPath, 65536)
	if err != nil || analytics.Digest(configRaw) != identity.ConfigSHA256 {
		return "configuration_mismatch"
	}
	opts, err := analytics.LoadRuntime(configPath)
	defer clear(opts.BackupKey)
	now := validity.clock().UTC()
	validity.started = now
	if err != nil || len(opts.BackupKey) != 32 || !opts.Config.Authorized(now) {
		return "assessment_unavailable"
	}
	validity.deadline = opts.Config.Assessment.ValidUntil
	var runtime analytics.RuntimeFile
	if strictPreflightJSON(configRaw, &runtime) != nil || inputs.observe(runtime.BackupKeyFile, 32) != nil {
		return "secret_storage_unavailable"
	}
	if opts.Config.NoticeVersion != identity.NoticeVersion || opts.Config.NoticeHash != identity.NoticeSHA256 ||
		opts.Config.Assessment.Version != identity.AssessmentVersion || opts.Config.Assessment.Scope != identity.ScopeSHA256 {
		return "assessment_mismatch"
	}
	if privatePreflightPath(opts.Root, true) != nil || !outsidePreflightRoot(opts.Root, opts.PrivacyPath) ||
		!outsidePreflightRoot(opts.Root, opts.AnchorPath) || opts.PrivacyPath == opts.AnchorPath {
		return "storage_path_invalid"
	}
	digests["configuration"] = analytics.Digest(configRaw)
	if inputs.observe(binaryPath, 256<<20) != nil || inputs[binaryPath].digest != identity.BinarySHA256 {
		return "binary_mismatch"
	}
	digests["binary"] = inputs[binaryPath].digest
	bindingDigest := preflightIdentityDigest(identity)
	for _, kind := range preflightKinds {
		reference, exists := manifest.Evidence[kind]
		if !exists || !preflightSHA.MatchString(reference.SHA256) {
			return "evidence_missing"
		}
		raw, err = inputs.read(reference.Path, 65536)
		if err != nil || analytics.Digest(raw) != reference.SHA256 {
			return "evidence_mismatch"
		}
		var record preflightRecord
		if strictPreflightJSON(raw, &record) != nil || record.SchemaVersion != 1 || record.Kind != kind || record.BindingSHA256 != bindingDigest {
			return "evidence_invalid"
		}
		expected := "passed"
		if kind == "controller" || kind == "activation" {
			expected = "approved"
		}
		if record.Status != expected || record.VerifiedUnix <= 0 || record.VerifiedUnix > now.Unix() || record.ExpiresUnix <= now.Unix() ||
			record.ExpiresUnix <= record.VerifiedUnix || record.ExpiresUnix > opts.Config.Assessment.ValidUntil {
			return "approval_unavailable"
		}
		validity.deadline = min(validity.deadline, record.ExpiresUnix)
		native := kind == "nativeAndroid" || kind == "nativeIOS"
		if native && (record.FullJourneys == nil || !*record.FullJourneys || record.SpokenAccessibility == nil || !*record.SpokenAccessibility) ||
			!native && (record.FullJourneys != nil || record.SpokenAccessibility != nil) ||
			kind == "privacy" && (record.Unresolved == nil || len(*record.Unresolved) != 0) || kind != "privacy" && record.Unresolved != nil {
			return "verification_incomplete"
		}
		if inputs.observe(record.Document.Path, 16<<20) != nil || !preflightSHA.MatchString(record.Document.SHA256) || inputs[record.Document.Path].digest != record.Document.SHA256 {
			return "supporting_evidence_mismatch"
		}
		digests[kind] = reference.SHA256
	}
	app, err := inputs.database(ctx, appPath)
	if err != nil {
		return "app_storage_unavailable"
	}
	defer func() { _ = app.Close() }()
	if (&store.Store{SQL: app.DB}).CheckSchema(ctx) != nil {
		return "app_schema_invalid"
	}
	privacy, err := inputs.database(ctx, opts.PrivacyPath)
	if err != nil {
		return "privacy_storage_unavailable"
	}
	defer func() { _ = privacy.Close() }()
	journal, err := inputs.database(ctx, filepath.Join(opts.Root, "journal.sqlite"))
	if err != nil {
		return "journal_unavailable"
	}
	defer func() { _ = journal.Close() }()
	if _, err = inputs.read(opts.AnchorPath, 65536); err != nil || analytics.CheckReadOnlyStorage(ctx, app.DB, privacy.DB, journal.DB, opts, identity.AppBinding, now) != nil {
		return "privacy_state_unavailable"
	}
	if !validity.current() {
		return "approval_unavailable"
	}
	return ""
}
