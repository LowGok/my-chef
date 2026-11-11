package imports

import (
	"encoding/json"
	"math/big"
	"regexp"
	"strconv"
	"strings"

	"github.com/LowGok/my-chef/internal/domain"
)

func recipeFromObject(object map[string]any) (ExtractedRecipe, error) {
	out := ExtractedRecipe{Title: recipeValueText(object["name"]), Description: recipeValueText(object["description"]),
		Equipment: strings.Join(recipeStringList(object["tool"]), ", "), AllergenNote: recipeValueText(object["allergenNote"]),
		Ingredients: []ExtractedIngredient{}, Uncertain: []DraftIssue{}}
	out.SourceYield = recipeStringList(object["recipeYield"])
	if len(out.SourceYield) == 1 {
		copy := domain.PersonalRecipeDraft(domain.Recipe{Yield: out.SourceYield})
		if !copy.NeedsServings {
			value := copy.Content.Servings
			out.Servings = &value
		}
	}
	if out.Servings == nil {
		if number, ok := object["recipeYield"].(json.Number); ok && recipeNumericYield.MatchString(string(number)) {
			integer, _, _ := strings.Cut(string(number), ".")
			if servings, err := strconv.Atoi(integer); err == nil && servings >= 1 && servings <= 40 {
				out.Servings = &servings
			}
		}
	}
	for _, field := range []struct {
		name, key   string
		destination **int
	}{
		{"prepMinutes", "prepTime", &out.PrepMinutes}, {"cookMinutes", "cookTime", &out.CookMinutes}, {"totalMinutes", "totalTime", &out.TotalMinutes},
	} {
		value := object[field.key]
		if value == nil {
			continue
		}
		minutes := recipeDurationMinutes(recipeValueText(value))
		*field.destination = minutes
		if minutes == nil {
			out.Uncertain = append(out.Uncertain, DraftIssue{Field: field.name, Reason: "ambiguous", SourceText: recipeValueText(value)})
		}
	}
	ingredients := recipeStringList(object["recipeIngredient"])
	if len(ingredients) > 100 {
		return ExtractedRecipe{}, ErrImportInput
	}
	for _, line := range ingredients {
		out.Ingredients = append(out.Ingredients, ingredientFromText(line))
	}
	instructions, issues, err := recipeInstructionSteps(object["recipeInstructions"], 0)
	if err != nil {
		return ExtractedRecipe{}, err
	}
	out.Instructions = instructions
	out.Uncertain = append(out.Uncertain, issues...)
	out.Allergens = recipeStringList(object["allergens"])
	return out, nil
}

func recipeValueText(value any) string {
	switch value := value.(type) {
	case string:
		return plainRecipeText(value)
	case json.Number:
		return string(value)
	case map[string]any:
		if text, ok := value["@value"].(string); ok {
			return plainRecipeText(text)
		}
		if text, ok := value["name"].(string); ok {
			return plainRecipeText(text)
		}
	}
	return ""
}

func recipeStringList(value any) []string {
	out := []string{}
	if items, ok := value.([]any); ok {
		for _, item := range items {
			if text := recipeValueText(item); text != "" {
				out = append(out, text)
			}
		}
	} else if text := recipeValueText(value); text != "" {
		out = append(out, text)
	}
	return out
}

var recipeNumericYield = regexp.MustCompile(`^[1-9][0-9]?(?:\.0+)?$`)

var recipeDuration = regexp.MustCompile(`^P(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)S)?)?$`)

func recipeDurationMinutes(value string) *int {
	matched := recipeDuration.FindStringSubmatch(strings.TrimSpace(value))
	if len(matched) != 5 || value == "P" || value == "PT" {
		return nil
	}
	seconds, fields := 0, 0
	for i, multiplier := range []int{86400, 3600, 60, 1} {
		if matched[i+1] == "" {
			continue
		}
		part, err := strconv.Atoi(matched[i+1])
		if err != nil || part > 172800/multiplier {
			return nil
		}
		seconds += part * multiplier
		fields++
	}
	if fields == 0 || seconds > 172800 || seconds%60 != 0 {
		return nil
	}
	minutes := seconds / 60
	return &minutes
}

func recipeInstructionSteps(value any, depth int) ([]string, []DraftIssue, error) {
	if depth > 12 {
		return nil, nil, ErrImportInput
	}
	steps, issues := []string{}, []DraftIssue{}
	switch value := value.(type) {
	case nil:
	case string:
		for _, line := range strings.Split(plainRecipeText(value), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				steps = append(steps, line)
			}
		}
	case []any:
		for _, item := range value {
			children, uncertain, err := recipeInstructionSteps(item, depth+1)
			if err != nil {
				return nil, nil, err
			}
			steps = append(steps, children...)
			issues = append(issues, uncertain...)
			if len(steps) > 100 {
				return nil, nil, ErrImportInput
			}
		}
	case map[string]any:
		if schemaType(value["@type"], "HowToSection") {
			children, uncertain, err := recipeInstructionSteps(value["itemListElement"], depth+1)
			if err != nil {
				return nil, nil, err
			}
			if len(children) > 0 && recipeValueText(value["name"]) != "" {
				children[0] = recipeValueText(value["name"]) + ": " + children[0]
			}
			steps, issues = children, uncertain
		} else if text := recipeValueText(value["text"]); text != "" {
			steps = append(steps, text)
		} else if name := recipeValueText(value["name"]); name != "" {
			steps = append(steps, name)
			issues = append(issues, DraftIssue{Field: "instructions", Reason: "ambiguous", SourceText: name})
		} else {
			issues = append(issues, DraftIssue{Field: "instructions", Reason: "ambiguous"})
		}
	default:
		issues = append(issues, DraftIssue{Field: "instructions", Reason: "ambiguous"})
	}
	if len(steps) > 100 {
		return nil, nil, ErrImportInput
	}
	return steps, issues, nil
}

