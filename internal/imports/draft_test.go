package imports

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/domain"
)

func extractionFixture() ExtractedRecipe {
	servings, prep, cook, total := 4, 10, 20, 40
	quantity, unit := domain.Decimal("0.2500"), "kg"
	return ExtractedRecipe{
		Title: "  Chickpea dinner  ", Description: "A simple dinner.",
		Servings: &servings, SourceYield: []string{"4 servings"},
		PrepMinutes: &prep, CookMinutes: &cook, TotalMinutes: &total,
		Ingredients:  []ExtractedIngredient{{Name: "Chickpeas", Quantity: &quantity, Unit: &unit, SourceText: "250 g chickpeas"}},
		Instructions: []string{"Heat the pan.", "Cook gently."}, Allergens: []string{"Mustard"},
	}
}

func sourceFixture() ImportSource {
	return ImportSource{Kind: "url", URL: "https://recipes.example.com/chickpeas?recipe_id=42&utm_source=demo#steps", Title: "Recipe page", ImportedAt: time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)}
}

func reviewedDraft(t *testing.T, d Draft, content domain.PersonalRecipeContent) (ReviewSnapshot, ReviewConfirmation) {
	t.Helper()
	snapshot, err := d.Review(content)
	if err != nil {
		t.Fatal(err)
	}
	resolved := make([]string, len(d.Issues))
	for i, issue := range d.Issues {
		resolved[i] = issue.Field
	}
	return snapshot, ReviewConfirmation{
		DraftRevision: d.Revision, ContentRevision: snapshot.ContentRevision,
		ReviewedFields: append([]string(nil), snapshot.RequiredFields...), ResolvedFields: resolved,
	}
}

func TestImportDraftPreservesExactInputAndRequiresDeliberateReview(t *testing.T) {
	in := extractionFixture()
	before, _ := json.Marshal(in)
	d, err := NewDraft(in, sourceFixture())
	if err != nil {
		t.Fatal(err)
	}
	if d.Source.URL != "https://recipes.example.com/chickpeas?recipe_id=42" || d.Source.Kind != "url" {
		t.Fatal("saved provenance retained tracking or fragment")
	}
	if d.Content.TotalMinutes != 40 || *d.Content.Ingredients[0].Quantity != "0.2500" || d.SourceYield[0] != "4 servings" {
		t.Fatal("extraction changed exact source content")
	}
	if len(d.Issues) != 1 || d.Issues[0].Field != "allergens" || d.Issues[0].Reason != "unverified" {
		t.Fatal("imported allergen declaration must remain unverified")
	}
	if _, err := d.Confirm(d.Content, ReviewConfirmation{}); !errors.Is(err, ErrImportReviewRequired) {
		t.Fatal("unreviewed extraction could be finalized", err)
	}
	snapshot, confirmation := reviewedDraft(t, d, d.Content)
	want := []string{"title", "servings", "prepMinutes", "cookMinutes", "totalMinutes", "allergens", "ingredients.0", "instructions.0", "instructions.1"}
	if !reflect.DeepEqual(snapshot.RequiredFields, want) {
		t.Fatal("review does not cover each important source field")
	}
	accepted, err := d.Confirm(d.Content, confirmation)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Title != "Chickpea dinner" || *accepted.Ingredients[0].Quantity != "0.25" || !strings.HasPrefix(accepted.AllergenNote, ImportedAllergenWarning) {
		t.Fatal("confirmed content bypassed existing personal validation or allergen warning")
	}
	accepted.Ingredients[0].Name = "Edited later"
	*d.Content.Ingredients[0].Quantity = "99"
	d.SourceYield[0] = "Changed"
	after, _ := json.Marshal(in)
	if string(before) != string(after) {
		t.Fatal("working with the draft mutated extraction input")
	}
}

func TestImportDraftKeepsMissingAndAmbiguousValuesVisible(t *testing.T) {
	in := extractionFixture()
	in.Servings, in.PrepMinutes, in.CookMinutes, in.TotalMinutes = nil, nil, nil, nil
	in.SourceYield = []string{"2 adults and 2 children"}
	in.Ingredients[0].Quantity = nil
	in.Ingredients[0].SourceText = "A generous handful of chickpeas"
	in.Uncertain = []DraftIssue{{Field: "ingredients.0.quantity", Reason: "ambiguous", SourceText: in.Ingredients[0].SourceText}}
	d, err := NewDraft(in, sourceFixture())
	if err != nil {
		t.Fatal(err)
	}
	issues := map[string]DraftIssue{}
	for _, issue := range d.Issues {
		issues[issue.Field] = issue
	}
	for _, field := range []string{"servings", "prepMinutes", "cookMinutes", "totalMinutes", "allergens", "ingredients.0.quantity"} {
		if _, exists := issues[field]; !exists {
			t.Fatal("missing review marker for", field)
		}
	}
	if d.Content.Servings != 0 || d.Content.TotalMinutes != 0 || d.Content.Ingredients[0].Quantity != nil || issues["ingredients.0.quantity"].SourceText != in.Ingredients[0].SourceText {
		t.Fatal("ambiguous extraction silently invented a value or lost source text")
	}
	if _, err := d.Review(d.Content); err == nil {
		t.Fatal("missing required values passed personal-recipe validation")
	}
	fixed := d.Content
	fixed.Servings, fixed.PrepMinutes, fixed.CookMinutes, fixed.TotalMinutes = 4, 0, 20, 20
	_, confirmation := reviewedDraft(t, d, fixed)
	accepted, err := d.Confirm(fixed, confirmation)
	if err != nil || accepted.Ingredients[0].Quantity != nil || accepted.PrepMinutes != 0 {
		t.Fatal("deliberately reviewed unknown quantity or explicit zero rejected", err)
	}
}

