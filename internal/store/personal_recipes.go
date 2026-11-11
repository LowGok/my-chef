package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/LowGok/my-chef/internal/domain"
	"gorm.io/gorm"
)

var (
	ErrPersonalInput       = errors.New("invalid personal recipe request")
	ErrPersonalSession     = errors.New("verified personal recipe session required")
	ErrPersonalPermission  = errors.New("personal recipe permission required")
	ErrPersonalNotFound    = errors.New("personal recipe unavailable")
	ErrPersonalKeyConflict = errors.New("personal recipe request key conflict")
	ErrPersonalDeleted     = errors.New("personal recipe was deleted")
	ErrPersonalStorage     = errors.New("personal recipe storage unavailable")
)

type PersonalActor struct {
	UserID      int64
	SessionHash string
	Native      bool
}

type PersonalEntitlement struct {
	Premium     bool
	ExpiresUnix int64
}

type PersonalEntitlementReader func(context.Context, *gorm.DB, int64, string) (PersonalEntitlement, error)

type PersonalSave struct {
	Import                          *PersonalImportSave `json:"import,omitempty"`
	ID, ExpectedVersion, RequestKey string
	Content                         domain.PersonalRecipeContent
	SourceRecipeID                  *int64
	PhotoAction, PhotoDigest        string
}

type PersonalWriteOptions struct {
	Fault        func(string) error
	PhotoAssetID string
	Abort        bool
}

type PersonalRecipe struct {
	ImportSource     *domain.RecipeImportSource   `json:"importSource,omitempty"`
	PhotoID          *string                      `json:"photoId"`
	ImageURL         *string                      `json:"imageUrl"`
	ID               string                       `json:"id"`
	VersionID        string                       `json:"versionId"`
	CurrentVersionID string                       `json:"currentVersionId"`
	Content          domain.PersonalRecipeContent `json:"content"`
	SourceYield      []string                     `json:"sourceYield"`
	CreatedAt        string                       `json:"createdAt"`
	UpdatedAt        string                       `json:"updatedAt"`
}

type PersonalRecipePage struct {
	Recipes    []PersonalRecipe `json:"recipes"`
	NextCursor *string          `json:"nextCursor"`
}

type PersonalCopy struct {
	Draft      domain.PersonalRecipeCopyDraft `json:"draft"`
	ExistingID *string                        `json:"existingId"`
	ImageURL   *string                        `json:"imageUrl"`
}

type PersonalVersionConflict struct {
	Current PersonalRecipe `json:"current"`
	Fields  []string       `json:"fields"`
}

func (*PersonalVersionConflict) Error() string { return "A newer recipe version is available." }

type PersonalCopyExists struct{ ID string }

func (*PersonalCopyExists) Error() string { return "This source already has a personal recipe." }

type personalRoot struct {
	ID, Incarnation, CurrentVersion, Title, SearchTitle string
	UserID                                              int64
	SourceRecipeID                                      *int64
	SourceYield                                         string
	CreatedNS, UpdatedNS                                int64
}

func (personalRoot) TableName() string { return "personal_recipes" }

type personalVersion struct {
	PhotoID, ImageURL, ImagePublicID, ImageBinding *string
	ID, RecipeID                                   string
	PreviousID                                     *string
	Body                                           string
	CreatedNS                                      int64
}

func (personalVersion) TableName() string { return "personal_recipe_versions" }

type personalOperation struct {
	UserID                                                                   int64
	Incarnation, RequestKey, RequestHash, Action, RecipeID, VersionID, State string
	CreatedNS                                                                int64
}

func (personalOperation) TableName() string { return "personal_recipe_operations" }

func validPersonalID(value string) bool {
	return len(value) == 32 && strings.IndexFunc(value, func(r rune) bool {
		return r < '0' || r > '9' && r < 'a' || r > 'f'
	}) == -1
}

