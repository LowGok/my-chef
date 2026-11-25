package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/LowGok/my-chef/internal/analytics"
	"github.com/LowGok/my-chef/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrShoppingInput = errors.New("invalid shopping list")
var ErrShoppingConflict = errors.New("shopping list changed")

type shoppingRecord struct {
	UserID   int64  `gorm:"primaryKey"`
	Week     string `gorm:"primaryKey"`
	Revision int64
	Body     string
}

func (shoppingRecord) TableName() string { return "shopping_lists" }

// The persisted default list keeps the user's meal selections and extras.
// Full-week check-offs with a different ingredient scope have their own
// fingerprints and cannot overwrite a check-off for a different amount.
type storedShopping struct {
	domain.ShoppingList
	FullWeekChecks map[string]shoppingCheck `json:"fullWeekChecks,omitempty"`
}

type shoppingCheck struct {
	Item     string  `json:"item"`
	State    string  `json:"state"`
	Name     string  `json:"name"`
	Quantity *string `json:"quantity"`
	Unit     string  `json:"unit"`
}

func shoppingWeek(week string) (string, error) {
	d, err := time.Parse(time.DateOnly, week)
	if err != nil || d.Weekday() != time.Monday || d.Format(time.DateOnly) != week {
		return "", ErrShoppingInput
	}
	return d.AddDate(0, 0, 7).Format(time.DateOnly), nil
}
func loadShopping(tx *gorm.DB, owner int64, week string) (storedShopping, error) {
	var row shoppingRecord
	err := tx.Where("user_id = ? AND week = ?", owner, week).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return storedShopping{ShoppingList: domain.ShoppingList{Week: week, Meals: []domain.ShoppingMeal{}, Items: []domain.ShoppingItem{}}}, nil
	}
	if err != nil {
		return storedShopping{}, err
	}
	var list storedShopping
	err = json.Unmarshal([]byte(row.Body), &list)
	return list, err
}
func sourceRecipes(tx *gorm.DB, owner int64, week, end string) ([]domain.Recipe, error) {
	var rows []Meal
	if err := tx.Where("creator_id = ? AND creationdate >= ? AND creationdate < ?", owner, week, end).Order("creationdate ASC, meal_id ASC").Preload("Ingredients", func(db *gorm.DB) *gorm.DB { return db.Order("pk_ingredients") }).Find(&rows).Error; err != nil {
		return nil, err
	}
	recipes := make([]domain.Recipe, 0, len(rows))
	for _, m := range rows {
		recipes = append(recipes, m.Recipe())
	}
	return recipes, nil
}
func reconcileShopping(old domain.ShoppingList, recipes []domain.Recipe) domain.ShoppingList {
	out := domain.ShoppingList{Week: old.Week, Revision: old.Revision, Meals: []domain.ShoppingMeal{}, Items: []domain.ShoppingItem{}}
	data, _ := json.Marshal(recipes)
	out.SourceVersion = domain.ShoppingHash(string(data))
	choices := map[string]bool{}
	states := map[string]domain.ShoppingItem{}
	for _, m := range old.Meals {
		choices[m.Position] = m.Selected
	}
	for _, i := range old.Items {
		if !i.Manual {
			states[i.ID] = i
		}
	}
	counts := map[string]int{}
	selected := []domain.Recipe{}
	for _, r := range recipes {
		if r.Source != nil && r.Source.Import != nil {
			out.PrivateImports = true
		}
		position := fmt.Sprintf("%s/%d", r.Date, counts[r.Date])
		counts[r.Date]++
		chosen, exists := choices[position]
		if !exists {
			chosen = true
		}
		out.Meals = append(out.Meals, domain.ShoppingMeal{Position: position, RecipeID: r.ID, Date: r.Date, Title: r.Title, Selected: chosen})
		if chosen {
			selected = append(selected, r)
		}
	}
	out.Items = domain.ShoppingIngredients(selected)
	for index, item := range out.Items {
		if previous, ok := states[item.ID]; ok {
			if reflect.DeepEqual(item.Quantity, previous.Quantity) {
				out.Items[index].State = previous.State
				out.Items[index].Updated = previous.Updated
			} else {
				out.Items[index].Updated = true
			}
		}
	}
	for _, item := range old.Items {
		if item.Manual {
			out.Items = append(out.Items, item)
		}
	}
	return out
}
func persistShopping(tx *gorm.DB, owner int64, list domain.ShoppingList, checks map[string]shoppingCheck) error {
	body, err := json.Marshal(storedShopping{ShoppingList: list, FullWeekChecks: checks})
	if err != nil {
		return err
	}
	return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "user_id"}, {Name: "week"}}, DoUpdates: clause.AssignmentColumns([]string{"revision", "body"})}).Create(&shoppingRecord{UserID: owner, Week: list.Week, Revision: list.Revision, Body: string(body)}).Error
}
func shoppingTier(ctx context.Context, tx *gorm.DB, owner int64, week string, policy *PlanningPolicy) (restricted bool, err error) {
	window, err := policy.Window(ctx, tx, owner)
	if err != nil {
		return false, err
	}
	if !window.Includes(week) {
		return false, gorm.ErrRecordNotFound
	}
	return window.Enabled() && !window.Premium, nil
}

