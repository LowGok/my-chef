package domain

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const PersonalRecipePayloadLimit = 256 << 10

// PersonalRecipeContent is independently validated user-authored content.
// Generated recipes still use RecipeContent.Validate and household exclusions.
type PersonalRecipeContent struct {
	SchemaVersion int                 `json:"schemaVersion"`
	Title         string              `json:"title"`
	Description   string              `json:"description"`
	Servings      int                 `json:"servings"`
	Equipment     string              `json:"equipment"`
	PrepMinutes   int                 `json:"prepMinutes"`
	CookMinutes   int                 `json:"cookMinutes"`
	TotalMinutes  int                 `json:"totalMinutes"`
	Ingredients   []IngredientContent `json:"ingredients"`
	Instructions  []string            `json:"instructions"`
	Tips          []string            `json:"tips"`
	Allergens     []string            `json:"allergens"`
	AllergenNote  string              `json:"allergenNote"`
}

type PersonalRecipeValidationError struct {
	Fields map[string]string `json:"fields"`
}

func (*PersonalRecipeValidationError) Error() string { return "Check the recipe fields." }

func personalText(raw string, max int, required, multiline bool) (string, bool) {
	if !utf8.ValidString(raw) {
		return "", false
	}
	if multiline {
		raw = strings.ReplaceAll(strings.ReplaceAll(raw, "\r\n", "\n"), "\r", "\n")
	}
	for _, r := range raw {
		if unicode.IsControl(r) && (!multiline || r != '\n') {
			return "", false
		}
	}
	value := strings.TrimSpace(raw)
	return value, (!required || value != "") && utf8.RuneCountInString(value) <= max
}

func (in PersonalRecipeContent) Normalize() (PersonalRecipeContent, error) {
	out := in
	fields := map[string]string{}
	text := func(path, value string, max int, required, multiline bool) string {
		normalized, valid := personalText(value, max, required, multiline)
		if !valid {
			fields[path] = "Check this field and its length."
		}
		return normalized
	}
	if in.SchemaVersion != 1 {
		fields["schemaVersion"] = "This recipe format is unsupported."
	}
	out.Title = text("title", in.Title, 200, true, false)
	out.Description = text("description", in.Description, 4000, false, true)
	out.Equipment = text("equipment", in.Equipment, 2000, false, true)
	out.AllergenNote = text("allergenNote", in.AllergenNote, 4000, false, true)
	if in.Servings < 1 || in.Servings > 40 {
		fields["servings"] = "Choose 1 to 40 servings."
	}
	if in.PrepMinutes < 0 || in.PrepMinutes > 1440 {
		fields["prepMinutes"] = "Choose 0 to 1440 minutes."
	}
	if in.CookMinutes < 0 || in.CookMinutes > 1440 {
		fields["cookMinutes"] = "Choose 0 to 1440 minutes."
	}
	if in.TotalMinutes < 1 || in.TotalMinutes > 2880 || in.PrepMinutes > in.TotalMinutes || in.CookMinutes > in.TotalMinutes-in.PrepMinutes {
		fields["totalMinutes"] = "Total time must include preparation and cooking, up to 2880 minutes."
	}
	out.Ingredients = []IngredientContent{}
	if len(in.Ingredients) < 1 || len(in.Ingredients) > 100 {
		fields["ingredients"] = "Add 1 to 100 ingredients."
	} else {
		for i, ingredient := range in.Ingredients {
			path := fmt.Sprintf("ingredients.%d.", i)
			item := IngredientContent{Name: text(path+"name", ingredient.Name, 200, true, false)}
			if ingredient.Quantity != nil {
				value, valid := personalText(string(*ingredient.Quantity), 100, true, false)
				quantity := Decimal(value)
				if !valid || !quantity.Valid() {
					fields[path+"quantity"] = "Use an exact nonnegative decimal of at most 100 characters."
				} else {
					if strings.Contains(string(quantity), ".") {
						quantity = Decimal(strings.TrimSuffix(strings.TrimRight(string(quantity), "0"), "."))
					}
					item.Quantity = &quantity
				}
			}
			if ingredient.Unit != nil {
				unit := text(path+"unit", *ingredient.Unit, 80, false, false)
				if unit != "" {
					item.Unit = &unit
				}
			}
			out.Ingredients = append(out.Ingredients, item)
		}
	}
	list := func(path string, values []string, min, count, size int, multiline bool) []string {
		result := []string{}
		if len(values) < min || len(values) > count {
			fields[path] = "Check the number of rows."
			return result
		}
		for i, value := range values {
			result = append(result, text(fmt.Sprintf("%s.%d", path, i), value, size, true, multiline))
		}
		return result
	}
	out.Instructions = list("instructions", in.Instructions, 1, 100, 4000, true)
	out.Tips = list("tips", in.Tips, 0, 30, 1000, true)
	out.Allergens = list("allergens", in.Allergens, 0, 30, 200, false)
	encoded, err := json.Marshal(out)
	if err != nil || len(encoded) > PersonalRecipePayloadLimit {
		fields["recipe"] = "The recipe is too large."
	}
	if len(fields) != 0 {
		return PersonalRecipeContent{}, &PersonalRecipeValidationError{Fields: fields}
	}
	return out, nil
}

