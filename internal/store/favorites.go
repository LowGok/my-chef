package store

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/LowGok/my-chef/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrFavoriteInput = errors.New("invalid favorite request")
var ErrFavoriteConflict = errors.New("favorite or meals changed; refresh and retry")

type favoriteRecord struct {
	Saved bool

	UserID      int64 `gorm:"primaryKey"`
	RecipeID    int64 `gorm:"primaryKey"`
	Body        string
	CreatedUnix int64
}

func (favoriteRecord) TableName() string { return "favorites" }

type favoriteQueue struct {
	ID                int64
	UserID            int64
	RecipeID          int64
	Week              string
	Body              string
	ConsumedOperation *int64
}

func (favoriteQueue) TableName() string { return "favorite_queue" }

type favoriteSwapRecord struct {
	UserID      int64  `gorm:"primaryKey"`
	RequestKey  string `gorm:"primaryKey"`
	RequestHash string
	Body        string
}

func (favoriteSwapRecord) TableName() string { return "favorite_swaps" }
func favoriteRecipe(tx *gorm.DB, user, id int64) (domain.Recipe, error) {
	var row favoriteRecord
	err := tx.Where("user_id=? AND recipe_id=? AND saved=?", user, id, true).First(&row).Error
	var r domain.Recipe
	if err == nil {
		err = json.Unmarshal([]byte(row.Body), &r)
		r.IsFavorite = true
	}
	return r, err
}
func (s *Store) SetFavorite(ctx context.Context, user, id int64, save bool, options ...PersonalPlanning) (domain.Recipe, error) {
	var out domain.Recipe
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := nativePlanningAuthority(tx, user, options); err != nil {
			return err
		}
		var meal Meal
		err := tx.Where("creator_id=? AND meal_id=?", user, id).Preload("Ingredients").First(&meal).Error
		if err == nil {
			out = meal.Recipe()
		} else if errors.Is(err, gorm.ErrRecordNotFound) {
			var prior favoriteRecord
			err = tx.Where("user_id=? AND recipe_id=?", user, id).First(&prior).Error
			if err == nil {
				err = json.Unmarshal([]byte(prior.Body), &out)
			}
		}
		if err != nil {
			return err
		}
		if save {
			if err := personalAssignmentAuthority(ctx, tx, user, out, options, false); err != nil {
				return err
			}
		}
		out.IsFavorite = save
		if !save {
			return tx.Model(&favoriteRecord{}).Where("user_id=? AND recipe_id=?", user, id).Update("saved", false).Error
		}
		b, err := json.Marshal(out)
		if err != nil {
			return err
		}
		var existing favoriteRecord
		if err := tx.Where("user_id=? AND recipe_id=? AND saved=?", user, id, true).First(&existing).Error; err == nil {
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "user_id"}, {Name: "recipe_id"}}, DoUpdates: clause.AssignmentColumns([]string{"saved", "body", "created_unix"})}).Create(&favoriteRecord{Saved: true, UserID: user, RecipeID: id, Body: string(b), CreatedUnix: time.Now().UTC().UnixNano()}).Error
	})
	return out, err
}
func (s *Store) Favorites(ctx context.Context, user int64, limit, offset int) (domain.RecipePage, error) {
	var rows []favoriteRecord
	out := domain.RecipePage{Recipes: []domain.Recipe{}}
	err := s.DB.WithContext(ctx).Where("user_id=? AND saved=?", user, true).Order("created_unix DESC,recipe_id DESC").Limit(limit + 1).Offset(offset).Find(&rows).Error
	if err != nil {
		return out, err
	}
	if len(rows) > limit {
		rows = rows[:limit]
		out.NextOffset = Ptr(offset + limit)
	}
	for _, row := range rows {
		var r domain.Recipe
		if err = json.Unmarshal([]byte(row.Body), &r); err != nil {
			return out, err
		}
		r.IsFavorite = true
		out.Recipes = append(out.Recipes, r)
	}
	return out, nil
}
func loadGeneratedFavoriteWeek(tx *gorm.DB, user int64, week string) (domain.FavoriteWeek, error) {
	out := domain.FavoriteWeek{Week: week, Recipes: []domain.Recipe{}, AddedIDs: []string{}}
	if _, err := shoppingWeek(week); err != nil {
		return out, ErrFavoriteInput
	}
	var rows []favoriteQueue
	if err := tx.Where("user_id=? AND week=?", user, week).Order("id").Find(&rows).Error; err != nil {
		return out, err
	}
	pending := []int64{}
	for _, row := range rows {
		out.AddedIDs = append(out.AddedIDs, strconv.FormatInt(row.RecipeID, 10))
		if row.ConsumedOperation == nil {
			var r domain.Recipe
			if err := json.Unmarshal([]byte(row.Body), &r); err != nil {
				return out, err
			}
			out.Recipes = append(out.Recipes, r)
			pending = append(pending, row.ID)
		}
	}
	mealIDs := []int64{}
	if err := tx.Model(&Meal{}).Where("creator_id=? AND creationdate>=? AND creationdate<?", user, week, addWeek(week)).Order("meal_id").Pluck("meal_id", &mealIDs).Error; err != nil {
		return out, err
	}
	// A planner review must notice intervening swaps as well as queue changes.
	b, _ := json.Marshal(struct {
		Pending []int64
		Meals   []int64
	}{pending, mealIDs})
	out.Revision = domain.ShoppingHash(string(b))
	return out, nil
}
func (s *Store) FavoriteWeek(ctx context.Context, user int64, week string, options ...PersonalPlanning) (domain.FavoriteWeek, error) {
	var out domain.FavoriteWeek
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		out, err = loadFavoriteWeek(tx, user, week, options...)
		return err
	})
	return out, err
}
func (s *Store) QueueFavorite(ctx context.Context, user, id int64, week string, options ...PersonalPlanning) (domain.FavoriteWeek, error) {
	var out domain.FavoriteWeek
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := nativePlanningAuthority(tx, user, options); err != nil {
			return err
		}
		if _, err := shoppingWeek(week); err != nil {
			return ErrFavoriteInput
		}
		var existing favoriteQueue
		err := tx.Where("user_id=? AND week=? AND recipe_id=?", user, week, id).First(&existing).Error
		if err == nil {
			var stored domain.Recipe
			if json.Unmarshal([]byte(existing.Body), &stored) != nil {
				return ErrPersonalStorage
			}
			if err := personalAssignmentAuthority(ctx, tx, user, stored, options, true); err != nil {
				return err
			}
			out, err = loadFavoriteWeek(tx, user, week, options...)
			return err
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		r, err := favoriteRecipe(tx, user, id)
		if err != nil {
			return err
		}
		if r.Source != nil {
			if _, err := queuePersonalSnapshot(ctx, tx, user, week, r, options); err != nil {
				return err
			}
			out, err = loadFavoriteWeek(tx, user, week, options...)
			return err
		}
		count, err := pendingRecipeCount(tx, user, week)
		if err != nil {
			return err
		}
		if count >= 7 {
			return ErrFavoriteConflict
		}
		body, err := json.Marshal(r)
		if err != nil {
			return err
		}
		if err = tx.Create(&favoriteQueue{UserID: user, RecipeID: id, Week: week, Body: string(body)}).Error; err != nil {
			return err
		}
		out, err = loadFavoriteWeek(tx, user, week, options...)
		return err
	})
	return out, err
}