func personalAuthority(tx *gorm.DB, actor PersonalActor, now time.Time) (string, error) {
	if actor.UserID <= 0 || actor.SessionHash == "" {
		return "", ErrPersonalSession
	}
	var incarnation string
	err := tx.Raw(`SELECT a.incarnation FROM account_incarnations a JOIN users u ON u.user_id=a.user_id
JOIN sessions s ON s.user_id=u.user_id WHERE u.user_id=? AND s.hash=? AND s.expires_unix>?
AND u.email_verified_at IS NOT NULL AND u.email_verified_at>0`, actor.UserID, actor.SessionHash, now.Unix()).Row().Scan(&incarnation)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrPersonalSession
	}
	if err != nil {
		return "", ErrPersonalStorage
	}
	return incarnation, nil
}

func personalPermission(ctx context.Context, tx *gorm.DB, actor PersonalActor, incarnation string, reader PersonalEntitlementReader, now time.Time) error {
	if reader == nil {
		return ErrPersonalPermission
	}
	state, err := reader(ctx, tx, actor.UserID, incarnation)
	if err != nil {
		return ErrPersonalStorage
	}
	if !state.Premium || state.ExpiresUnix <= now.Unix() {
		return ErrPersonalPermission
	}
	return nil
}

func loadPersonalRecipe(tx *gorm.DB, actor PersonalActor, incarnation, id, version string) (PersonalRecipe, error) {
	var root personalRoot
	if err := tx.Where("user_id=? AND incarnation=? AND id=?", actor.UserID, incarnation, id).Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return PersonalRecipe{}, ErrPersonalNotFound
		}
		return PersonalRecipe{}, ErrPersonalStorage
	}
	if version == "" {
		version = root.CurrentVersion
	}
	var row personalVersion
	if err := tx.Where("recipe_id=? AND id=?", id, version).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return PersonalRecipe{}, ErrPersonalNotFound
		}
		return PersonalRecipe{}, ErrPersonalStorage
	}
	out := PersonalRecipe{ID: root.ID, VersionID: row.ID, CurrentVersionID: root.CurrentVersion, SourceYield: []string{},
		CreatedAt: time.Unix(0, root.CreatedNS).UTC().Format(time.RFC3339Nano), UpdatedAt: time.Unix(0, root.UpdatedNS).UTC().Format(time.RFC3339Nano)}
	if json.Unmarshal([]byte(row.Body), &out.Content) != nil || json.Unmarshal([]byte(root.SourceYield), &out.SourceYield) != nil {
		return PersonalRecipe{}, ErrPersonalStorage
	}
	if _, err := out.Content.Normalize(); err != nil {
		return PersonalRecipe{}, ErrPersonalStorage
	}
	if out.SourceYield == nil {
		out.SourceYield = []string{}
	}
	var err error
	out.ImportSource, err = loadPersonalImportSource(tx, row.ID)
	if err != nil {
		return PersonalRecipe{}, err
	}
	out.PhotoID, out.ImageURL = row.PhotoID, row.ImageURL
	if row.PhotoID != nil {
		out.ImageURL = Ptr("/api/v1/personal-photos/" + *row.PhotoID)
	}
	return out, nil
}

func (s *Store) ReadPersonalRecipe(ctx context.Context, actor PersonalActor, id, version string, now time.Time) (PersonalRecipe, error) {
	if !validPersonalID(id) || version != "" && !validPersonalID(version) {
		return PersonalRecipe{}, ErrPersonalNotFound
	}
	var out PersonalRecipe
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		incarnation, err := personalAuthority(tx, actor, now)
		if err != nil {
			return err
		}
		out, err = loadPersonalRecipe(tx, actor, incarnation, id, version)
		return err
	})
	return out, err
}