func TestImportDraftRejectsStaleIncompleteOrForgedReview(t *testing.T) {
	d, err := NewDraft(extractionFixture(), sourceFixture())
	if err != nil {
		t.Fatal(err)
	}
	_, original := reviewedDraft(t, d, d.Content)
	for _, name := range []string{"missing field", "duplicate field", "unknown field", "unresolved issue", "different draft", "changed after review", "changed row count", "wrong revision"} {
		t.Run(name, func(t *testing.T) {
			candidate := d.Content
			confirmation := original
			confirmation.ReviewedFields = append([]string(nil), original.ReviewedFields...)
			switch name {
			case "missing field":
				confirmation.ReviewedFields = confirmation.ReviewedFields[1:]
			case "duplicate field":
				confirmation.ReviewedFields = append(confirmation.ReviewedFields, "title")
			case "unknown field":
				confirmation.ReviewedFields = append(confirmation.ReviewedFields, "providerInstructions")
			case "unresolved issue":
				confirmation.ResolvedFields = nil
			case "different draft":
				confirmation.DraftRevision = strings.Repeat("a", 64)
			case "changed after review":
				candidate.Title = "Changed after approval"
			case "changed row count":
				candidate.Instructions = append(append([]string(nil), candidate.Instructions...), "One more step")
			case "wrong revision":
				confirmation.ContentRevision = strings.Repeat("b", 64)
			}
			if _, err := d.Confirm(candidate, confirmation); !errors.Is(err, ErrImportReviewRequired) {
				t.Fatal("stale or forged review accepted", err)
			}
		})
	}
}

func TestImportDraftBoundsAndDiagnostics(t *testing.T) {
	for _, name := range []string{"too many ingredients", "oversized draft", "bad uncertainty path", "oversized uncertainty", "invalid text", "bad source", "source credentials"} {
		t.Run(name, func(t *testing.T) {
			in, source := extractionFixture(), sourceFixture()
			switch name {
			case "too many ingredients":
				in.Ingredients = make([]ExtractedIngredient, 101)
			case "oversized draft":
				in.Description = strings.Repeat("private-recipe-canary", 20000)
			case "bad uncertainty path":
				in.Uncertain = []DraftIssue{{Field: "../credentials", Reason: "ambiguous", SourceText: "private-recipe-canary"}}
			case "oversized uncertainty":
				in.Uncertain = []DraftIssue{{Field: "title", Reason: "ambiguous", SourceText: strings.Repeat("x", 4001)}}
			case "invalid text":
				in.Title = "private-recipe-canary\x00"
			case "bad source":
				source.Kind = "dish-photo-invention"
			case "source credentials":
				source.URL = "https://user:private-recipe-canary@recipes.example.com/recipe"
			}
			_, err := NewDraft(in, source)
			if !errors.Is(err, ErrImportInput) || strings.Contains(err.Error(), "private-recipe-canary") {
				t.Fatal("unsafe draft accepted or diagnostic exposed content", err)
			}
		})
	}
}

func TestImportDraftPhotosHaveNoSourceImageOrInventedLink(t *testing.T) {
	source := sourceFixture()
	source.Kind, source.URL, source.Title = "photos", "", ""
	d, err := NewDraft(extractionFixture(), source)
	if err != nil || d.Source.URL != "" {
		t.Fatal("photo extraction invented a source URL", err)
	}
	source.URL = "https://recipes.example.com/assumed-source"
	if _, err := NewDraft(extractionFixture(), source); !errors.Is(err, ErrImportInput) {
		t.Fatal("photo extraction accepted an invented source link", err)
	}
}

func TestImportDraftRejectsOversizedAutomaticallyCreatedIssue(t *testing.T) {
	in := extractionFixture()
	in.Ingredients[0].Quantity = nil
	in.Ingredients[0].SourceText = strings.Repeat("x", 4001)
	if _, err := NewDraft(in, sourceFixture()); !errors.Is(err, ErrImportInput) {
		t.Fatal("oversized automatic uncertainty text accepted", err)
	}
}

func TestImportDraftRejectsUnserializableSourceTime(t *testing.T) {
	source := sourceFixture()
	source.ImportedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := NewDraft(extractionFixture(), source); !errors.Is(err, ErrImportInput) {
		t.Fatal("draft without a serializable revision accepted", err)
	}
}

func TestImportSourceRejectsSensitiveOrUnsupportedLinks(t *testing.T) {
	for _, raw := range []string{
		"http://recipes.example.com/recipe",
		"https://recipes.example.com:8443/recipe",
		"https://recipes.example.com/recipe?access_token=private-canary",
		"https://recipes.example.com/recipe?recipe_id=1&recipe_id=2",
		"https://recipes.example.com/recipe?recipe_id=header.payload.signature",
		"https://recipes.example.com/%00recipe",
	} {
		source := sourceFixture()
		source.URL = raw
		if _, err := NewDraft(extractionFixture(), source); !errors.Is(err, ErrImportInput) {
			t.Fatal("unsafe source accepted", err)
		}
	}
}

func TestImportDraftPartialCollectionsRemainJSONArrays(t *testing.T) {
	draft, err := NewDraft(ExtractedRecipe{Title: "Partial recipe"}, sourceFixture())
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(draft)
	if err != nil {
		t.Fatal(err)
	}
	var value struct {
		SourceYield []string                                         `json:"sourceYield"`
		Content     struct{ Instructions, Tips, Allergens []string } `json:"content"`
	}
	if json.Unmarshal(data, &value) != nil || value.SourceYield == nil || value.Content.Instructions == nil || value.Content.Tips == nil || value.Content.Allergens == nil {
		t.Fatal("partial draft collections violate the editor array contract")
	}
}