func shoppingItemFingerprint(item domain.ShoppingItem) string {
	encoded, _ := json.Marshal(struct {
		Name     string
		Quantity *string
		Unit     string
		Manual   bool
	}{item.Name, item.Quantity, item.Unit, item.Manual})
	return domain.ShoppingHash(string(encoded))
}

func fullWeekShopping(saved storedShopping, recipes []domain.Recipe) domain.ShoppingList {
	unrestrictedSelection := saved.ShoppingList
	unrestrictedSelection.Meals = slices.Clone(saved.Meals)
	for i := range unrestrictedSelection.Meals {
		unrestrictedSelection.Meals[i].Selected = true
	}
	result := reconcileShopping(unrestrictedSelection, recipes)
	result.CustomizationLocked = true
	// A late customized snapshot must not be accepted under a different scope.
	result.SourceVersion = domain.ShoppingHash("full-week:" + result.SourceVersion)
	restoreFullWeekChecks(&result, saved)
	return result
}

func restoreFullWeekChecks(result *domain.ShoppingList, saved storedShopping) {
	stored := make(map[string]domain.ShoppingItem, len(saved.Items))
	for _, item := range saved.Items {
		stored[item.ID] = item
	}
	for i, item := range result.Items {
		if check, exists := saved.FullWeekChecks[item.ID]; !item.Manual && exists &&
			check.Item == shoppingItemFingerprint(item) &&
			check.Item != shoppingItemFingerprint(stored[item.ID]) &&
			validShoppingState(check.State) {
			result.Items[i].State = check.State
			result.Items[i].Updated = false
		}
	}
}

