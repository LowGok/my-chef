package imports

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/LowGok/my-chef/internal/domain"
)

var (
	ErrImportInput          = errors.New("recipe import input is invalid")
	ErrImportReviewRequired = errors.New("review the imported recipe before saving")
)

const ImportedAllergenWarning = "Imported allergen information is unverified. Check ingredients, labels and cross-contamination."

// ExtractedRecipe preserves absent numeric values separately from explicit zero.
// It is extraction data, never an instruction to create a library recipe.
type ExtractedRecipe struct {
	Title, Description, Equipment          string
	Servings                               *int
	SourceYield                            []string
	PrepMinutes, CookMinutes, TotalMinutes *int
	Ingredients                            []ExtractedIngredient
	Instructions, Tips, Allergens          []string
	AllergenNote                           string
	Uncertain                              []DraftIssue
}

type ExtractedIngredient struct {
	Name       string
	Quantity   *domain.Decimal
	Unit       *string
	SourceText string
}

type ImportSource struct {
	Kind       string    `json:"kind"`
	URL        string    `json:"url,omitempty"`
	Title      string    `json:"title,omitempty"`
	ImportedAt time.Time `json:"importedAt"`
}

type DraftIssue struct {
	Field      string `json:"field"`
	Reason     string `json:"reason"`
	SourceText string `json:"sourceText,omitempty"`
}

type Draft struct {
	Content     domain.PersonalRecipeContent `json:"content"`
	Source      ImportSource                 `json:"source"`
	SourceYield []string                     `json:"sourceYield"`
	Issues      []DraftIssue                 `json:"issues"`
	Revision    string                       `json:"revision"`
}

type ReviewSnapshot struct {
	Content         domain.PersonalRecipeContent `json:"content"`
	ContentRevision string                       `json:"contentRevision"`
	RequiredFields  []string                     `json:"requiredFields"`
}

type ReviewConfirmation struct {
	DraftRevision   string   `json:"draftRevision"`
	ContentRevision string   `json:"contentRevision"`
	ReviewedFields  []string `json:"reviewedFields"`
	ResolvedFields  []string `json:"resolvedFields"`
}

func NewDraft(in ExtractedRecipe, source ImportSource) (Draft, error) {
	if len(in.Ingredients) > 100 || len(in.Instructions) > 100 || len(in.Tips) > 30 ||
		len(in.Allergens) > 30 || len(in.SourceYield) > 30 || len(in.Uncertain) > 500 || !extractedTextValid(in, source) {
		return Draft{}, ErrImportInput
	}
	encoded, err := json.Marshal(in)
	if err != nil || len(encoded) > domain.PersonalRecipePayloadLimit {
		return Draft{}, ErrImportInput
	}
	// Detach every slice and pointer before handing a mutable draft to an editor.
	var captured ExtractedRecipe
	if err := json.Unmarshal(encoded, &captured); err != nil {
		return Draft{}, ErrImportInput
	}
	source, err = normalizeImportSource(source)
	if err != nil {
		return Draft{}, err
	}
	d := Draft{Source: source, SourceYield: append([]string{}, captured.SourceYield...), Issues: []DraftIssue{},
		Content: domain.PersonalRecipeContent{SchemaVersion: 1, Title: captured.Title,
			Description: captured.Description, Equipment: captured.Equipment, Instructions: append([]string{}, captured.Instructions...),
			Tips: append([]string{}, captured.Tips...), Allergens: append([]string{}, captured.Allergens...), AllergenNote: captured.AllergenNote,
			Ingredients: []domain.IngredientContent{}},
	}
	add := func(issue DraftIssue) {
		for i, previous := range d.Issues {
			if previous.Field == issue.Field {
				if previous.SourceText == "" && issue.SourceText != "" {
					d.Issues[i].SourceText = issue.SourceText
				}
				return
			}
		}
		d.Issues = append(d.Issues, issue)
	}
	for _, issue := range captured.Uncertain {
		if !validIssue(issue, len(captured.Ingredients), len(captured.Instructions)) {
			return Draft{}, ErrImportInput
		}
		if issue.Field != "allergens" {
			add(issue)
		}
	}
	for _, field := range []struct {
		name string
		from *int
		to   *int
	}{
		{"servings", captured.Servings, &d.Content.Servings},
		{"prepMinutes", captured.PrepMinutes, &d.Content.PrepMinutes},
		{"cookMinutes", captured.CookMinutes, &d.Content.CookMinutes},
		{"totalMinutes", captured.TotalMinutes, &d.Content.TotalMinutes},
	} {
		if field.from == nil {
			add(DraftIssue{Field: field.name, Reason: "missing"})
		} else {
			*field.to = *field.from
		}
	}
	for i, item := range captured.Ingredients {
		d.Content.Ingredients = append(d.Content.Ingredients, domain.IngredientContent{Name: item.Name, Quantity: item.Quantity, Unit: item.Unit})
		if item.Quantity == nil && item.SourceText != "" {
			add(DraftIssue{Field: "ingredients." + strconv.Itoa(i) + ".quantity", Reason: "ambiguous", SourceText: item.SourceText})
		}
	}
	if _, err := d.Content.Normalize(); err != nil {
		var invalid *domain.PersonalRecipeValidationError
		if !errors.As(err, &invalid) {
			return Draft{}, ErrImportInput
		}
		// Sort paths so an equivalent extraction always has the same revision.
		for _, field := range sortedKeys(invalid.Fields) {
			add(DraftIssue{Field: field, Reason: "invalid"})
		}
	}
	add(DraftIssue{Field: "allergens", Reason: "unverified", SourceText: captured.AllergenNote})
	d.Revision = d.revision()
	if d.Revision == "" {
		return Draft{}, ErrImportInput
	}
	return d, nil
}