// CaptureFavorites runs in the generation-creation transaction. Its assignments are persisted with the operation.
func CaptureFavorites(tx *gorm.DB, user int64, input domain.MenuInput, shuffle func([]string), options ...PersonalPlanning) ([]domain.PlannedFavorite, error) {
	if err := nativePlanningAuthority(tx, user, options); err != nil {
		return nil, err
	}
	out := []domain.PlannedFavorite{}
	for _, week := range input.WeekStarts() {
		summary, err := loadFavoriteWeek(tx, user, week, options...)
		if err != nil {
			return nil, err
		}
		reviewed, ok := input.FavoriteRevisions[week]
		if (len(summary.Recipes) > 0 && !ok) || (ok && reviewed != summary.Revision) {
			return nil, ErrFavoriteConflict
		}
		dates := []string{}
		for _, date := range input.Dates {
			if date >= week && date < addWeek(week) {
				dates = append(dates, date)
			}
		}
		if len(summary.Recipes) > len(dates) {
			return nil, ErrFavoriteInput
		}
		var rows []favoriteQueue
		if err = tx.Where("user_id=? AND week=? AND consumed_operation IS NULL", user, week).Order("id").Find(&rows).Error; err != nil {
			return nil, err
		}
		shuffle(dates)
		for i, row := range rows {
			var r domain.Recipe
			if err = json.Unmarshal([]byte(row.Body), &r); err != nil {
				return nil, err
			}
			if err := personalAssignmentAuthority(tx.Statement.Context, tx, user, r, options, true); err != nil {
				return nil, err
			}
			out = append(out, domain.PlannedFavorite{EntryID: row.ID, Date: dates[i], Recipe: r})
		}
		var personalRows []personalRecipeQueue
		if err := tx.Where("user_id=? AND week=? AND consumed_operation IS NULL", user, week).Order("id").Find(&personalRows).Error; err != nil {
			return nil, err
		}
		for i, row := range personalRows {
			var r domain.Recipe
			if json.Unmarshal([]byte(row.Body), &r) != nil {
				return nil, ErrPersonalStorage
			}
			if err := personalAssignmentAuthority(tx.Statement.Context, tx, user, r, options, true); err != nil {
				return nil, err
			}
			out = append(out, domain.PlannedFavorite{PersonalEntryID: Ptr(row.ID), Date: dates[len(rows)+i], Recipe: r})
		}
	}
	return out, nil
}
func addWeek(week string) string {
	d, _ := time.Parse(time.DateOnly, week)
	return d.AddDate(0, 0, 7).Format(time.DateOnly)
}
func copyRecipe(tx *gorm.DB, user int64, menu *int64, date string, r domain.Recipe) (Meal, error) {
	if r.Source != nil {
		if err := validatePersonalSnapshot(r); err != nil {
			return Meal{}, err
		}
		if r.Source.PhotoID != nil {
			var count int64
			if err := tx.Model(&PersonalPhotoAsset{}).Where("id=? AND user_id=?", *r.Source.PhotoID, user).Count(&count).Error; err != nil || count != 1 {
				return Meal{}, ErrPersonalStorage
			}
			r.ImageURL = Ptr("/api/v1/personal-photos/" + *r.Source.PhotoID)
		}
	}
	row := Meal{CreatorID: user, MenuID: menu, CreationDate: date, RecipeTitle: r.Title, Yield: jsonText(r.Yield), Notes: r.Note, Equipment: r.Equipment, Instructions: jsonText(r.Instructions), Allergens: jsonText(r.Allergens), AllergenNote: r.AllergenNote, TipsJSON: jsonText(r.Tips), ImageURL: r.ImageURL, ImageStatus: r.ImageStatus, LegacyRaw: jsonText(r.LegacyRaw)}
	if r.Source != nil {
		row.PersonalSource = jsonText(r.Source)
	}
	if row.ImageStatus == "pending" {
		row.ImageStatus = "unavailable"
	}
	if r.PrepMinutes != nil {
		row.PrepTime = Ptr(strconv.Itoa(*r.PrepMinutes))
	}
	if r.CookMinutes != nil {
		row.CookTime = Ptr(strconv.Itoa(*r.CookMinutes))
	}
	if r.TotalMinutes != nil {
		row.TotalTime = Ptr(strconv.Itoa(*r.TotalMinutes))
	}
	if err := tx.Create(&row).Error; err != nil {
		return row, err
	}
	for _, ingredient := range r.Ingredients {
		i := Ingredient{RecipeID: row.ID, Name: ingredient.Name, Quantity: ingredient.Quantity, Unit: ingredient.Unit, Date: &date}
		if err := tx.Create(&i).Error; err != nil {
			return row, err
		}
		row.Ingredients = append(row.Ingredients, i)
	}
	return row, nil
}
func (s *Store) SwapFavorite(ctx context.Context, user, id int64, input domain.FavoriteSwap, options ...PersonalPlanning) (domain.Recipe, error) {
	signature, _ := json.Marshal(struct {
		RecipeID int64
		Input    domain.FavoriteSwap
	}{id, input})
	hash := domain.ShoppingHash(string(signature))
	return s.swapSavedRecipe(ctx, user, input, hash, func(tx *gorm.DB) (domain.Recipe, error) {
		r, err := favoriteRecipe(tx, user, id)
		if err != nil {
			return r, err
		}
		if err := personalAssignmentAuthority(ctx, tx, user, r, options, true); err != nil {
			return domain.Recipe{}, err
		}
		return r, nil
	}, options)
}

