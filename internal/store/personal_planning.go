package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/LowGok/my-chef/internal/domain"
	"gorm.io/gorm"
)

type PersonalPlanning struct {
	Actor        PersonalActor
	Entitlements PersonalEntitlementReader
	Now          time.Time
}

func planningOption(options []PersonalPlanning) PersonalPlanning {
	var p PersonalPlanning
	if len(options) > 0 {
		p = options[0]
	}
	if p.Now.IsZero() {
		p.Now = time.Now()
	}
	return p
}

type personalRecipeQueue struct {
	ID, Incarnation, RecipeID, VersionID, Week, Body string
	UserID                                           int64
	ConsumedOperation                                *int64
}

func (personalRecipeQueue) TableName() string { return "personal_recipe_queue" }

type personalQueueReceipt struct {
	UserID                                                                                  int64
	Incarnation, RequestKey, RequestHash, Action, QueueID, RecipeID, VersionID, Week, State string
}

func (personalQueueReceipt) TableName() string { return "personal_queue_receipts" }

func personalRecipeSnapshot(tx *gorm.DB, actor PersonalActor, recipe PersonalRecipe) (domain.Recipe, error) {
	content, err := recipe.Content.Normalize()
	if err != nil {
		return domain.Recipe{}, ErrPersonalStorage
	}
	var owned int64
	if err := tx.Model(&personalRoot{}).Where("id=? AND user_id=?", recipe.ID, actor.UserID).Count(&owned).Error; err != nil || owned != 1 {
		return domain.Recipe{}, ErrPersonalNotFound
	}
	c := content.RecipeContent()
	r := domain.Recipe{ID: "personal:" + recipe.ID, Title: c.Title, Note: Ptr(c.Note), Yield: c.Yield, Equipment: Ptr(c.Equipment), Instructions: c.Instructions, Tips: c.Tips, Allergens: c.Allergens, AllergenNote: Ptr(c.AllergenNote), PrepMinutes: Ptr(c.PrepMinutes), CookMinutes: Ptr(c.CookMinutes), TotalMinutes: Ptr(c.TotalMinutes), Ingredients: []domain.Ingredient{}, ImageURL: recipe.ImageURL, ImageStatus: "unavailable", Source: &domain.RecipeSource{Kind: "personal", RecipeID: recipe.ID, VersionID: recipe.VersionID, PhotoID: recipe.PhotoID, Import: recipe.ImportSource}}
	if recipe.ImageURL != nil {
		r.ImageStatus = "ready"
	}
	for i, ingredient := range c.Ingredients {
		var quantity *string
		if ingredient.Quantity != nil {
			quantity = Ptr(string(*ingredient.Quantity))
		}
		r.Ingredients = append(r.Ingredients, domain.Ingredient{ID: "personal:" + strconv.Itoa(i+1), Name: ingredient.Name, Quantity: quantity, Unit: ingredient.Unit})
	}
	return r, nil
}

func validatePersonalSnapshot(recipe domain.Recipe) error {
	if recipe.Source == nil || recipe.Source.Kind != "personal" || !validPersonalID(recipe.Source.RecipeID) || !validPersonalID(recipe.Source.VersionID) || recipe.Source.PhotoID != nil && !validPersonalID(*recipe.Source.PhotoID) {
		return ErrPersonalStorage
	}
	if recipe.Source.Import != nil {
		if _, err := checkedImportSource(recipe.Source.Import); err != nil {
			return err
		}
	}
	draft := domain.PersonalRecipeDraft(recipe)
	if draft.NeedsServings {
		return ErrPersonalStorage
	}
	if _, err := draft.Content.Normalize(); err != nil {
		return ErrPersonalStorage
	}
	return nil
}

// The durable worker has a server-captured plan, not a stored session token.
// Its operation belongs to the account and is removed by account deletion.
func requirePlanEntitlement(ctx context.Context, tx *gorm.DB, user int64, reader PersonalEntitlementReader) error {
	var incarnation string
	err := tx.Raw(`SELECT a.incarnation FROM account_incarnations a JOIN users u ON u.user_id=a.user_id
WHERE a.user_id=? AND u.email_verified_at>0`, user).Row().Scan(&incarnation)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrPersonalSession
	}
	if err != nil {
		return ErrPersonalStorage
	}
	return personalPermission(ctx, tx, PersonalActor{UserID: user}, incarnation, reader, time.Now())
}