// Review validates edited content without writing or calling a provider. The API
// may return this snapshot before collecting the user's explicit confirmation.
func (d Draft) Review(candidate domain.PersonalRecipeContent) (ReviewSnapshot, error) {
	if d.Revision == "" || d.revision() != d.Revision {
		return ReviewSnapshot{}, ErrImportReviewRequired
	}
	if !strings.HasPrefix(candidate.AllergenNote, ImportedAllergenWarning) {
		candidate.AllergenNote = strings.TrimSpace(ImportedAllergenWarning + " " + candidate.AllergenNote)
	}
	normalized, err := candidate.Normalize()
	if err != nil {
		return ReviewSnapshot{}, err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return ReviewSnapshot{}, ErrImportInput
	}
	digest := sha256.Sum256(encoded)
	required := []string{"title", "servings", "prepMinutes", "cookMinutes", "totalMinutes", "allergens"}
	for i := range normalized.Ingredients {
		required = append(required, "ingredients."+strconv.Itoa(i))
	}
	for i := range normalized.Instructions {
		required = append(required, "instructions."+strconv.Itoa(i))
	}
	return ReviewSnapshot{Content: normalized, ContentRevision: hex.EncodeToString(digest[:]), RequiredFields: required}, nil
}

// Confirm only returns normalized content. A separate owner-checked transaction
// must still perform the user's explicit Save and attach provenance atomically.
func (d Draft) Confirm(candidate domain.PersonalRecipeContent, confirmation ReviewConfirmation) (domain.PersonalRecipeContent, error) {
	snapshot, err := d.Review(candidate)
	if err != nil {
		return domain.PersonalRecipeContent{}, err
	}
	issues := make([]string, len(d.Issues))
	for i, issue := range d.Issues {
		issues[i] = issue.Field
	}
	if confirmation.DraftRevision != d.Revision || confirmation.ContentRevision != snapshot.ContentRevision ||
		!sameUniqueFields(confirmation.ReviewedFields, snapshot.RequiredFields) || !sameUniqueFields(confirmation.ResolvedFields, issues) {
		return domain.PersonalRecipeContent{}, ErrImportReviewRequired
	}
	return snapshot.Content, nil
}

