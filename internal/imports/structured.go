package imports

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"
)

var ErrImportSelectionRequired = errors.New("choose one recipe from this page")

const maxRecipeFallbackBytes = 64 << 10

type StructuredCandidate struct {
	ID       string `json:"id"`
	Draft    Draft  `json:"draft"`
	Complete bool   `json:"complete"`
}

type PageInspection struct {
	Candidates        []StructuredCandidate `json:"candidates"`
	FallbackText      string                `json:"fallbackText,omitempty"`
	StructuredInvalid bool                  `json:"structuredInvalid"`
}

// InspectRecipePage reads inert local bytes only. It neither fetches JSON-LD
// contexts/images nor invokes AI, and returns unsaved private draft candidates.
func InspectRecipePage(page RecipePage, source ImportSource) (PageInspection, error) {
	if len(page.HTML) == 0 || len(page.HTML) > maxRecipeHTMLBytes || !utf8.Valid(page.HTML) || source.Kind != "url" {
		return PageInspection{}, ErrImportInput
	}
	source.URL = page.URL
	if _, err := normalizeImportSource(source); err != nil {
		return PageInspection{}, ErrImportInput
	}
	scan, err := scanRecipeHTML(page.HTML, true)
	if err != nil {
		return PageInspection{}, ErrImportInput
	}
	collector := structuredCollector{source: source, seen: make(map[string]bool), candidates: []StructuredCandidate{}, invalid: scan.invalid}
	for _, script := range scan.scripts {
		value, err := boundedRecipeJSON(script)
		if err != nil {
			collector.invalid = true
			continue
		}
		if err := collector.visit(value); err != nil {
			return PageInspection{}, err
		}
	}
	result := PageInspection{Candidates: collector.candidates, StructuredInvalid: collector.invalid}
	// Several recipes are never combined into a single AI input. The user can
	// select a structured draft and correct it manually instead.
	if len(result.Candidates) == 0 || (len(result.Candidates) == 1 && !result.Candidates[0].Complete) {
		result.FallbackText = scan.text
	}
	return result, nil
}

func (p PageInspection) Select(id string) (Draft, error) {
	for _, candidate := range p.Candidates {
		if candidate.ID == id || (id == "" && len(p.Candidates) == 1) {
			encoded, err := json.Marshal(candidate.Draft)
			if err != nil {
				return Draft{}, ErrImportInput
			}
			var detached Draft
			if err := json.Unmarshal(encoded, &detached); err != nil {
				return Draft{}, ErrImportInput
			}
			return detached, nil
		}
	}
	return Draft{}, ErrImportSelectionRequired
}

type structuredCollector struct {
	source     ImportSource
	seen       map[string]bool
	candidates []StructuredCandidate
	invalid    bool
}

func (c *structuredCollector) visit(value any) error {
	switch value := value.(type) {
	case map[string]any:
		if schemaType(value["@type"], "Recipe") {
			extracted, err := recipeFromObject(value)
			if err != nil {
				c.invalid = true
				return nil
			}
			draft, err := NewDraft(extracted, c.source)
			if err != nil {
				c.invalid = true
				return nil
			}
			encoded, err := json.Marshal(extracted)
			if err != nil {
				return ErrImportInput
			}
			digest := sha256.Sum256(encoded)
			id := hex.EncodeToString(digest[:])
			if !c.seen[id] {
				if len(c.candidates) >= 12 {
					return ErrImportInput
				}
				c.seen[id] = true
				c.candidates = append(c.candidates, StructuredCandidate{ID: id, Draft: draft, Complete: completeStructuredDraft(draft)})
			}
			return nil
		}
		for _, key := range slices.Sorted(maps.Keys(value)) {
			if key == "@context" {
				continue
			}
			if err := c.visit(value[key]); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range value {
			if err := c.visit(item); err != nil {
				return err
			}
		}
	}
	return nil
}

func schemaType(value any, want string) bool {
	switch value := value.(type) {
	case string:
		return value == want || value == "https://schema.org/"+want || value == "http://schema.org/"+want
	case []any:
		for _, item := range value {
			if schemaType(item, want) {
				return true
			}
		}
	}
	return false
}

func completeStructuredDraft(draft Draft) bool {
	if _, err := draft.Review(draft.Content); err != nil {
		return false
	}
	for _, issue := range draft.Issues {
		if issue.Field != "allergens" || issue.Reason != "unverified" {
			return false
		}
	}
	return true
}

func boundedRecipeJSON(raw []byte) (any, error) {
	return boundedImportJSON(raw, 256<<10, 16000)
}
func boundedImportJSON(raw []byte, maxBytes, maxString int) (any, error) {
	if len(raw) == 0 || len(raw) > maxBytes || !utf8.Valid(raw) {
		return nil, ErrImportInput
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	budget := 10000
	value, err := readRecipeJSON(decoder, 0, &budget, maxString)
	if err != nil {
		return nil, ErrImportInput
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, ErrImportInput
	}
	return value, nil
}

func readRecipeJSON(decoder *json.Decoder, depth int, budget *int, maxString int) (any, error) {
	if depth > 32 || *budget <= 0 {
		return nil, ErrImportInput
	}
	*budget--
	token, err := decoder.Token()
	if err != nil {
		return nil, ErrImportInput
	}
	switch token := token.(type) {
	case json.Delim:
		switch token {
		case '{':
			object := map[string]any{}
			for decoder.More() {
				if len(object) >= 128 {
					return nil, ErrImportInput
				}
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, ErrImportInput
				}
				key, ok := keyToken.(string)
				if !ok || len(key) > 256 || strings.ContainsRune(key, '\ufffd') {
					return nil, ErrImportInput
				}
				if _, exists := object[key]; exists {
					return nil, ErrImportInput
				}
				value, err := readRecipeJSON(decoder, depth+1, budget, maxString)
				if err != nil {
					return nil, err
				}
				object[key] = value
			}
			if close, err := decoder.Token(); err != nil || close != json.Delim('}') {
				return nil, ErrImportInput
			}
			return object, nil
		case '[':
			items := []any{}
			for decoder.More() {
				if len(items) >= 1024 {
					return nil, ErrImportInput
				}
				value, err := readRecipeJSON(decoder, depth+1, budget, maxString)
				if err != nil {
					return nil, err
				}
				items = append(items, value)
			}
			if close, err := decoder.Token(); err != nil || close != json.Delim(']') {
				return nil, ErrImportInput
			}
			return items, nil
		default:
			return nil, ErrImportInput
		}
	case string:
		if len(token) > maxString || strings.ContainsRune(token, '\ufffd') {
			return nil, ErrImportInput
		}
		return token, nil
	case json.Number:
		if len(token) > 100 {
			return nil, ErrImportInput
		}
		return token, nil
	case bool, nil:
		return token, nil
	default:
		return nil, ErrImportInput
	}
}
