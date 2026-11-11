package imports

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"time"
)

type PageFetcher interface {
	Fetch(context.Context, string) (RecipePage, error)
}
type WorkerRequest struct {
	OperationID string       `json:"operationId"`
	AccountID   string       `json:"accountId"`
	Source      ImportSource `json:"source"`
	Text        string       `json:"text,omitempty"`
	Images      [][]byte     `json:"images,omitempty"`
}
type WorkerResult struct {
	Private PrivateDraft `json:"private"`
	Model   string       `json:"model,omitempty"`
}
type WorkerReady struct {
	Ready       bool `json:"ready"`
	AIAvailable bool `json:"aiAvailable"`
	Fixture     bool `json:"fixture"`
}
type WorkerOptions struct {
	Ledger     *AdmissionLedger
	Binding    ExtractionBinding
	Policy     func() (ExtractionPolicy, error)
	Engine     ExtractionEngine
	Fetcher    PageFetcher
	ChannelKey []byte
	Now        func() time.Time
	Fixture    bool
}

func WorkerAuthorization(key []byte) string {
	return "Bearer " + base64.RawURLEncoding.EncodeToString(key)
}
func NewWorkerHandler(options WorkerOptions) (http.Handler, error) {
	if options.Ledger == nil || options.Policy == nil || options.Fetcher == nil || len(options.ChannelKey) != 32 {
		return nil, ErrExtractionUnavailable
	}
	if _, err := NewDraftVault(options.ChannelKey); err != nil {
		return nil, ErrExtractionUnavailable
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	authorization := WorkerAuthorization(options.ChannelKey)
	fail := func(w http.ResponseWriter, status int) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"error":"recipe extraction is unavailable"}`)
	}
	write := func(w http.ResponseWriter, value any) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(value)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(authorization)) != 1 {
			fail(w, 401)
			return
		}
		now := options.Now().UTC()
		if r.Method == http.MethodGet && r.URL.Path == "/readyz" {
			ready := options.Ledger.CheckAvailable(r.Context(), now) == nil
			policy, err := options.Policy()
			ai := ready && options.Engine != nil && err == nil && policy.Check(now, options.Binding) == nil
			if !ready {
				fail(w, 503)
				return
			}
			write(w, WorkerReady{Ready: true, AIAvailable: ai, Fixture: options.Fixture})
			return
		}
		if r.Method != http.MethodPost || (r.URL.Path != "/v1/check-page" && r.URL.Path != "/v1/extract") {
			fail(w, 404)
			return
		}
		contentType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if contentType != "application/json" || r.Header.Get("Content-Encoding") != "" {
			fail(w, 400)
			return
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, 12<<20+1))
		if err != nil || len(raw) > 12<<20 {
			fail(w, 400)
			return
		}
		defer clear(raw)
		if _, err := boundedImportJSON(raw, 12<<20, 3<<20); err != nil {
			fail(w, 400)
			return
		}
		var request WorkerRequest
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&request) != nil || !admissionHex(request.OperationID, 16) || !admissionHex(request.AccountID, 16) {
			fail(w, 400)
			return
		}
		defer func() {
			for _, data := range request.Images {
				clear(data)
			}
		}()
		source, err := CanonicalImportSource(request.Source)
		if err != nil {
			fail(w, 400)
			return
		}
		request.Source = source
		if r.URL.Path == "/v1/check-page" {
			if source.Kind != "url" || request.Text != "" || len(request.Images) != 0 {
				fail(w, 400)
				return
			}
			hash := sha256.Sum256([]byte(source.URL))
			address, _ := url.Parse(source.URL)
			admission, err := options.Ledger.AdmitFetch(r.Context(), AdmissionRequest{OperationID: request.OperationID, AccountID: request.AccountID, SourceGroup: address.Hostname(), RequestHash: hex.EncodeToString(hash[:])}, now)
			if err != nil {
				fail(w, 503)
				return
			}
			if !admission.Dispatch {
				fail(w, 409)
				return
			}
			outcome := "ambiguous"
			defer finishWorkerReservation(options.Ledger, admission, &outcome, options.Now)
			page, err := options.Fetcher.Fetch(r.Context(), source.URL)
			if err != nil {
				outcome = "failed"
				fail(w, 422)
				return
			}
			defer clear(page.HTML)
			source.URL = page.URL
			source, err = CanonicalImportSource(source)
			if err != nil {
				outcome = "failed"
				fail(w, 422)
				return
			}
			inspection, err := InspectRecipePage(page, source)
			if err != nil {
				outcome = "failed"
				fail(w, 422)
				return
			}
			private := PrivateDraft{Source: source, Inspection: &inspection}
			if _, err := private.Encode(); err != nil {
				outcome = "failed"
				fail(w, 422)
				return
			}
			outcome = "completed"
			write(w, WorkerResult{Private: private})
			return
		}
		if options.Engine == nil {
			fail(w, 503)
			return
		}
		policy, err := options.Policy()
		if err != nil || policy.Check(now, options.Binding) != nil {
			fail(w, 503)
			return
		}
		if source.Kind == "url" && (request.Text == "" || len(request.Images) != 0) || source.Kind == "photos" && (request.Text != "" || len(request.Images) == 0) {
			fail(w, 400)
			return
		}
		input, err := prepareExtractionInput(ExtractionInput{Text: request.Text, Images: request.Images}, policy.Limits)
		if err != nil {
			fail(w, 400)
			return
		}
		var group string
		if source.Kind == "url" {
			address, _ := url.Parse(source.URL)
			group = address.Hostname()
		} else {
			group = extractionRequestHash(source, input)
		}
		admission, err := options.Ledger.Admit(r.Context(), policy, options.Binding, AdmissionRequest{OperationID: request.OperationID, AccountID: request.AccountID, SourceGroup: group, RequestHash: extractionRequestHash(source, input)}, now)
		if err != nil {
			fail(w, 503)
			return
		}
		if !admission.Dispatch {
			fail(w, 409)
			return
		}
		outcome := "ambiguous"
		defer finishWorkerReservation(options.Ledger, admission, &outcome, options.Now)
		// Reload the operator kill/eligibility policy after reservation, immediately
		// before dispatch. A denied/cancelled reservation is kept, never refunded.
		current, err := options.Policy()
		if err != nil || current.Check(options.Now().UTC(), options.Binding) != nil || !sameExtractionLimits(current.Limits, policy.Limits) || r.Context().Err() != nil {
			fail(w, 503)
			return
		}
		extracted, err := options.Engine.extract(r.Context(), input, policy.Limits)
		if err != nil {
			if r.Context().Err() == nil {
				outcome = "failed"
			}
			fail(w, 422)
			return
		}
		draft, err := NewDraft(extracted, source)
		if err != nil {
			outcome = "failed"
			fail(w, 422)
			return
		}
		outcome = "completed"
		write(w, WorkerResult{Private: PrivateDraft{Source: source, Draft: &draft}, Model: options.Binding.Model})
	}), nil
}
func sameExtractionLimits(a, b ExtractionLimits) bool { return a == b }
func finishWorkerReservation(ledger *AdmissionLedger, reservation AdmissionReservation, outcome *string, now func() time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = ledger.Finish(ctx, reservation, *outcome, now().UTC())
}