func (d Draft) revision() string {
	d.Revision = ""
	encoded, err := json.Marshal(d)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func sameUniqueFields(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := make(map[string]bool, len(want))
	for _, field := range want {
		seen[field] = true
	}
	for _, field := range got {
		if !seen[field] {
			return false
		}
		delete(seen, field)
	}
	return len(seen) == 0
}

func validIssue(issue DraftIssue, ingredients, instructions int) bool {
	if len(issue.SourceText) > 16000 || utf8.RuneCountInString(issue.SourceText) > 4000 ||
		(issue.Reason != "missing" && issue.Reason != "ambiguous" && issue.Reason != "unverified" && issue.Reason != "invalid") {
		return false
	}
	switch issue.Field {
	case "title", "description", "equipment", "servings", "prepMinutes", "cookMinutes", "totalMinutes", "ingredients", "instructions", "allergens", "allergenNote", "tips":
		return true
	}
	parts := strings.Split(issue.Field, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return false
	}
	i, err := strconv.Atoi(parts[1])
	if err != nil || strconv.Itoa(i) != parts[1] || i < 0 {
		return false
	}
	if parts[0] == "ingredients" && i < ingredients {
		return len(parts) == 2 || parts[2] == "name" || parts[2] == "quantity" || parts[2] == "unit"
	}
	return parts[0] == "instructions" && i < instructions && len(parts) == 2
}

func extractedTextValid(in ExtractedRecipe, source ImportSource) bool {
	values := []string{in.Title, in.Description, in.Equipment, in.AllergenNote, source.Kind, source.URL, source.Title}
	values = append(values, in.SourceYield...)
	values = append(values, in.Instructions...)
	values = append(values, in.Tips...)
	values = append(values, in.Allergens...)
	for _, item := range in.Ingredients {
		values = append(values, item.Name, item.SourceText)
		if len(item.SourceText) > 16000 || utf8.RuneCountInString(item.SourceText) > 4000 {
			return false
		}
		if item.Quantity != nil {
			values = append(values, string(*item.Quantity))
		}
		if item.Unit != nil {
			values = append(values, *item.Unit)
		}
	}
	for _, issue := range in.Uncertain {
		values = append(values, issue.Field, issue.Reason, issue.SourceText)
	}
	remaining := domain.PersonalRecipePayloadLimit
	for _, value := range values {
		if len(value) > remaining || !safeDraftText(value) {
			return false
		}
		remaining -= len(value)
	}
	return true
}

func safeDraftText(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return false
		}
	}
	return true
}

var sourceIdentifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,119}$`)

func normalizeImportSource(source ImportSource) (ImportSource, error) {
	if source.ImportedAt.IsZero() || len(source.URL) > 4096 || utf8.RuneCountInString(source.Title) > 200 {
		return ImportSource{}, ErrImportInput
	}
	source.ImportedAt = source.ImportedAt.UTC()
	source.Title = strings.TrimSpace(source.Title)
	if source.Kind == "photos" {
		if source.URL != "" || source.Title != "" {
			return ImportSource{}, ErrImportInput
		}
		return source, nil
	}
	if source.Kind != "url" {
		return ImportSource{}, ErrImportInput
	}
	u, err := normalizedImportURL(source.URL)
	if err != nil {
		return ImportSource{}, ErrImportInput
	}
	source.URL = u.String()
	return source, nil
}

func normalizedImportURL(raw string) (*url.URL, error) {
	if len(raw) == 0 || len(raw) > 4096 || !safeDraftText(raw) {
		return nil, ErrImportInput
	}
	u, err := url.Parse(raw)
	if err != nil || strings.ToLower(u.Scheme) != "https" || u.Hostname() == "" || u.User != nil || u.Opaque != "" ||
		(u.Port() != "" && u.Port() != "443") || !safeDraftText(u.Path) || strings.ContainsAny(u.Path, "\x00\r\n\t\\") {
		return nil, ErrImportInput
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, ErrImportInput
	}
	for key, values := range query {
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "utm_") || lower == "fbclid" || lower == "gclid" || lower == "msclkid" {
			query.Del(key)
			continue
		}
		if (lower != "id" && lower != "recipe_id" && lower != "recipeid" && lower != "recipe" && lower != "slug" && lower != "page") ||
			len(values) != 1 || !sourceIdentifier.MatchString(values[0]) {
			return nil, ErrImportInput
		}
	}
	u.Fragment, u.RawFragment = "", ""
	u.RawQuery, u.ForceQuery = query.Encode(), false
	u.Scheme = "https"
	u.Host = strings.ToLower(u.Host)
	if len(u.String()) > 4096 {
		return nil, ErrImportInput
	}
	return u, nil
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
