package imports

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"io"
)

// PrivateDraft is encrypted on the server. Neither fallback source text nor
// an operation's original source is included in progress responses.
type PrivateDraft struct {
	Source     ImportSource    `json:"source"`
	Inspection *PageInspection `json:"inspection,omitempty"`
	Draft      *Draft          `json:"draft,omitempty"`
}

func (PrivateDraft) String() string   { return "private import draft" }
func (PrivateDraft) GoString() string { return "private import draft" }

func CanonicalImportSource(source ImportSource) (ImportSource, error) {
	if !safeDraftText(source.Title) {
		return ImportSource{}, ErrImportInput
	}
	return normalizeImportSource(source)
}
func sameImportSource(a, b ImportSource) bool {
	return a.Kind == b.Kind && a.URL == b.URL && a.Title == b.Title && a.ImportedAt.Equal(b.ImportedAt)
}
func (p PrivateDraft) Encode() ([]byte, error) {
	source, err := CanonicalImportSource(p.Source)
	if err != nil || !sameImportSource(source, p.Source) {
		return nil, ErrPrivateDraft
	}
	validDraft := func(d Draft) bool {
		return d.Revision != "" && d.Revision == d.revision() && sameImportSource(d.Source, source)
	}
	if p.Draft != nil && !validDraft(*p.Draft) {
		return nil, ErrPrivateDraft
	}
	if p.Inspection != nil {
		inspection := p.Inspection
		if p.Source.Kind != "url" || len(inspection.Candidates) > 12 || len(inspection.FallbackText) > maxRecipeFallbackBytes || !safeDraftText(inspection.FallbackText) {
			return nil, ErrPrivateDraft
		}
		if len(inspection.Candidates) > 1 && inspection.FallbackText != "" {
			return nil, ErrPrivateDraft
		}
		seen := map[string]bool{}
		for _, candidate := range inspection.Candidates {
			id, err := hex.DecodeString(candidate.ID)
			if err != nil || len(id) != 32 || hex.EncodeToString(id) != candidate.ID || seen[candidate.ID] || !validDraft(candidate.Draft) {
				return nil, ErrPrivateDraft
			}
			seen[candidate.ID] = true
			if candidate.Complete != completeStructuredDraft(candidate.Draft) {
				return nil, ErrPrivateDraft
			}
		}
	}
	data, err := json.Marshal(p)
	if err != nil || len(data) > MaxPrivateDraftBytes {
		return nil, ErrPrivateDraft
	}
	return data, nil
}
func DecodePrivateDraft(raw []byte) (PrivateDraft, error) {
	if len(raw) == 0 || len(raw) > MaxPrivateDraftBytes {
		return PrivateDraft{}, ErrPrivateDraft
	}
	var out PrivateDraft
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&out) != nil {
		return PrivateDraft{}, ErrPrivateDraft
	}
	if _, err := decoder.Token(); err != io.EOF {
		return PrivateDraft{}, ErrPrivateDraft
	}
	if _, err := out.Encode(); err != nil {
		return PrivateDraft{}, ErrPrivateDraft
	}
	return out, nil
}
