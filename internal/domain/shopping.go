package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strings"
)

type ShoppingContribution struct {
	RecipeID string  `json:"recipeId"`
	Title    string  `json:"title"`
	Quantity *string `json:"quantity"`
	Unit     string  `json:"unit"`
}

type ShoppingItem struct {
	ID            string                 `json:"id"`
	Name          string                 `json:"name"`
	Quantity      *string                `json:"quantity"`
	Unit          string                 `json:"unit"`
	Category      string                 `json:"category"`
	State         string                 `json:"state"`
	Updated       bool                   `json:"updated"`
	Manual        bool                   `json:"manual"`
	Contributions []ShoppingContribution `json:"contributions"`
}

type ShoppingMeal struct {
	Position string `json:"position"`
	RecipeID string `json:"recipeId"`
	Date     string `json:"date"`
	Title    string `json:"title"`
	Selected bool   `json:"selected"`
}

type ShoppingList struct {
	PrivateImports bool           `json:"privateImports,omitempty"`
	Week           string         `json:"week"`
	Revision       int64          `json:"revision"`
	SourceVersion  string         `json:"sourceVersion"`
	Meals          []ShoppingMeal `json:"meals"`
	Items          []ShoppingItem `json:"items"`
}

func shoppingText(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

// Only listed units are interchangeable. Unknown units retain one row per source.
func shoppingUnit(unit *string) (string, int64, bool) {
	if unit == nil {
		return "", 1, false
	}
	u := shoppingText(*unit)
	switch u {
	case "g", "gram", "grams":
		return "g", 1, true
	case "kg", "kilogram", "kilograms":
		return "g", 1000, true
	case "ml", "milliliter", "milliliters", "millilitre", "millilitres":
		return "ml", 1, true
	case "l", "liter", "liters", "litre", "litres":
		return "ml", 1000, true
	case "piece", "pieces":
		return "piece", 1, true
	default:
		return u, 1, false
	}
}

func ShoppingHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func shoppingCategory(name string) string {
	for _, rule := range []struct {
		category string
		terms    []string
	}{
		{"Fruit & vegetables", []string{"carrot", "onion", "garlic", "spinach", "cabbage", "ginger", "sweet potato", "mushroom", "lemon"}},
		{"Frozen", []string{"frozen"}},
		{"Tins & jars", []string{"canned", "tinned"}},
		{"Grains & bread", []string{"rice", "lentil", "polenta", "buckwheat", "tortilla", "bread", "pasta", "noodle"}},
		{"Oils & seasonings", []string{"oil", "salt", "cumin", "thyme", "paprika", "pepper", "spice"}},
	} {
		for _, term := range rule.terms {
			if strings.Contains(name, term) {
				return rule.category
			}
		}
	}
	return "Other ingredients"
}

var shoppingAmount = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+|/[1-9][0-9]*)?$`)

// ShoppingIngredients never changes recipes and uses rationals rather than floats.
// An ambiguous amount or unit remains a separate, traceable source row.
func ShoppingIngredients(recipes []Recipe) []ShoppingItem {
	items := map[string]*ShoppingItem{}
	sums := map[string]*big.Rat{}
	for _, recipe := range recipes {
		for index, ingredient := range recipe.Ingredients {
			name := shoppingText(ingredient.Name)
			unit, scale, known := shoppingUnit(ingredient.Unit)
			key := name + "\x00" + unit
			var amount *big.Rat
			if ingredient.Quantity != nil && len(*ingredient.Quantity) <= 100 && shoppingAmount.MatchString(*ingredient.Quantity) {
				if q, ok := new(big.Rat).SetString(*ingredient.Quantity); ok && q.Sign() >= 0 {
					amount = q.Mul(q, big.NewRat(scale, 1))
				}
			}
			if !known || amount == nil {
				key += fmt.Sprintf("\x00%s/%d", recipe.ID, index)
			}
			id := ShoppingHash(key)
			item := items[id]
			if item == nil {
				item = &ShoppingItem{ID: id, Name: name, Unit: unit, Category: shoppingCategory(name), State: "buy", Contributions: []ShoppingContribution{}}
				items[id] = item
			}
			sourceUnit := ""
			if ingredient.Unit != nil {
				sourceUnit = *ingredient.Unit
			}
			item.Contributions = append(item.Contributions, ShoppingContribution{RecipeID: recipe.ID, Title: recipe.Title, Quantity: ingredient.Quantity, Unit: sourceUnit})
			if amount != nil {
				if sums[id] == nil {
					sums[id] = new(big.Rat)
				}
				sums[id].Add(sums[id], amount)
			} else {
				item.Quantity = ingredient.Quantity
			}
		}
	}
	out := make([]ShoppingItem, 0, len(items))
	for id, item := range items {
		if q := sums[id]; q != nil {
			// Decimal inputs and supported fractional inputs retain exact terminating decimals.
			denom := new(big.Int).Set(q.Denom())
			two, five := big.NewInt(2), big.NewInt(5)
			digits2, digits5 := 0, 0
			for new(big.Int).Mod(denom, two).Sign() == 0 {
				denom.Div(denom, two)
				digits2++
			}
			for new(big.Int).Mod(denom, five).Sign() == 0 {
				denom.Div(denom, five)
				digits5++
			}
			value := q.RatString()
			if denom.Cmp(big.NewInt(1)) == 0 {
				precision := max(digits2, digits5)
				value = q.FloatString(precision)
				if strings.Contains(value, ".") {
					value = strings.TrimRight(strings.TrimRight(value, "0"), ".")
				}
			}
			item.Quantity = &value
		}
		out = append(out, *item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out
}