func personalAssignmentAuthority(ctx context.Context, tx *gorm.DB, user int64, recipe domain.Recipe, options []PersonalPlanning, premium bool) error {
	if recipe.Source == nil {
		return nil
	}
	if err := validatePersonalSnapshot(recipe); err != nil {
		return err
	}
	p := planningOption(options)
	if p.Actor.UserID != user {
		return ErrPersonalSession
	}
	incarnation, err := personalAuthority(tx, p.Actor, p.Now)
	if err != nil {
		return err
	}
	if _, err := loadPersonalRecipe(tx, p.Actor, incarnation, recipe.Source.RecipeID, recipe.Source.VersionID); err != nil {
		return err
	}
	if premium {
		return personalPermission(ctx, tx, p.Actor, incarnation, p.Entitlements, p.Now)
	}
	return nil
}

func pendingRecipeCount(tx *gorm.DB, user int64, week string) (int, error) {
	var count int
	err := tx.Raw(`SELECT (SELECT COUNT(*) FROM favorite_queue WHERE user_id=? AND week=? AND consumed_operation IS NULL)+(SELECT COUNT(*) FROM personal_recipe_queue WHERE user_id=? AND week=? AND consumed_operation IS NULL)`, user, week, user, week).Row().Scan(&count)
	return count, err
}

func loadFavoriteWeek(tx *gorm.DB, user int64, week string, options ...PersonalPlanning) (domain.FavoriteWeek, error) {
	out, err := loadGeneratedFavoriteWeek(tx, user, week)
	if err != nil {
		return out, err
	}
	out.Entries = []domain.QueuedRecipe{}
	var rows []personalRecipeQueue
	if err := tx.Where("user_id=? AND week=?", user, week).Order("id").Find(&rows).Error; err != nil {
		return out, err
	}
	if len(rows) == 0 {
		return out, nil
	}
	identities := []string{}
	for _, row := range rows {
		var recipe domain.Recipe
		if json.Unmarshal([]byte(row.Body), &recipe) != nil || validatePersonalSnapshot(recipe) != nil || recipe.Source.RecipeID != row.RecipeID || recipe.Source.VersionID != row.VersionID {
			return out, ErrPersonalStorage
		}
		out.AddedIDs = append(out.AddedIDs, recipe.ID)
		if row.ConsumedOperation != nil {
			continue
		}
		if err := personalAssignmentAuthority(tx.Statement.Context, tx, user, recipe, options, false); err != nil {
			return out, err
		}
		p := planningOption(options)
		incarnation, err := personalAuthority(tx, p.Actor, p.Now)
		if err != nil {
			return out, err
		}
		permission := personalPermission(tx.Statement.Context, tx, p.Actor, incarnation, p.Entitlements, p.Now)
		if permission != nil && !errors.Is(permission, ErrPersonalPermission) {
			return out, permission
		}
		blocked := permission != nil
		out.Recipes = append(out.Recipes, recipe)
		out.Entries = append(out.Entries, domain.QueuedRecipe{ID: row.ID, Recipe: recipe, Blocked: blocked})
		identities = append(identities, row.ID+":"+row.VersionID+":"+domain.ShoppingHash(row.Body)+":"+strconv.FormatBool(blocked))
	}
	if len(identities) > 0 {
		encoded, _ := json.Marshal(struct {
			Generated string
			Personal  []string
		}{out.Revision, identities})
		out.Revision = domain.ShoppingHash(string(encoded))
	}
	return out, nil
}