var recipeQuantityPrefix = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?(?:[ \t]+[0-9]+/[0-9]+)?|[0-9]+/[0-9]+|\.[0-9]+)[ \t]+(.+)$`)
var recipeCompactMetric = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)(kg|mg|g|ml|l)[ \t]+(.+)$`)
var recipeUnits = map[string]string{
	"g": "g", "gram": "g", "grams": "g", "kg": "kg", "kilogram": "kg", "kilograms": "kg", "mg": "mg",
	"ml": "ml", "milliliter": "ml", "milliliters": "ml", "millilitre": "ml", "millilitres": "ml", "l": "l", "liter": "l", "liters": "l", "litre": "l", "litres": "l",
	"tsp": "tsp", "teaspoon": "tsp", "teaspoons": "tsp", "tbsp": "tbsp", "tablespoon": "tbsp", "tablespoons": "tbsp",
	"cup": "cup", "cups": "cup", "oz": "oz", "ounce": "oz", "ounces": "oz", "lb": "lb", "pound": "lb", "pounds": "lb",
	"pinch": "pinch", "pinches": "pinch", "clove": "clove", "cloves": "clove",
}

func ingredientFromText(raw string) ExtractedIngredient {
	line := strings.TrimSpace(plainRecipeText(raw))
	item := ExtractedIngredient{Name: line, SourceText: line}
	numeric := line
	for _, fraction := range []struct{ glyph, text string }{{"½", "1/2"}, {"¼", "1/4"}, {"¾", "3/4"}, {"⅛", "1/8"}, {"⅜", "3/8"}, {"⅝", "5/8"}, {"⅞", "7/8"}, {"⅓", "1/3"}, {"⅔", "2/3"}} {
		numeric = strings.ReplaceAll(numeric, fraction.glyph, " "+fraction.text)
	}
	numeric = strings.TrimSpace(numeric)
	matched := recipeQuantityPrefix.FindStringSubmatch(numeric)
	var quantity, remaining, compactUnit string
	if len(matched) == 3 {
		quantity, remaining = matched[1], matched[2]
	} else if compact := recipeCompactMetric.FindStringSubmatch(numeric); len(compact) == 4 {
		quantity, compactUnit, remaining = compact[1], compact[2], compact[3]
	} else {
		return item
	}
	lower := strings.ToLower(remaining)
	if strings.HasPrefix(lower, "-") || strings.HasPrefix(lower, "–") || strings.HasPrefix(lower, "—") || strings.HasPrefix(lower, "to ") || strings.HasPrefix(lower, "or ") {
		return item
	}
	decimal, ok := exactRecipeQuantity(quantity)
	if !ok {
		return item
	}
	unit := compactUnit
	if unit == "" {
		first, rest, found := strings.Cut(remaining, " ")
		if candidate, known := recipeUnits[strings.TrimSuffix(strings.ToLower(first), ".")]; known && found && strings.TrimSpace(rest) != "" {
			unit, remaining = candidate, strings.TrimSpace(rest)
		}
	}
	item.Name, item.Quantity = remaining, &decimal
	if unit != "" {
		item.Unit = &unit
	}
	return item
}

func exactRecipeQuantity(value string) (domain.Decimal, bool) {
	if len(value) > 100 {
		return "", false
	}
	total := new(big.Rat)
	for _, part := range strings.Fields(value) {
		r, ok := new(big.Rat).SetString(part)
		if !ok || r.Sign() < 0 {
			return "", false
		}
		total.Add(total, r)
	}
	denominator := new(big.Int).Set(total.Denom())
	scale := 0
	for _, prime := range []int64{2, 5} {
		count := 0
		p := big.NewInt(prime)
		for new(big.Int).Mod(denominator, p).Sign() == 0 {
			denominator.Quo(denominator, p)
			count++
			if count > 100 {
				return "", false
			}
		}
		if count > scale {
			scale = count
		}
	}
	if denominator.Cmp(big.NewInt(1)) != 0 {
		return "", false
	}
	text := total.FloatString(scale)
	if strings.Contains(text, ".") {
		text = strings.TrimRight(strings.TrimRight(text, "0"), ".")
	}
	decimal := domain.Decimal(text)
	return decimal, len(text) <= 100 && decimal.Valid()
}
