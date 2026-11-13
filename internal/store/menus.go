package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/LowGok/my-chef/internal/domain"
	"gorm.io/gorm"
)

func Ptr[T any](v T) *T      { return &v }
func jsonText(v any) *string { b, _ := json.Marshal(v); return Ptr(string(b)) }
func lists(v *string) []string {
	if v == nil {
		return nil
	}
	var out []string
	if json.Unmarshal([]byte(*v), &out) == nil {
		return out
	}
	return []string{*v}
}
func minutes(v *string) *int {
	if v == nil {
		return nil
	}
	n, e := strconv.Atoi(*v)
	if e != nil {
		return nil
	}
	return &n
}

func (m Meal) Recipe() domain.Recipe {
	var mid *string
	if m.MenuID != nil {
		mid = Ptr(strconv.FormatInt(*m.MenuID, 10))
	}
	out := domain.Recipe{ID: strconv.FormatInt(m.ID, 10), MenuID: mid, Date: m.CreationDate, Title: m.RecipeTitle, Note: m.Notes, Yield: lists(m.Yield), Equipment: m.Equipment, Instructions: lists(m.Instructions), Tips: lists(m.TipsJSON), Allergens: lists(m.Allergens), AllergenNote: m.AllergenNote, PrepMinutes: minutes(m.PrepTime), CookMinutes: minutes(m.CookTime), TotalMinutes: minutes(m.TotalTime), ImageURL: m.ImageURL, ImageStatus: m.ImageStatus, Ingredients: []domain.Ingredient{}}
	if m.PersonalSource != nil {
		_ = json.Unmarshal([]byte(*m.PersonalSource), &out.Source)
	}
	if m.LegacyRaw != nil {
		_ = json.Unmarshal([]byte(*m.LegacyRaw), &out.LegacyRaw)
	}
	for _, i := range m.Ingredients {
		out.Ingredients = append(out.Ingredients, domain.Ingredient{ID: strconv.FormatInt(i.ID, 10), Name: i.Name, Quantity: i.Quantity, Unit: i.Unit})
	}
	return out
}

func (g Generation) Operation() domain.Operation {
	var mid *string
	if g.MenuID != nil {
		mid = Ptr(strconv.FormatInt(*g.MenuID, 10))
	}
	return domain.Operation{ID: strconv.FormatInt(g.ID, 10), Status: g.Status, Stage: g.Stage, MenuID: mid, ErrorCode: g.ErrorCode, ErrorMessage: g.ErrorMessage}
}