func queuePersonalSnapshot(ctx context.Context, tx *gorm.DB, user int64, week string, recipe domain.Recipe, options []PersonalPlanning) (personalRecipeQueue, error) {
	if _, err := shoppingWeek(week); err != nil {
		return personalRecipeQueue{}, ErrPersonalInput
	}
	if err := personalAssignmentAuthority(ctx, tx, user, recipe, options, true); err != nil {
		return personalRecipeQueue{}, err
	}
	var prior personalRecipeQueue
	err := tx.Where("user_id=? AND week=? AND recipe_id=?", user, week, recipe.Source.RecipeID).Take(&prior).Error
	if err == nil {
		if prior.VersionID != recipe.Source.VersionID {
			return personalRecipeQueue{}, ErrFavoriteConflict
		}
		return prior, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return personalRecipeQueue{}, ErrPersonalStorage
	}
	count, err := pendingRecipeCount(tx, user, week)
	if err != nil {
		return personalRecipeQueue{}, ErrPersonalStorage
	}
	if count >= 7 {
		return personalRecipeQueue{}, ErrFavoriteConflict
	}
	p := planningOption(options)
	incarnation, err := personalAuthority(tx, p.Actor, p.Now)
	if err != nil {
		return personalRecipeQueue{}, err
	}
	id, err := randomLifecycleID()
	if err != nil {
		return personalRecipeQueue{}, err
	}
	body, err := json.Marshal(recipe)
	if err != nil {
		return personalRecipeQueue{}, ErrPersonalStorage
	}
	row := personalRecipeQueue{ID: id, UserID: user, Incarnation: incarnation, RecipeID: recipe.Source.RecipeID, VersionID: recipe.Source.VersionID, Week: week, Body: string(body)}
	if err := tx.Create(&row).Error; err != nil {
		return personalRecipeQueue{}, ErrPersonalStorage
	}
	return row, nil
}

func personalAssignmentKeyAvailable(tx *gorm.DB, actor PersonalActor, incarnation, key string) error {
	if err := unusedPersonalPhotoKey(tx, actor, incarnation, key); err != nil {
		return err
	}
	var count int
	if err := tx.Raw(`SELECT (SELECT COUNT(*) FROM personal_recipe_operations WHERE user_id=? AND incarnation=? AND request_key=?)+(SELECT COUNT(*) FROM personal_queue_receipts WHERE user_id=? AND incarnation=? AND request_key=?)+(SELECT COUNT(*) FROM favorite_swaps WHERE user_id=? AND request_key=?)`, actor.UserID, incarnation, key, actor.UserID, incarnation, key, actor.UserID, key).Row().Scan(&count); err != nil {
		return ErrPersonalStorage
	}
	if count != 0 {
		return ErrPersonalKeyConflict
	}
	return nil
}

func (s *Store) QueuePersonalRecipe(ctx context.Context, actor PersonalActor, reader PersonalEntitlementReader, id, version, week, key string, now time.Time) (domain.FavoriteWeek, error) {
	if !validPersonalID(id) || !validPersonalID(version) || !validPersonalID(key) {
		return domain.FavoriteWeek{}, ErrPersonalInput
	}
	if _, err := shoppingWeek(week); err != nil {
		return domain.FavoriteWeek{}, ErrPersonalInput
	}
	hash, _ := personalRequestHash([]string{"queue", id, version, week})
	p := PersonalPlanning{Actor: actor, Entitlements: reader, Now: now}
	var out domain.FavoriteWeek
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		incarnation, err := personalAuthority(tx, actor, now)
		if err != nil {
			return err
		}
		if err := personalPermission(ctx, tx, actor, incarnation, reader, now); err != nil {
			return err
		}
		var prior personalQueueReceipt
		err = tx.Where("user_id=? AND incarnation=? AND request_key=?", actor.UserID, incarnation, key).Take(&prior).Error
		if err == nil {
			if prior.Action != "queue" || prior.RequestHash != hash {
				return ErrPersonalKeyConflict
			}
			if prior.State != "active" {
				return ErrPersonalDeleted
			}
			out, err = loadFavoriteWeek(tx, actor.UserID, week, p)
			return err
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrPersonalStorage
		}
		if err := personalAssignmentKeyAvailable(tx, actor, incarnation, key); err != nil {
			return err
		}
		current, err := loadPersonalRecipe(tx, actor, incarnation, id, "")
		if err != nil {
			return err
		}
		if current.VersionID != version {
			return &PersonalVersionConflict{Current: current, Fields: []string{"recipe"}}
		}
		snapshot, err := personalRecipeSnapshot(tx, actor, current)
		if err != nil {
			return err
		}
		row, err := queuePersonalSnapshot(ctx, tx, actor.UserID, week, snapshot, []PersonalPlanning{p})
		if err != nil {
			return err
		}
		receipt := personalQueueReceipt{UserID: actor.UserID, Incarnation: incarnation, RequestKey: key, RequestHash: hash, Action: "queue", QueueID: row.ID, RecipeID: id, VersionID: version, Week: week, State: "active"}
		if err := tx.Create(&receipt).Error; err != nil {
			return ErrPersonalStorage
		}
		out, err = loadFavoriteWeek(tx, actor.UserID, week, p)
		return err
	})
	return out, err
}

