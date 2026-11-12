package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

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

func shoppingWeek(week string) (string, error) {
	d, err := time.Parse(time.DateOnly, week)
	if err != nil || d.Weekday() != time.Monday || d.Format(time.DateOnly) != week {
		return "", ErrShoppingInput
	}
	return d.AddDate(0, 0, 7).Format(time.DateOnly), nil
}
func loadShopping(tx *gorm.DB, owner int64, week string) (domain.ShoppingList, error) {
	var row shoppingRecord
	err := tx.Where("user_id = ? AND week = ?", owner, week).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.ShoppingList{Week: week, Meals: []domain.ShoppingMeal{}, Items: []domain.ShoppingItem{}}, nil
	}
	if err != nil {
		return domain.ShoppingList{}, err
	}
	var list domain.ShoppingList
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
func persistShopping(tx *gorm.DB, owner int64, list domain.ShoppingList) error {
	body, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "user_id"}, {Name: "week"}}, DoUpdates: clause.AssignmentColumns([]string{"revision", "body"})}).Create(&shoppingRecord{UserID: owner, Week: list.Week, Revision: list.Revision, Body: string(body)}).Error
}
func (s *Store) Shopping(ctx context.Context, owner int64, week string, actors ...PersonalActor) (domain.ShoppingList, error) {
	end, err := shoppingWeek(week)
	if err != nil {
		return domain.ShoppingList{}, err
	}
	var result domain.ShoppingList
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := nativeActorAuthority(tx, owner, actors...); err != nil {
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
		result = reconcileShopping(old, recipes)
		if !reflect.DeepEqual(old, result) {
			result.Revision++
			return persistShopping(tx, owner, result)
		}
		return nil
	})
	return result, err
}
func validShoppingState(state string) bool {
	return state == "buy" || state == "basket" || state == "home"
}
func (s *Store) SaveShopping(ctx context.Context, owner int64, week string, input domain.ShoppingList, actors ...PersonalActor) (domain.ShoppingList, error) {
	end, err := shoppingWeek(week)
	if err != nil || input.Week != week || len(input.Items) > 1000 {
		return domain.ShoppingList{}, ErrShoppingInput
	}
	var result domain.ShoppingList
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := nativeActorAuthority(tx, owner, actors...); err != nil {
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
		current := reconcileShopping(old, recipes)
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
		result.Revision = old.Revision + 1
		return persistShopping(tx, owner, result)
	})
	return result, err
}