func applyFullWeekChecks(saved storedShopping, recipes []domain.Recipe, desired domain.ShoppingList) (domain.ShoppingList, domain.ShoppingList, map[string]shoppingCheck, error) {
	full := fullWeekShopping(saved, recipes)
	if desired.Revision != saved.Revision || desired.SourceVersion != full.SourceVersion {
		return domain.ShoppingList{}, domain.ShoppingList{}, nil, ErrShoppingConflict
	}
	canonical := reconcileShopping(saved.ShoppingList, recipes)
	restoreFullWeekChecks(&canonical, saved)
	previous := make(map[string]int, len(canonical.Items))
	for i, item := range canonical.Items {
		previous[item.ID] = i
	}
	approvals := make(map[string]domain.ShoppingItem, len(desired.Items))
	order := make([]string, 0, len(desired.Items))
	for _, item := range desired.Items {
		if _, exists := approvals[item.ID]; exists || !validShoppingState(item.State) {
			return domain.ShoppingList{}, domain.ShoppingList{}, nil, ErrShoppingInput
		}
		approvals[item.ID] = item
		order = append(order, item.ID)
	}
	blockedExtras := false
	coveredOrder := make([]string, 0, len(full.Items))
	checks := make(map[string]shoppingCheck)
	for index, item := range full.Items {
		fingerprint := shoppingItemFingerprint(item)
		if want, exists := approvals[item.ID]; exists {
			coveredOrder = append(coveredOrder, item.ID)
			delete(approvals, item.ID)
			if shoppingItemFingerprint(want) != fingerprint {
				blockedExtras = true
			} else if item.State != want.State {
				full.Items[index].State = want.State
				full.Items[index].Updated = false
				if i, stored := previous[item.ID]; stored && shoppingItemFingerprint(canonical.Items[i]) == fingerprint {
					canonical.Items[i].State = want.State
					canonical.Items[i].Updated = false
				}
			}
		} else if item.Manual {
			blockedExtras = true
		}
		canonicalIndex, shared := previous[item.ID]
		if !item.Manual && full.Items[index].State != "buy" &&
			(!shared || fingerprint != shoppingItemFingerprint(canonical.Items[canonicalIndex])) {
			checks[item.ID] = shoppingCheck{Item: fingerprint, State: full.Items[index].State,
				Name: item.Name, Quantity: item.Quantity, Unit: item.Unit}
		}
	}
	if len(approvals) > 0 || blockedExtras {
		full.BlockedChanges = append(full.BlockedChanges, "extras")
	}
	if !slices.EqualFunc(desired.Meals, full.Meals, func(a, b domain.ShoppingMeal) bool {
		return a.Position == b.Position && a.Selected == b.Selected
	}) {
		full.BlockedChanges = append(full.BlockedChanges, "meals")
	}
	keptOrder := slices.DeleteFunc(order, func(id string) bool { _, unknown := approvals[id]; return unknown })
	if !slices.Equal(keptOrder, coveredOrder) {
		full.BlockedChanges = append(full.BlockedChanges, "order")
	}
	canonical.Revision++
	full.Revision = canonical.Revision
	return full, canonical, checks, nil
}

func (s *Store) Shopping(ctx context.Context, owner int64, week string, actors ...PersonalActor) (domain.ShoppingList, error) {
	return s.ShoppingForPlan(ctx, owner, week, nil, actors...)
}

func (s *Store) ShoppingForPlan(ctx context.Context, owner int64, week string, policy *PlanningPolicy, actors ...PersonalActor) (domain.ShoppingList, error) {
	end, err := shoppingWeek(week)
	if err != nil {
		return domain.ShoppingList{}, err
	}
	var result domain.ShoppingList
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := nativeActorAuthority(tx, owner, actors...); err != nil {
			return err
		}
		restricted, err := shoppingTier(ctx, tx, owner, week, policy)
		if err != nil {
			return err
		}
		old, err := loadShopping(tx, owner, week)
		if err != nil {
			return err
		}
		recipes, err := sourceRecipes(tx, owner, week, end)
		if err != nil {
			return err
		}
		if restricted {
			result = fullWeekShopping(old, recipes)
			return nil
		}
		result = reconcileShopping(old.ShoppingList, recipes)
		restoreFullWeekChecks(&result, old)
		if !reflect.DeepEqual(old.ShoppingList, result) {
			result.Revision++
			return persistShopping(tx, owner, result, old.FullWeekChecks)
		}
		return nil
	})
	return result, err
}
func validShoppingState(state string) bool {
	return state == "buy" || state == "basket" || state == "home"
}
func (s *Store) SaveShopping(ctx context.Context, owner int64, week string, input domain.ShoppingList, actors ...PersonalActor) (domain.ShoppingList, error) {
	return s.SaveShoppingForPlan(ctx, owner, week, input, nil, actors...)
}