func (s *Store) RemovePersonalQueuedRecipe(ctx context.Context, actor PersonalActor, id, key string, now time.Time) (domain.FavoriteWeek, error) {
	if !validPersonalID(id) || !validPersonalID(key) {
		return domain.FavoriteWeek{}, ErrPersonalInput
	}
	hash, _ := personalRequestHash([]string{"remove", id})
	var out domain.FavoriteWeek
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		incarnation, err := personalAuthority(tx, actor, now)
		if err != nil {
			return err
		}
		var prior personalQueueReceipt
		err = tx.Where("user_id=? AND incarnation=? AND request_key=?", actor.UserID, incarnation, key).Take(&prior).Error
		if err == nil {
			if prior.Action != "remove" || prior.RequestHash != hash {
				return ErrPersonalKeyConflict
			}
			out, err = loadFavoriteWeek(tx, actor.UserID, prior.Week, PersonalPlanning{Actor: actor, Now: now})
			return err
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrPersonalStorage
		}
		if err := personalAssignmentKeyAvailable(tx, actor, incarnation, key); err != nil {
			return err
		}
		var row personalRecipeQueue
		if err := tx.Where("user_id=? AND incarnation=? AND id=? AND consumed_operation IS NULL", actor.UserID, incarnation, id).Take(&row).Error; err != nil {
			return ErrPersonalNotFound
		}
		if err := tx.Where("id=? AND user_id=?", id, actor.UserID).Delete(&personalRecipeQueue{}).Error; err != nil {
			return ErrPersonalStorage
		}
		if err := tx.Model(&personalQueueReceipt{}).Where("user_id=? AND incarnation=? AND queue_id=? AND state='active'", actor.UserID, incarnation, id).Update("state", "removed").Error; err != nil {
			return ErrPersonalStorage
		}
		if err := tx.Create(&personalQueueReceipt{UserID: actor.UserID, Incarnation: incarnation, RequestKey: key, RequestHash: hash, Action: "remove", QueueID: id, RecipeID: row.RecipeID, VersionID: row.VersionID, Week: row.Week, State: "removed"}).Error; err != nil {
			return ErrPersonalStorage
		}
		out, err = loadFavoriteWeek(tx, actor.UserID, row.Week, PersonalPlanning{Actor: actor, Now: now})
		return err
	})
	return out, err
}

func (s *Store) SwapPersonalRecipe(ctx context.Context, actor PersonalActor, reader PersonalEntitlementReader, id, version string, input domain.FavoriteSwap, now time.Time) (domain.Recipe, error) {
	if !validPersonalID(id) || !validPersonalID(version) || !validPersonalID(input.RequestKey) {
		return domain.Recipe{}, ErrPersonalInput
	}
	hash, _ := personalRequestHash(struct {
		Kind, RecipeID, VersionID string
		Input                     domain.FavoriteSwap
	}{"personal", id, version, input})
	p := PersonalPlanning{Actor: actor, Entitlements: reader, Now: now}
	return s.swapSavedRecipe(ctx, actor.UserID, input, hash, func(tx *gorm.DB) (domain.Recipe, error) {
		incarnation, err := personalAuthority(tx, actor, now)
		if err != nil {
			return domain.Recipe{}, err
		}
		if err := personalPermission(ctx, tx, actor, incarnation, reader, now); err != nil {
			return domain.Recipe{}, err
		}
		current, err := loadPersonalRecipe(tx, actor, incarnation, id, "")
		if err != nil {
			return domain.Recipe{}, err
		}
		if current.VersionID != version {
			return domain.Recipe{}, &PersonalVersionConflict{Current: current, Fields: []string{"recipe"}}
		}
		return personalRecipeSnapshot(tx, actor, current)
	}, []PersonalPlanning{p})
}
