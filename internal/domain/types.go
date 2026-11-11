package domain

import (
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jinzhu/inflection"
)

type Preferences struct {
	Diet      []string `json:"diet"`
	Allergens []string `json:"allergens"`
	Dislikes  []string `json:"dislikes"`
	Technique []string `json:"technique"`
	Effort    string   `json:"effort"`
}

func EmptyPreferences() Preferences {
	return Preferences{Diet: []string{}, Allergens: []string{}, Dislikes: []string{}, Technique: []string{}, Effort: "Quick weekday meals"}
}

type MenuInput struct {
	Dates             []string          `json:"dates"`
	FavoriteRevisions map[string]string `json:"favoriteRevisions,omitempty"`
	Adults            int               `json:"adults"`
	Children          int               `json:"children"`
	MaxMinutes        int               `json:"maxMinutes"`
	Budget            string            `json:"budget"`
	Region            string            `json:"region"`
	Creativity        int               `json:"creativity"`
	ReplaceExisting   bool              `json:"replaceExisting,omitempty"`
}

var ErrMenuExists = errors.New("a menu already exists for the selected week")

// WeekStarts returns the distinct Monday dates touched by a validated request.
func (m MenuInput) WeekStarts() []string {
	seen := map[string]bool{}
	weeks := []string{}
	for _, date := range m.Dates {
		day, err := time.Parse(time.DateOnly, date)
		if err != nil {
			continue
		}
		monday := day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7)).Format(time.DateOnly)
		if !seen[monday] {
			seen[monday] = true
			weeks = append(weeks, monday)
		}
	}
	sort.Strings(weeks)
	return weeks
}

func (m MenuInput) Validate() error {
	if len(m.Dates) < 1 || len(m.Dates) > 7 || m.Adults < 1 || m.Adults > 20 || m.Children < 0 || m.Children > 20 || m.MaxMinutes < 10 || m.MaxMinutes > 240 || m.Creativity < 0 || m.Creativity > 10 || len(m.Region) < 1 || len(m.Region) > 120 {
		return errors.New("invalid menu input")
	}
	if m.Budget != "low" && m.Budget != "medium" && m.Budget != "high" {
		return errors.New("invalid budget")
	}
	seen := map[string]bool{}
	for _, d := range m.Dates {
		p, e := time.Parse(time.DateOnly, d)
		if e != nil || p.Format(time.DateOnly) != d || seen[d] {
			return errors.New("invalid meal dates")
		}
		seen[d] = true
	}
	return nil
}

var decimalPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]+)?$`)

type Decimal string

func (d Decimal) Valid() bool {
	return len(d) > 0 && len(d) <= 100 && decimalPattern.MatchString(string(d))
}

type IngredientContent struct {
	Name     string   `json:"name"`
	Quantity *Decimal `json:"quantity"`
	Unit     *string  `json:"unit"`
}
type RecipeContent struct {
	Title        string              `json:"title"`
	Note         string              `json:"note"`
	Yield        []string            `json:"yield"`
	Equipment    string              `json:"equipment"`
	Instructions []string            `json:"instructions"`
	Tips         []string            `json:"tips"`
	Allergens    []string            `json:"allergens"`
	AllergenNote string              `json:"allergenNote"`
	PrepMinutes  int                 `json:"prepMinutes"`
	CookMinutes  int                 `json:"cookMinutes"`
	TotalMinutes int                 `json:"totalMinutes"`
	IsMeatOrFish bool                `json:"isMeatOrFish"`
	Ingredients  []IngredientContent `json:"ingredients"`
}
type GenerationRequest struct {
	Favorites []PlannedFavorite `json:"favorites,omitempty"`
	MenuInput
	Preferences Preferences `json:"preferences"`
	CurrentDate string      `json:"currentDate"`
	OperationID string      `json:"operationId"`
}

func (r RecipeContent) Validate(input MenuInput, prefs Preferences) error {
	if strings.TrimSpace(r.Title) == "" || len(r.Title) > 200 || len(r.Note) == 0 || len(r.Note) > 4000 || len(r.Equipment) == 0 || len(r.Equipment) > 2000 || len(r.AllergenNote) == 0 || len(r.AllergenNote) > 4000 || len(r.Yield) == 0 || len(r.Instructions) == 0 || len(r.Ingredients) == 0 || len(r.Ingredients) > 100 || len(r.Instructions) > 100 || r.Tips == nil || r.Allergens == nil {
		return errors.New("incomplete recipe")
	}
	if r.PrepMinutes < 0 || r.CookMinutes < 0 || r.TotalMinutes < 1 || r.TotalMinutes < r.PrepMinutes+r.CookMinutes || r.TotalMinutes > input.MaxMinutes {
		return errors.New("invalid recipe timing")
	}
	for _, rule := range []struct {
		values       []string
		count, bytes int
	}{{r.Yield, 30, 200}, {r.Instructions, 100, 4000}, {r.Tips, 30, 200}, {r.Allergens, 30, 200}} {
		if len(rule.values) > rule.count {
			return errors.New("too many recipe fields")
		}
		for _, v := range rule.values {
			if strings.TrimSpace(v) == "" || len(v) > rule.bytes {
				return errors.New("invalid recipe text")
			}
		}
	}
	ingredients := make([]string, 0, len(r.Ingredients))
	for _, i := range r.Ingredients {
		if strings.TrimSpace(i.Name) == "" || len(i.Name) > 200 || i.Quantity != nil && !i.Quantity.Valid() || i.Unit != nil && len(*i.Unit) > 80 {
			return errors.New("invalid ingredient")
		}
		ingredients = append(ingredients, strings.ToLower(i.Name))
	}
	exclusions := append(append([]string{}, prefs.Allergens...), prefs.Dislikes...)
	for _, value := range prefs.Diet {
		for _, diet := range strings.Split(value, ",") {
			diet = strings.ToLower(strings.TrimSpace(diet))
			if (diet == "vegan" || diet == "vegetarian") && r.IsMeatOrFish {
				return errors.New("dietary conflict")
			}
			if diet == "vegan" || diet == "vegetarian" || diet == "pescatarian" {
				exclusions = append(exclusions, "beef", "pork", "chicken", "turkey", "lamb", "bacon", "ham", "duck", "veal", "venison")
			}
			if diet == "vegan" || diet == "vegetarian" {
				exclusions = append(exclusions, "fish", "crustaceans", "molluscs")
			}
			if diet == "vegan" || diet == "dairy-free" {
				exclusions = append(exclusions, "milk")
			}
			if diet == "vegan" {
				exclusions = append(exclusions, "eggs", "honey")
			}
			if diet == "gluten-free" {
				exclusions = append(exclusions, "cereals containing gluten")
			}
		}
	}
	text := strings.ToLower(strings.Join(append(append([]string{}, r.Allergens...), ingredients...), " "))
	aliases := map[string][]string{"milk": {"milk", "cream", "cheese", "butter", "yogurt"}, "peanuts": {"peanut"}, "nuts": {"almond", "walnut", "cashew", "hazelnut", "pistachio", "pecan"}, "eggs": {"egg"}, "soya": {"soy", "soya", "tofu"}, "soybeans": {"soy", "soya", "tofu"}, "fish": {"fish", "salmon", "tuna", "cod", "anchovy", "sardine"}, "sesame": {"sesame", "tahini"}, "sesame seeds": {"sesame", "tahini"}, "crustaceans": {"shrimp", "prawn", "crab", "lobster", "crayfish"}, "molluscs": {"mussel", "oyster", "clam", "scallop", "squid", "octopus"}, "celery": {"celery", "celeriac"}, "sulphites": {"sulphite", "sulfite"}, "cereals containing gluten": {"wheat", "barley", "rye", "gluten", "semolina", "couscous", "seitan"}}
	for _, exclude := range exclusions {
		key := strings.ToLower(strings.TrimSpace(exclude))
		if key == "" {
			continue
		}
		terms := append([]string{key, inflection.Singular(key)}, aliases[key]...)
		for _, term := range terms {
			candidateText := text
			if term == "gluten" {
				clean := make([]string, len(ingredients))
				for i, v := range ingredients {
					clean[i] = strings.ReplaceAll(strings.ReplaceAll(v, "gluten-free", ""), "gluten free", "")
				}
				candidateText = strings.ToLower(strings.Join(append(append([]string{}, r.Allergens...), clean...), " "))
			}
			singular := inflection.Singular(term)
			re := regexp.MustCompile(`(?i)(^|[^\p{L}\p{N}])(?:` + regexp.QuoteMeta(singular) + `|` + regexp.QuoteMeta(inflection.Plural(singular)) + `)($|[^\p{L}\p{N}])`)
			if re.MatchString(candidateText) {
				return errors.New("declared exclusion conflict")
			}
		}
	}
	return nil
}

type Operation struct {
	ID           string  `json:"id"`
	Status       string  `json:"status"`
	Stage        string  `json:"stage"`
	MenuID       *string `json:"menuId"`
	ErrorCode    *string `json:"errorCode"`
	ErrorMessage *string `json:"errorMessage"`
}

type Ingredient struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Quantity *string `json:"quantity"`
	Unit     *string `json:"unit"`
}
type Recipe struct {
	Source       *RecipeSource      `json:"source,omitempty"`
	IsFavorite   bool               `json:"isFavorite"`
	ID           string             `json:"id"`
	MenuID       *string            `json:"menuId"`
	Date         string             `json:"date"`
	Title        string             `json:"title"`
	Note         *string            `json:"note"`
	Yield        []string           `json:"yield"`
	Equipment    *string            `json:"equipment"`
	Instructions []string           `json:"instructions"`
	Tips         []string           `json:"tips"`
	Allergens    []string           `json:"allergens"`
	AllergenNote *string            `json:"allergenNote"`
	PrepMinutes  *int               `json:"prepMinutes"`
	CookMinutes  *int               `json:"cookMinutes"`
	TotalMinutes *int               `json:"totalMinutes"`
	Ingredients  []Ingredient       `json:"ingredients"`
	ImageURL     *string            `json:"imageUrl"`
	ImageStatus  string             `json:"imageStatus"`
	LegacyRaw    map[string]*string `json:"legacyRaw"`
}

// RecipeImportSource is the minimal private attribution saved after import review.
type RecipeImportSource struct {
	Kind       string `json:"kind"`
	URL        string `json:"url,omitempty"`
	Title      string `json:"title,omitempty"`
	ImportedAt string `json:"importedAt"`
}

// RecipeSource persists personal provenance even after the library is deleted.
// These are opaque application identities, never provider object handles.
type RecipeSource struct {
	Import    *RecipeImportSource `json:"import,omitempty"`
	Kind      string              `json:"kind"`
	RecipeID  string              `json:"recipeId"`
	VersionID string              `json:"versionId"`
	PhotoID   *string             `json:"photoId"`
}
type Menu struct {
	ID      string   `json:"id"`
	Recipes []Recipe `json:"recipes"`
}
type RecipePage struct {
	Recipes    []Recipe `json:"recipes"`
	NextOffset *int     `json:"nextOffset"`
}

// PlannedFavorite is a server-captured assignment, never client input.
type PlannedFavorite struct {
	PersonalEntryID *string `json:"personalEntryId,omitempty"`
	EntryID         int64   `json:"entryId"`
	Date            string  `json:"date"`
	Recipe          Recipe  `json:"recipe"`
}
type FavoriteWeek struct {
	Entries  []QueuedRecipe `json:"entries,omitempty"`
	Week     string         `json:"week"`
	Revision string         `json:"revision"`
	Recipes  []Recipe       `json:"recipes"`
	AddedIDs []string       `json:"addedIds"`
}
type QueuedRecipe struct {
	ID      string `json:"id"`
	Recipe  Recipe `json:"recipe"`
	Blocked bool   `json:"blocked"`
}
type FavoriteSwap struct {
	Week       string `json:"week"`
	Today      string `json:"today"`
	TargetID   string `json:"targetId"`
	RequestKey string `json:"requestKey"`
}