func (s *Store) SearchPersonalRecipes(ctx context.Context, actor PersonalActor, query string, limit int, cursor string, now time.Time) (PersonalRecipePage, error) {
	if limit < 1 || limit > 50 || len(cursor) > 128 {
		return PersonalRecipePage{}, ErrPersonalInput
	}
	if !utf8.ValidString(query) || strings.IndexFunc(query, unicode.IsControl) >= 0 {
		return PersonalRecipePage{}, ErrPersonalInput
	}
	q := strings.ToLower(strings.TrimSpace(query))
	if len([]rune(q)) > 200 {
		return PersonalRecipePage{}, ErrPersonalInput
	}
	var stamp int64
	var id string
	if cursor != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(cursor)
		parts := strings.SplitN(string(decoded), ":", 2)
		if err != nil || len(parts) != 2 || !validPersonalID(parts[1]) {
			return PersonalRecipePage{}, ErrPersonalInput
		}
		stamp, err = strconv.ParseInt(parts[0], 10, 64)
		if err != nil || stamp < 0 {
			return PersonalRecipePage{}, ErrPersonalInput
		}
		id = parts[1]
	}
	out := PersonalRecipePage{Recipes: []PersonalRecipe{}}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		incarnation, err := personalAuthority(tx, actor, now)
		if err != nil {
			return err
		}
		rows := []personalRoot{}
		lookup := tx.Where("user_id=? AND incarnation=?", actor.UserID, incarnation)
		if q != "" {
			lookup = lookup.Where("instr(search_title,?)>0", q)
		}
		if cursor != "" {
			lookup = lookup.Where("updated_ns<? OR (updated_ns=? AND id<?)", stamp, stamp, id)
		}
		if err := lookup.Order("updated_ns DESC,id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
			return ErrPersonalStorage
		}
		if len(rows) > limit {
			rows = rows[:limit]
			last := rows[len(rows)-1]
			out.NextCursor = Ptr(base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(last.UpdatedNS, 10) + ":" + last.ID)))
		}
		for _, row := range rows {
			recipe, err := loadPersonalRecipe(tx, actor, incarnation, row.ID, "")
			if err != nil {
				return err
			}
			out.Recipes = append(out.Recipes, recipe)
		}
		return nil
	})
	return out, err
}

func personalCopySource(tx *gorm.DB, user, id int64) (domain.Recipe, error) {
	var meal Meal
	err := tx.Where("creator_id=? AND meal_id=?", user, id).Preload("Ingredients").First(&meal).Error
	if err == nil {
		return meal.Recipe(), nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.Recipe{}, ErrPersonalStorage
	}
	recipe, err := favoriteRecipe(tx, user, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.Recipe{}, ErrPersonalNotFound
	}
	if err != nil {
		return domain.Recipe{}, ErrPersonalStorage
	}
	return recipe, nil
}

func (s *Store) CopyPersonalRecipe(ctx context.Context, actor PersonalActor, reader PersonalEntitlementReader, source int64, now time.Time) (PersonalCopy, error) {
	if source < 1 {
		return PersonalCopy{}, ErrPersonalInput
	}
	var out PersonalCopy
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		incarnation, err := personalAuthority(tx, actor, now)
		if err != nil {
			return err
		}
		if err := personalPermission(ctx, tx, actor, incarnation, reader, now); err != nil {
			return err
		}
		recipe, err := personalCopySource(tx, actor.UserID, source)
		if err != nil {
			return err
		}
		out.Draft = domain.PersonalRecipeDraft(recipe)
		out.Draft.SourceYield = append([]string{}, out.Draft.SourceYield...)
		out.Draft.Content.Instructions = append([]string{}, out.Draft.Content.Instructions...)
		out.Draft.Content.Tips = append([]string{}, out.Draft.Content.Tips...)
		out.Draft.Content.Allergens = append([]string{}, out.Draft.Content.Allergens...)
		var preview personalVersion
		if err := preparePersonalVersionPhoto(tx, actor, incarnation, PersonalSave{SourceRecipeID: &source}, &preview, nil); err != nil {
			return err
		}
		out.ImageURL = preview.ImageURL
		if preview.PhotoID != nil {
			out.ImageURL = Ptr("/api/v1/personal-photos/" + *preview.PhotoID)
		}
		var root personalRoot
		err = tx.Where("user_id=? AND incarnation=? AND source_recipe_id=?", actor.UserID, incarnation, source).Take(&root).Error
		if err == nil {
			out.ExistingID = Ptr(root.ID)
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrPersonalStorage
		}
		return nil
	})
	return out, err
}