func (s *Store) SaveMenu(ctx context.Context, userID, operationID int64, input domain.MenuInput, recipes []domain.RecipeContent, readers ...PersonalEntitlementReader) (domain.Menu, error) {
	if err := input.Validate(); err != nil {
		return domain.Menu{}, err
	}
	for _, r := range recipes {
		if err := r.Validate(input, domain.EmptyPreferences()); err != nil {
			return domain.Menu{}, err
		}
	}
	menu := MenuRecord{UserID: userID, CreatedUnix: time.Now().UTC().Unix()}
	saved := []domain.Recipe{}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var op Generation
		if err := tx.Where("id = ? AND user_id = ? AND status = ?", operationID, userID, "running").First(&op).Error; err != nil {
			return err
		}
		var captured domain.GenerationRequest
		if err := json.Unmarshal([]byte(op.RequestJSON), &captured); err != nil {
			return err
		}
		if len(recipes)+len(captured.Favorites) != len(input.Dates) {
			return errors.New("recipe count does not match dates")
		}
		favorites := map[string]domain.PlannedFavorite{}
		for _, f := range captured.Favorites {
			favorites[f.Date] = f
		}
		for _, f := range captured.Favorites {
			if f.Recipe.Source != nil {
				var reader PersonalEntitlementReader
				if len(readers) > 0 {
					reader = readers[0]
				}
				if err := requirePlanEntitlement(ctx, tx, userID, reader); err != nil {
					return err
				}
				break
			}
		}
		if err := CheckMenuReplacement(tx, userID, input); err != nil {
			return err
		}
		if input.ReplaceExisting {
			if err := weekMeals(tx, userID, input).Delete(&Meal{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Create(&menu).Error; err != nil {
			return err
		}
		generatedIndex := 0
		for _, date := range input.Dates {
			if favorite, ok := favorites[date]; ok {
				row, err := copyRecipe(tx, userID, &menu.ID, date, favorite.Recipe)
				if err != nil {
					return err
				}
				saved = append(saved, row.Recipe())
				var result *gorm.DB
				if favorite.PersonalEntryID != nil {
					result = tx.Model(&personalRecipeQueue{}).Where("id=? AND user_id=? AND consumed_operation IS NULL", *favorite.PersonalEntryID, userID).Update("consumed_operation", operationID)
				} else {
					result = tx.Model(&favoriteQueue{}).Where("id=? AND user_id=? AND consumed_operation IS NULL", favorite.EntryID, userID).Update("consumed_operation", operationID)
				}
				if result.Error != nil {
					return result.Error
				}
				if result.RowsAffected != 1 && favorite.Recipe.Source == nil {
					return ErrFavoriteConflict
				}
				continue
			}
			r := recipes[generatedIndex]
			generatedIndex++
			row := Meal{CreatorID: userID, MenuID: &menu.ID, CreationDate: date, RecipeTitle: r.Title, Yield: jsonText(r.Yield), PrepTime: Ptr(strconv.Itoa(r.PrepMinutes)), CookTime: Ptr(strconv.Itoa(r.CookMinutes)), TotalTime: Ptr(strconv.Itoa(r.TotalMinutes)), Notes: &r.Note, Equipment: &r.Equipment, Instructions: jsonText(r.Instructions), Allergens: jsonText(r.Allergens), AllergenNote: &r.AllergenNote, IsMeatOrFish: &r.IsMeatOrFish, TipsJSON: jsonText(r.Tips), RecipeJSON: jsonText(r), ImageStatus: "pending"}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			for _, item := range r.Ingredients {
				var q *string
				if item.Quantity != nil {
					q = Ptr(string(*item.Quantity))
				}
				i := Ingredient{RecipeID: row.ID, Name: item.Name, Quantity: q, Unit: item.Unit, Date: &row.CreationDate}
				if err := tx.Create(&i).Error; err != nil {
					return err
				}
				row.Ingredients = append(row.Ingredients, i)
			}
			saved = append(saved, row.Recipe())
		}
		if err := tx.Model(&User{}).Where("user_id = ?", userID).Update("lastmeal", time.Now().UTC().Format(time.DateOnly)).Error; err != nil {
			return err
		}
		result := tx.Model(&Generation{}).Where("id = ? AND user_id = ? AND status = ?", operationID, userID, "running").Updates(map[string]any{"status": "succeeded", "stage": "finished", "menu_id": menu.ID})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("generation changed while saving")
		}
		return nil
	})
	if err != nil {
		return domain.Menu{}, err
	}
	return domain.Menu{ID: strconv.FormatInt(menu.ID, 10), Recipes: saved}, nil
}

// CheckMenuReplacement runs inside the caller's transaction before starting or saving a request.
func CheckMenuReplacement(tx *gorm.DB, userID int64, input domain.MenuInput) error {
	if input.ReplaceExisting {
		return nil
	}
	var count int64
	if err := weekMeals(tx.Model(&Meal{}), userID, input).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return domain.ErrMenuExists
	}
	return nil
}

func weekMeals(tx *gorm.DB, userID int64, input domain.MenuInput) *gorm.DB {
	clauses := []string{}
	args := []any{}
	for _, start := range input.WeekStarts() {
		day, _ := time.Parse(time.DateOnly, start)
		clauses = append(clauses, "(creationdate >= ? AND creationdate < ?)")
		args = append(args, start, day.AddDate(0, 0, 7).Format(time.DateOnly))
	}
	return tx.Where("creator_id = ?", userID).Where("("+strings.Join(clauses, " OR ")+")", args...)
}