// RecipeContent converts validated personal content into a stable meal snapshot.
// It does not assert that user-entered ingredients satisfy household exclusions.
func (r PersonalRecipeContent) RecipeContent() RecipeContent {
	note := r.AllergenNote
	if note == "" {
		note = "Allergens not specified. Check ingredient labels and cross-contamination."
		if len(r.Allergens) != 0 {
			note = "User-entered allergens. Check ingredient labels and cross-contamination."
		}
	}
	return RecipeContent{
		Title: r.Title, Note: r.Description, Yield: []string{strconv.Itoa(r.Servings) + " servings"},
		Equipment: r.Equipment, PrepMinutes: r.PrepMinutes, CookMinutes: r.CookMinutes, TotalMinutes: r.TotalMinutes,
		Ingredients: clonePersonalIngredients(r.Ingredients), Instructions: slices.Clone(r.Instructions),
		Tips: slices.Clone(r.Tips), Allergens: slices.Clone(r.Allergens), AllergenNote: note,
	}
}

func clonePersonalIngredients(items []IngredientContent) []IngredientContent {
	out := make([]IngredientContent, 0, len(items))
	for _, item := range items {
		if item.Quantity != nil {
			quantity := *item.Quantity
			item.Quantity = &quantity
		}
		if item.Unit != nil {
			unit := *item.Unit
			item.Unit = &unit
		}
		out = append(out, item)
	}
	return out
}

type PersonalRecipeCopyDraft struct {
	Content       PersonalRecipeContent `json:"content"`
	SourceYield   []string              `json:"sourceYield"`
	NeedsServings bool                  `json:"needsServings"`
}

var personalServings = regexp.MustCompile(`(?i)^([1-9][0-9]?)(?:\s+(?:servings?|portions?))?$`)

// PersonalRecipeDraft preserves source values; Normalize is called after review.
func PersonalRecipeDraft(source Recipe) PersonalRecipeCopyDraft {
	text := func(value *string) string {
		if value != nil {
			return *value
		}
		return ""
	}
	minutes := func(value *int) int {
		if value != nil {
			return *value
		}
		return 0
	}
	content := PersonalRecipeContent{
		SchemaVersion: 1, Title: source.Title, Description: text(source.Note), Equipment: text(source.Equipment),
		PrepMinutes: minutes(source.PrepMinutes), CookMinutes: minutes(source.CookMinutes), TotalMinutes: minutes(source.TotalMinutes),
		Ingredients: []IngredientContent{}, Instructions: slices.Clone(source.Instructions),
		Tips: slices.Clone(source.Tips), Allergens: slices.Clone(source.Allergens), AllergenNote: text(source.AllergenNote),
	}
	for _, item := range source.Ingredients {
		copy := IngredientContent{Name: item.Name}
		if item.Quantity != nil {
			quantity := Decimal(*item.Quantity)
			copy.Quantity = &quantity
		}
		if item.Unit != nil {
			unit := *item.Unit
			copy.Unit = &unit
		}
		content.Ingredients = append(content.Ingredients, copy)
	}
	if len(source.Yield) == 1 {
		if matched := personalServings.FindStringSubmatch(strings.TrimSpace(source.Yield[0])); len(matched) == 2 {
			servings, err := strconv.Atoi(matched[1])
			if err == nil && servings <= 40 {
				content.Servings = servings
			}
		}
	}
	return PersonalRecipeCopyDraft{Content: content, SourceYield: slices.Clone(source.Yield), NeedsServings: content.Servings == 0}
}