func (s *Store) SaveShoppingForPlan(ctx context.Context, owner int64, week string, input domain.ShoppingList, policy *PlanningPolicy, actors ...PersonalActor) (domain.ShoppingList, error) {
	end, err := shoppingWeek(week)
	if err != nil || input.Week != week || len(input.Items) > 1000 {
		return domain.ShoppingList{}, ErrShoppingInput
	}
	var result domain.ShoppingList
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := nativeActorAuthority(tx, owner, actors...); err != nil {
			return err
		}
		restricted, err := shoppingTier(ctx, tx, owner, week, policy)
		if err != nil {
			return err
		}
		old, err := loadShopping(tx, owner, week)
		if err != nil {
			return err
		}
		recipes, err := sourceRecipes(tx, owner, week, end)
		if err != nil {
			return err
		}
		if restricted {
			full, canonical, checks, err := applyFullWeekChecks(old, recipes, input)
			if err != nil {
				return err
			}
			result = full
			return persistShopping(tx, owner, canonical, checks)
		}
		current := reconcileShopping(old.ShoppingList, recipes)
		if input.Revision != old.Revision || input.SourceVersion != current.SourceVersion {
			return ErrShoppingConflict
		}
		if len(input.Meals) != len(current.Meals) {
			return ErrShoppingInput
		}
		choices := map[string]bool{}
		for _, m := range input.Meals {
			if _, exists := choices[m.Position]; exists {
				return ErrShoppingInput
			}
			choices[m.Position] = m.Selected
		}
		for i, m := range current.Meals {
			selected, ok := choices[m.Position]
			if !ok {
				return ErrShoppingInput
			}
			current.Meals[i].Selected = selected
		}
		// Rebuild ingredients after changing selection. Source fields supplied by clients are never persisted.
		result = reconcileShopping(current, recipes)
		restoreFullWeekChecks(&result, old)
		previous := map[string]domain.ShoppingItem{}
		for _, item := range old.Items {
			previous[item.ID] = item
		}
		desired := map[string]domain.ShoppingItem{}
		for _, item := range input.Items {
			if _, ok := desired[item.ID]; ok || !validShoppingState(item.State) {
				return ErrShoppingInput
			}
			desired[item.ID] = item
		}
		derived := make([]domain.ShoppingItem, 0, len(result.Items))
		for _, item := range result.Items {
			if !item.Manual {
				if want, ok := desired[item.ID]; ok && !want.Manual && reflect.DeepEqual(previous[item.ID].Quantity, item.Quantity) {
					item.State = want.State
					item.Updated = false
				}
				derived = append(derived, item)
			}
		}
		for _, item := range input.Items {
			if item.Manual {
				if len(item.ID) < 1 || len(item.ID) > 100 || strings.TrimSpace(item.Name) == "" || len(item.Name) > 200 || len(item.Unit) > 80 || item.Quantity != nil && len(*item.Quantity) > 100 {
					return ErrShoppingInput
				}
				if prior, ok := previous[item.ID]; ok && !prior.Manual {
					return ErrShoppingInput
				}
				item.Name = strings.TrimSpace(item.Name)
				item.Category = "Household extras"
				item.Contributions = []domain.ShoppingContribution{}
				item.Updated = false
				derived = append(derived, item)
			}
		}
		result.Items = derived
		before := reconcileShopping(old.ShoppingList, recipes)
		restoreFullWeekChecks(&before, old)
		changed := materialShoppingChange(before, result)
		result.Revision = old.Revision + 1
		if err := persistShopping(tx, owner, result, old.FullWeekChecks); err != nil {
			return err
		}
		if changed {
			return analytics.CaptureEvent(ctx, tx, owner, "shopping_list_edited", "")
		}
		return nil
	})
	return result, err
}

func materialShoppingChange(before, after domain.ShoppingList) bool {
	type manual struct {
		Name        string
		Quantity    *string
		Unit, State string
	}
	projection := func(list domain.ShoppingList) (map[string]bool, map[string]string, map[string]manual) {
		choices := map[string]bool{}
		states := map[string]string{}
		extras := map[string]manual{}
		for _, m := range list.Meals {
			choices[m.Position] = m.Selected
		}
		for _, i := range list.Items {
			if i.Manual {
				extras[i.ID] = manual{i.Name, i.Quantity, i.Unit, i.State}
			} else {
				states[i.ID] = i.State
			}
		}
		return choices, states, extras
	}
	a, b, c := projection(before)
	x, y, z := projection(after)
	return !reflect.DeepEqual(a, x) || !reflect.DeepEqual(b, y) || !reflect.DeepEqual(c, z)
}