func (s *Store) swapSavedRecipe(ctx context.Context, user int64, input domain.FavoriteSwap, hash string, resolve func(*gorm.DB) (domain.Recipe, error), options []PersonalPlanning) (domain.Recipe, error) {
	var out domain.Recipe
	if len(input.RequestKey) < 8 || len(input.RequestKey) > 100 {
		return out, ErrFavoriteInput
	}
	target, e := strconv.ParseInt(input.TargetID, 10, 64)
	if e != nil || target < 1 {
		return out, ErrFavoriteInput
	}
	if _, e = shoppingWeek(input.Week); e != nil {
		return out, ErrFavoriteInput
	}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := nativePlanningAuthority(tx, user, options); err != nil {
			return err
		}
		var prior favoriteSwapRecord
		err := tx.Where("user_id=? AND request_key=?", user, input.RequestKey).First(&prior).Error
		if err == nil {
			if prior.RequestHash != hash {
				return ErrFavoriteConflict
			}
			if err := json.Unmarshal([]byte(prior.Body), &out); err != nil {
				return err
			}
			if out.Source != nil {
				p := planningOption(options)
				if p.Actor.UserID != user {
					return ErrPersonalSession
				}
				if _, err := personalAuthority(tx, p.Actor, p.Now); err != nil {
					return err
				}
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		r, err := resolve(tx)
		if err != nil {
			return err
		}
		if r.Source != nil {
			if !validPersonalID(input.RequestKey) {
				return ErrPersonalInput
			}
			p := planningOption(options)
			incarnation, err := personalAuthority(tx, p.Actor, p.Now)
			if err != nil {
				return err
			}
			if err := personalAssignmentKeyAvailable(tx, p.Actor, incarnation, input.RequestKey); err != nil {
				return err
			}
		}
		var old Meal
		if err = tx.Where("creator_id=? AND meal_id=? AND creationdate>=? AND creationdate<?", user, target, input.Week, addWeek(input.Week)).First(&old).Error; err != nil {
			return ErrFavoriteConflict
		}
		var operations []Generation
		if err = tx.Where("user_id=? AND status IN ?", user, []string{"queued", "running"}).Find(&operations).Error; err != nil {
			return err
		}
		for _, op := range operations {
			var request domain.GenerationRequest
			if err = json.Unmarshal([]byte(op.RequestJSON), &request); err != nil {
				return err
			}
			for _, week := range request.WeekStarts() {
				if week == input.Week {
					return ErrFavoriteConflict
				}
			}
		}
		if err = tx.Delete(&old).Error; err != nil {
			return err
		}
		copy, err := copyRecipe(tx, user, old.MenuID, old.CreationDate, r)
		if err != nil {
			return err
		}
		out = copy.Recipe()
		body, err := json.Marshal(out)
		if err != nil {
			return err
		}
		return tx.Create(&favoriteSwapRecord{UserID: user, RequestKey: input.RequestKey, RequestHash: hash, Body: string(body)}).Error
	})
	return out, err
}