func personalRequestHash(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", ErrPersonalInput
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

func personalFault(options []PersonalWriteOptions, stage string) error {
	if len(options) > 0 && options[0].Fault != nil {
		return options[0].Fault(stage)
	}
	return nil
}

func personalChangedFields(before, after domain.PersonalRecipeContent) []string {
	a, b := reflect.ValueOf(before), reflect.ValueOf(after)
	fields := []string{}
	for i := 0; i < a.NumField(); i++ {
		if !reflect.DeepEqual(a.Field(i).Interface(), b.Field(i).Interface()) {
			fields = append(fields, a.Type().Field(i).Tag.Get("json"))
		}
	}
	return fields
}

func personalChangedRecipeFields(before, after PersonalRecipe) []string {
	fields := personalChangedFields(before.Content, after.Content)
	if !reflect.DeepEqual(before.PhotoID, after.PhotoID) || !reflect.DeepEqual(before.ImageURL, after.ImageURL) {
		fields = append(fields, "photo")
	}
	return fields
}

func loadPersonalReceipt(tx *gorm.DB, actor PersonalActor, incarnation, key, hash, action string) (*personalOperation, error) {
	var prior personalOperation
	err := tx.Where("user_id=? AND incarnation=? AND request_key=?", actor.UserID, incarnation, key).Take(&prior).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, ErrPersonalStorage
	}
	if prior.RequestHash != hash || prior.Action != action {
		return nil, ErrPersonalKeyConflict
	}
	return &prior, nil
}

func (s *Store) SavePersonalRecipe(ctx context.Context, actor PersonalActor, reader PersonalEntitlementReader, input PersonalSave, now time.Time, options ...PersonalWriteOptions) (PersonalRecipe, error) {
	return savePersonalRecipeOn(ctx, s.DB, actor, reader, input, now, options...)
}

func normalizePersonalSave(input PersonalSave) (PersonalSave, error) {
	if input.Import != nil {
		if !validPersonalID(input.Import.OperationID) || input.ID != "" || input.SourceRecipeID != nil {
			return input, ErrPersonalInput
		}
		var err error
		input.Content, err = retainImportWarning(input.Content)
		if err != nil {
			return input, err
		}
	}
	if !validPersonalID(input.RequestKey) || input.ID != "" && (!validPersonalID(input.ID) || !validPersonalID(input.ExpectedVersion)) || input.ID == "" && input.ExpectedVersion != "" || input.SourceRecipeID != nil && (input.ID != "" || *input.SourceRecipeID < 1) {
		return input, ErrPersonalInput
	}
	if input.PhotoAction != "" && input.PhotoAction != "keep" && input.PhotoAction != "remove" && input.PhotoAction != "replace" || input.PhotoAction == "replace" && !validPersonalDigest(input.PhotoDigest) || input.PhotoAction != "replace" && input.PhotoDigest != "" {
		return input, ErrPersonalInput
	}
	content, err := input.Content.Normalize()
	if err != nil {
		return input, err
	}
	input.Content = content
	return input, nil
}