func (s *Store) GetMenu(ctx context.Context, userID, id int64) (domain.Menu, error) {
	var menu MenuRecord
	if err := s.DB.WithContext(ctx).Where("id = ? AND user_id = ?", id, userID).First(&menu).Error; err != nil {
		return domain.Menu{}, err
	}
	var rows []Meal
	err := s.DB.WithContext(ctx).Where("menu_id = ? AND creator_id = ?", id, userID).Order("creationdate ASC, meal_id ASC").Preload("Ingredients", func(db *gorm.DB) *gorm.DB { return db.Order("pk_ingredients") }).Find(&rows).Error
	out := domain.Menu{ID: strconv.FormatInt(id, 10), Recipes: []domain.Recipe{}}
	for _, row := range rows {
		out.Recipes = append(out.Recipes, row.Recipe())
	}
	return out, err
}
func (s *Store) GetRecipe(ctx context.Context, userID, id int64) (domain.Recipe, error) {
	var row Meal
	err := s.DB.WithContext(ctx).Where("meal_id = ? AND creator_id = ?", id, userID).Preload("Ingredients", func(db *gorm.DB) *gorm.DB { return db.Order("pk_ingredients") }).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return favoriteRecipe(s.DB.WithContext(ctx), userID, id)
	}
	out := row.Recipe()
	if err == nil {
		var count int64
		if e := s.DB.WithContext(ctx).Model(&favoriteRecord{}).Where("user_id=? AND recipe_id=? AND saved=?", userID, id, true).Count(&count).Error; e != nil {
			return out, e
		}
		out.IsFavorite = count > 0
	}
	return out, err
}
func (s *Store) History(ctx context.Context, userID int64, from, to string, limit, offset int) (domain.RecipePage, error) {
	var rows []Meal
	err := s.DB.WithContext(ctx).Where("creator_id = ? AND creationdate >= ? AND creationdate < ?", userID, from, to).Order("creationdate ASC, meal_id ASC").Limit(limit+1).Offset(offset).Preload("Ingredients", func(db *gorm.DB) *gorm.DB { return db.Order("pk_ingredients") }).Find(&rows).Error
	out := domain.RecipePage{Recipes: []domain.Recipe{}}
	if len(rows) > limit {
		rows = rows[:limit]
		out.NextOffset = Ptr(offset + limit)
	}
	for _, row := range rows {
		out.Recipes = append(out.Recipes, row.Recipe())
	}
	return out, err
}

func (u User) Preferences() domain.Preferences {
	p := domain.EmptyPreferences()
	if u.PreferencesJSON != nil && json.Unmarshal([]byte(*u.PreferencesJSON), &p) == nil {
		return p
	}
	if u.Diet != nil {
		p.Diet = legacyPreferences(u.Diet)
	}
	if u.Allergens != nil {
		p.Allergens = legacyPreferences(u.Allergens)
	}
	if u.Dislikes != nil {
		p.Dislikes = legacyPreferences(u.Dislikes)
	}
	if u.CookingTechnique != nil {
		p.Technique = legacyPreferences(u.CookingTechnique)
	}
	if u.CookingEfforts != nil {
		p.Effort = *u.CookingEfforts
	}
	return p
}
func legacyPreferences(value *string) []string {
	out := []string{}
	if value == nil {
		return out
	}
	for _, item := range strings.Split(*value, ", ") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
func (u User) Profile() Profile {
	return Profile{ID: strconv.FormatInt(u.ID, 10), Email: u.Email, Username: u.Username, FirstName: u.FirstName, LastName: u.LastName, Birthdate: u.Birthdate, Sex: u.Sex, Preferences: u.Preferences()}
}

// AccountProfile includes the incarnation used to separate browser caches when
// a later account receives the same numeric SQLite ID.
func (s *Store) AccountProfile(ctx context.Context, user User) (Profile, error) {
	profile := user.Profile()
	err := s.SQL.QueryRowContext(ctx, `SELECT a.incarnation FROM account_incarnations a JOIN users u ON u.user_id=a.user_id WHERE u.user_id=? AND u.password=? AND u.email_key=?`, user.ID, user.Password, user.EmailKey).Scan(&profile.AccountInstance)
	if errors.Is(err, sql.ErrNoRows) {
		return Profile{}, ErrAccountAccess
	}
	if err != nil {
		return Profile{}, err
	}
	return profile, nil
}