func savePersonalRecipeOn(ctx context.Context, db *gorm.DB, actor PersonalActor, reader PersonalEntitlementReader, input PersonalSave, now time.Time, options ...PersonalWriteOptions) (PersonalRecipe, error) {
	input, err := normalizePersonalSave(input)
	if err != nil {
		return PersonalRecipe{}, err
	}
	content := input.Content
	hash, err := personalRequestHash(input)
	if err != nil {
		return PersonalRecipe{}, err
	}
	var out PersonalRecipe
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		incarnation, err := personalAuthority(tx, actor, now)
		if err != nil {
			return err
		}
		if err := personalPermission(ctx, tx, actor, incarnation, reader, now); err != nil {
			return err
		}
		prior, err := loadPersonalReceipt(tx, actor, incarnation, input.RequestKey, hash, "save")
		if err != nil {
			return err
		}
		if prior != nil {
			if prior.State == "deleted" {
				return ErrPersonalDeleted
			}
			out, err = loadPersonalRecipe(tx, actor, incarnation, prior.RecipeID, prior.VersionID)
			return err
		}
		if input.PhotoAction != "replace" {
			if err := personalAssignmentKeyAvailable(tx, actor, incarnation, input.RequestKey); err != nil {
				return err
			}
		}
		importOperation, provenance, importYield, err := prepareReviewedImport(tx, actor, incarnation, input, now)
		if err != nil {
			return err
		}
		root := personalRoot{ID: input.ID, UserID: actor.UserID, Incarnation: incarnation}
		var previous *string
		if input.ID != "" {
			current, err := loadPersonalRecipe(tx, actor, incarnation, input.ID, "")
			if err != nil {
				return err
			}
			if current.VersionID != input.ExpectedVersion {
				fields := []string{"recipe"}
				if base, err := loadPersonalRecipe(tx, actor, incarnation, input.ID, input.ExpectedVersion); err == nil {
					fields = personalChangedRecipeFields(base, current)
				}
				return &PersonalVersionConflict{Current: current, Fields: fields}
			}
			previous = Ptr(current.VersionID)
			provenance = current.ImportSource
		} else {
			root.ID, err = randomLifecycleID()
			if err != nil {
				return ErrPersonalStorage
			}
			root.CreatedNS, root.UpdatedNS = now.UnixNano(), now.UnixNano()
			root.SourceYield = "[]"
			if provenance != nil {
				if importYield == nil {
					importYield = []string{}
				}
				encoded, err := json.Marshal(importYield)
				if err != nil {
					return ErrPersonalInput
				}
				root.SourceYield = string(encoded)
			}
			if input.SourceRecipeID != nil {
				source, err := personalCopySource(tx, actor.UserID, *input.SourceRecipeID)
				if err != nil {
					return err
				}
				var existing personalRoot
				err = tx.Where("user_id=? AND incarnation=? AND source_recipe_id=?", actor.UserID, incarnation, *input.SourceRecipeID).Take(&existing).Error
				if err == nil {
					return &PersonalCopyExists{ID: existing.ID}
				}
				if !errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrPersonalStorage
				}
				root.SourceRecipeID = input.SourceRecipeID
				if source.Source != nil && source.Source.Import != nil {
					if _, err := checkedImportSource(source.Source.Import); err != nil {
						return err
					}
					provenance = source.Source.Import
				}
				yield := source.Yield
				if yield == nil {
					yield = []string{}
				}
				encoded, err := json.Marshal(yield)
				if err != nil || len(encoded) > 24<<10 {
					return ErrPersonalInput
				}
				root.SourceYield = string(encoded)
			}
		}
		if provenance != nil {
			content, err = retainImportWarning(content)
			if err != nil {
				return err
			}
		}
		body, err := json.Marshal(content)
		if err != nil {
			return ErrPersonalInput
		}
		versionID, err := randomLifecycleID()
		if err != nil {
			return ErrPersonalStorage
		}
		version := personalVersion{ID: versionID, RecipeID: root.ID, PreviousID: previous, Body: string(body), CreatedNS: now.UnixNano()}
		if err := preparePersonalVersionPhoto(tx, actor, incarnation, input, &version, options); err != nil {
			return err
		}
		if input.ID == "" {
			root.CurrentVersion, root.Title, root.SearchTitle = versionID, content.Title, strings.ToLower(content.Title)
			if err := tx.Create(&root).Error; err != nil {
				return ErrPersonalStorage
			}
		}
		if err := personalFault(options, "root"); err != nil {
			return err
		}
		if err := tx.Create(&version).Error; err != nil {
			return ErrPersonalStorage
		}
		if err := personalFault(options, "version"); err != nil {
			return err
		}
		if err := attachPersonalImport(tx, provenance, importOperation, root.ID, versionID, now, options); err != nil {
			return err
		}
		if input.ID != "" {
			result := tx.Model(&personalRoot{}).Where("id=? AND user_id=? AND incarnation=? AND current_version=?", root.ID, actor.UserID, incarnation, input.ExpectedVersion).Updates(map[string]any{"current_version": versionID, "title": content.Title, "search_title": strings.ToLower(content.Title), "updated_ns": now.UnixNano()})
			if result.Error != nil || result.RowsAffected != 1 {
				return ErrPersonalStorage
			}
		}
		if err := personalFault(options, "current"); err != nil {
			return err
		}
		if err := tx.Create(&personalOperation{UserID: actor.UserID, Incarnation: incarnation, RequestKey: input.RequestKey, RequestHash: hash, Action: "save", RecipeID: root.ID, VersionID: versionID, State: "active", CreatedNS: now.UnixNano()}).Error; err != nil {
			return ErrPersonalStorage
		}
		if err := personalFault(options, "receipt"); err != nil {
			return err
		}
		out, err = loadPersonalRecipe(tx, actor, incarnation, root.ID, versionID)
		return err
	})
	return out, err
}

func (s *Store) DeletePersonalRecipe(ctx context.Context, actor PersonalActor, id, expected, key string, now time.Time, options ...PersonalWriteOptions) error {
	if !validPersonalID(id) || !validPersonalID(expected) || !validPersonalID(key) {
		return ErrPersonalInput
	}
	hash, err := personalRequestHash([]string{id, expected})
	if err != nil {
		return err
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		incarnation, err := personalAuthority(tx, actor, now)
		if err != nil {
			return err
		}
		prior, err := loadPersonalReceipt(tx, actor, incarnation, key, hash, "delete")
		if err != nil {
			return err
		}
		if prior != nil {
			return nil
		}
		if err := personalAssignmentKeyAvailable(tx, actor, incarnation, key); err != nil {
			return err
		}
		current, err := loadPersonalRecipe(tx, actor, incarnation, id, "")
		if err != nil {
			return err
		}
		if current.VersionID != expected {
			return &PersonalVersionConflict{Current: current, Fields: []string{"recipe"}}
		}
		if err := tx.Model(&personalOperation{}).Where("user_id=? AND incarnation=? AND recipe_id=?", actor.UserID, incarnation, id).Update("state", "deleted").Error; err != nil {
			return ErrPersonalStorage
		}
		if err := tx.Model(&personalQueueReceipt{}).Where("user_id=? AND incarnation=? AND recipe_id=?", actor.UserID, incarnation, id).Update("state", "deleted").Error; err != nil {
			return ErrPersonalStorage
		}
		for _, table := range []string{"favorites", "favorite_queue"} {
			if err := tx.Exec("DELETE FROM "+table+" WHERE user_id=? AND CASE WHEN json_valid(body) THEN json_extract(body,'$.source.kind')='personal' AND json_extract(body,'$.source.recipeId')=? ELSE 0 END", actor.UserID, id).Error; err != nil {
				return ErrPersonalStorage
			}
		}
		if err := tx.Where("id=? AND user_id=? AND incarnation=?", id, actor.UserID, incarnation).Delete(&personalRoot{}).Error; err != nil {
			return ErrPersonalStorage
		}
		if err := personalFault(options, "delete"); err != nil {
			return err
		}
		if err := tx.Create(&personalOperation{UserID: actor.UserID, Incarnation: incarnation, RequestKey: key, RequestHash: hash, Action: "delete", RecipeID: id, VersionID: expected, State: "deleted", CreatedNS: now.UnixNano()}).Error; err != nil {
			return ErrPersonalStorage
		}
		return personalFault(options, "receipt")
	})
}
