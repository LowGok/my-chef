package store

import (
	"archive/zip"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/LowGok/my-chef/internal/domain"
	"gorm.io/gorm"
)

var (
	ErrAccountAccess = errors.New("current account authentication required")
	ErrAccountExport = errors.New("account export unavailable")
)

// PasswordHash is the exact credential verified by the authentication layer.
// Rechecking it and the session inside the snapshot prevents a stale step-up.
type AccountActor struct {
	UserID       int64
	SessionHash  string
	PasswordHash string
}

func accountAuthority(tx *gorm.DB, actor AccountActor, now time.Time) error {
	if actor.UserID <= 0 || actor.SessionHash == "" || actor.PasswordHash == "" {
		return ErrAccountAccess
	}
	var count int64
	if err := tx.Raw(`SELECT COUNT(*) FROM users u JOIN sessions s ON s.user_id=u.user_id
 WHERE u.user_id=? AND u.password=? AND s.hash=? AND s.expires_unix>?`, actor.UserID, actor.PasswordHash, actor.SessionHash, now.Unix()).Scan(&count).Error; err != nil {
		return ErrAccountExport
	}
	if count != 1 {
		return ErrAccountAccess
	}
	return nil
}

type exportPart struct {
	Name  string
	Query string
	IDs   map[string]string
	JSON  map[string]string
}

var accountExportParts = []exportPart{
	{Name: "weekly-reminders.json", Query: `SELECT p.zone,p.current_enabled AS currentEnabled,p.advance_enabled AS advanceEnabled,p.advance_day AS advanceWeekday,p.advance_minute AS advanceMinute FROM reminder_preferences p JOIN account_incarnations a ON a.incarnation=p.incarnation WHERE a.user_id=?`},
	{Name: "entitlements.json", Query: `SELECT CASE WHEN g.source='fixture' THEN 'fixture' ELSE 'other' END AS source,
g.starts_ns AS startsNanoseconds,g.ends_ns AS endsNanoseconds,g.revoked_ns AS revokedNanoseconds
FROM entitlement_grants g JOIN account_incarnations a ON a.incarnation=g.incarnation
WHERE a.user_id=? ORDER BY g.starts_ns,g.ends_ns,g.id`},
	{Name: "native-installations.json", Query: `SELECT f.id,f.platform,f.expires_unix AS expiresUnix FROM native_families f JOIN sessions s ON s.hash=f.anchor_hash WHERE s.user_id=? ORDER BY f.id`, IDs: map[string]string{"id": "installation"}},
	{Name: "recipe-import-drafts.json"},
	{Name: "personal-import-sources.json", Query: `SELECT s.version_id AS versionId,s.source_kind AS kind,s.source_url AS url,s.source_title AS title,s.imported_ns AS importedNanoseconds FROM personal_recipe_import_sources s JOIN personal_recipe_versions v ON v.id=s.version_id JOIN personal_recipes r ON r.id=v.recipe_id WHERE r.user_id=? ORDER BY s.version_id`, IDs: map[string]string{"versionId": "personal-version"}},
	{Name: "account.json", Query: `SELECT firstname AS firstName,lastname AS lastName,sex,birthdate,email,username,email_verified_at AS emailVerifiedAt,email_changed_at AS emailChangedAt FROM users WHERE user_id=?`},
	{Name: "preferences.json", Query: `SELECT preferences_json AS current,diet,allergens,dislikes,lastmeal AS lastMeal,cooking_efforts AS cookingEfforts,cooking_technique AS cookingTechnique,haspref AS hasPreferences FROM users WHERE user_id=?`, JSON: map[string]string{"current": "preferences"}},
	{Name: "menus.json", Query: `SELECT id,created_unix AS createdUnix FROM menus WHERE user_id=? ORDER BY id`, IDs: map[string]string{"id": "menu"}},
	{Name: "recipes.json", Query: `SELECT meal_id AS id,menu_id AS menuId,creationdate AS date,recipetitle AS title,yield,preptime AS prepTime,cooktime AS cookTime,mychefnotes AS notes,equipment,instructions,allergens,allergenssafetynote AS allergenNote,ismeatorfish AS isMeatOrFish,totaltime AS totalTime,recipeimg AS imageUrl,tips_json AS tips,image_status AS imageStatus,recipe_json AS content,personal_source AS source FROM meals WHERE creator_id=? ORDER BY meal_id`, IDs: map[string]string{"id": "recipe", "menuId": "menu"}, JSON: map[string]string{"content": "content", "source": "source"}},
	{Name: "ingredients.json", Query: `SELECT i.pk_ingredients AS id,i.recipe_id AS recipeId,i.ingredient_name AS name,i.quantity,i.unit,i.date FROM ingredients i JOIN meals m ON m.meal_id=i.recipe_id WHERE m.creator_id=? ORDER BY i.pk_ingredients`, IDs: map[string]string{"id": "ingredient", "recipeId": "recipe"}},
	{Name: "planning.json", Query: `SELECT id,menu_id AS menuId,request_json AS request,status,stage,error_code AS outcome,created_unix AS createdUnix FROM generations WHERE user_id=? ORDER BY id`, IDs: map[string]string{"id": "planning", "menuId": "menu"}, JSON: map[string]string{"request": "planning"}},
	{Name: "shopping.json", Query: `SELECT week,revision,body AS list FROM shopping_lists WHERE user_id=? ORDER BY week`, JSON: map[string]string{"list": "shopping"}},
	{Name: "favorites.json", Query: `SELECT recipe_id AS recipeId,saved,created_unix AS createdUnix,body AS recipe FROM favorites WHERE user_id=? ORDER BY recipe_id`, IDs: map[string]string{"recipeId": "recipe"}, JSON: map[string]string{"recipe": "recipe"}},
	{Name: "favorite-queue.json", Query: `SELECT id,recipe_id AS recipeId,week,consumed_operation AS consumedOperation,body AS recipe FROM favorite_queue WHERE user_id=? ORDER BY id`, IDs: map[string]string{"id": "favorite-entry", "recipeId": "recipe", "consumedOperation": "planning"}, JSON: map[string]string{"recipe": "recipe"}},
	{Name: "favorite-swaps.json", Query: `SELECT body AS recipe FROM favorite_swaps WHERE user_id=? ORDER BY request_key`, JSON: map[string]string{"recipe": "recipe"}},
	{Name: "feedback.json", Query: `SELECT reference,topic AS contactReason,message,source_id AS recipeId,created_unix AS createdUnix FROM feedback_reports WHERE user_id=? ORDER BY id`, IDs: map[string]string{"recipeId": "recipe"}},
	{Name: "ratings.json", Query: `SELECT source_id AS recipeId,rating FROM feedback_ratings WHERE user_id=? ORDER BY source_id`, IDs: map[string]string{"recipeId": "recipe"}},
	{Name: "rating-receipts.json", Query: `SELECT reference,created_unix AS createdUnix FROM feedback_rating_receipts WHERE user_id=? ORDER BY id`},
	{Name: "personal-recipes.json", Query: `SELECT id,current_version AS currentVersion,source_recipe_id AS sourceRecipeId,source_yield AS sourceYield,created_ns AS createdNanoseconds,updated_ns AS updatedNanoseconds FROM personal_recipes WHERE user_id=? ORDER BY id`, IDs: map[string]string{"id": "personal-recipe", "currentVersion": "personal-version", "sourceRecipeId": "recipe"}, JSON: map[string]string{"sourceYield": "yield"}},
	{Name: "personal-versions.json", Query: `SELECT v.id,v.recipe_id AS recipeId,v.previous_id AS previousVersion,v.body AS content,v.photo_id AS photoId,v.image_url AS imageUrl,v.created_ns AS createdNanoseconds FROM personal_recipe_versions v JOIN personal_recipes r ON r.id=v.recipe_id WHERE r.user_id=? ORDER BY v.recipe_id,v.created_ns,v.id`, IDs: map[string]string{"id": "personal-version", "recipeId": "personal-recipe", "previousVersion": "personal-version", "photoId": "photo"}, JSON: map[string]string{"content": "personal-content"}},
	{Name: "personal-queue.json", Query: `SELECT id,recipe_id AS recipeId,version_id AS versionId,week,body AS recipe,consumed_operation AS consumedOperation FROM personal_recipe_queue WHERE user_id=? ORDER BY id`, IDs: map[string]string{"id": "personal-entry", "recipeId": "personal-recipe", "versionId": "personal-version", "consumedOperation": "planning"}, JSON: map[string]string{"recipe": "recipe"}},
	{Name: "personal-photos.json", Query: `SELECT id,id AS file,width,height,size,digest AS sha256 FROM personal_photo_assets WHERE user_id=? AND (` + personalPhotoReferences + `) ORDER BY id`, IDs: map[string]string{"id": "photo", "file": "photo-file"}},
}

type exportMapper struct{ key [32]byte }

func (m exportMapper) id(kind, value string) string {
	if kind == "photo-file" {
		return "photos/" + m.id("photo", value) + ".jpg"
	}
	mac := hmac.New(sha256.New, m.key[:])
	_, _ = io.WriteString(mac, kind+":"+value)
	return kind + "-" + hex.EncodeToString(mac.Sum(nil)[:16])
}
func (m exportMapper) recipe(r domain.Recipe) domain.Recipe {
	sourceScope := ""
	if r.Source != nil {
		sourceScope = r.Source.RecipeID + ":" + r.Source.VersionID
	}
	if r.Source != nil && r.ID == "personal:"+r.Source.RecipeID {
		r.ID = m.id("personal-recipe", r.Source.RecipeID)
	} else {
		r.ID = m.id("recipe", r.ID)
	}
	r.ImageURL = m.imageURL(r.ImageURL)
	r.Source = m.source(r.Source)
	if r.MenuID != nil {
		r.MenuID = Ptr(m.id("menu", *r.MenuID))
	}
	for i := range r.Ingredients {
		id := r.Ingredients[i].ID
		if sourceScope != "" && strings.HasPrefix(id, "personal:") {
			id = sourceScope + ":" + id
		}
		r.Ingredients[i].ID = m.id("ingredient", id)
	}
	r.LegacyRaw = nil
	return r
}
func (m exportMapper) source(value *domain.RecipeSource) *domain.RecipeSource {
	if value == nil {
		return nil
	}
	out := *value
	out.RecipeID = m.id("personal-recipe", out.RecipeID)
	out.VersionID = m.id("personal-version", out.VersionID)
	if out.PhotoID != nil {
		out.PhotoID = Ptr(m.id("photo", *out.PhotoID))
	}
	return &out
}
func (m exportMapper) imageURL(value *string) *string {
	if value == nil {
		return nil
	}
	const prefix = "/api/v1/personal-photos/"
	if strings.HasPrefix(*value, prefix) {
		id := strings.TrimPrefix(*value, prefix)
		if validPersonalID(id) {
			return Ptr(m.id("photo-file", id))
		}
		return nil
	}
	return value
}
func (m exportMapper) jsonValue(kind, value string) (any, error) {
	switch kind {
	case "source":
		var source *domain.RecipeSource
		if json.Unmarshal([]byte(value), &source) != nil {
			return nil, ErrAccountExport
		}
		return m.source(source), nil
	case "yield":
		var yield []string
		err := json.Unmarshal([]byte(value), &yield)
		return yield, err
	case "personal-content":
		var recipe domain.PersonalRecipeContent
		if json.Unmarshal([]byte(value), &recipe) != nil {
			return nil, ErrAccountExport
		}
		if _, err := recipe.Normalize(); err != nil {
			return nil, ErrAccountExport
		}
		return recipe, nil
	case "preferences":
		var p domain.Preferences
		err := json.Unmarshal([]byte(value), &p)
		return p, err
	case "content":
		var r domain.RecipeContent
		err := json.Unmarshal([]byte(value), &r)
		return r, err
	case "recipe":
		var r domain.Recipe
		if err := json.Unmarshal([]byte(value), &r); err != nil {
			return nil, err
		}
		return m.recipe(r), nil
	case "shopping":
		var list domain.ShoppingList
		if err := json.Unmarshal([]byte(value), &list); err != nil {
			return nil, err
		}
		list.SourceVersion = ""
		for i := range list.Meals {
			list.Meals[i].RecipeID = m.id("recipe", list.Meals[i].RecipeID)
		}
		for i := range list.Items {
			list.Items[i].ID = m.id("shopping-item", list.Items[i].ID)
			for k := range list.Items[i].Contributions {
				list.Items[i].Contributions[k].RecipeID = m.id("recipe", list.Items[i].Contributions[k].RecipeID)
			}
		}
		return list, nil
	case "planning":
		var request domain.GenerationRequest
		if err := json.Unmarshal([]byte(value), &request); err != nil {
			return nil, err
		}
		request.FavoriteRevisions = nil
		favorites := make([]map[string]any, 0, len(request.Favorites))
		for _, favorite := range request.Favorites {
			entry := m.id("favorite-entry", strconv.FormatInt(favorite.EntryID, 10))
			if favorite.PersonalEntryID != nil {
				entry = m.id("personal-entry", *favorite.PersonalEntryID)
			}
			favorites = append(favorites, map[string]any{"entryId": entry, "date": favorite.Date, "recipe": m.recipe(favorite.Recipe)})
		}
		return map[string]any{"input": request.MenuInput, "preferences": request.Preferences, "currentDate": request.CurrentDate, "favorites": favorites}, nil
	}
	return nil, ErrAccountExport
}
func (m exportMapper) row(part exportPart, columns []string, values []any) (map[string]any, error) {
	out := make(map[string]any, len(columns))
	for i, name := range columns {
		value := values[i]
		if value != nil {
			if name == "imageUrl" {
				if text, ok := value.(string); ok {
					image := m.imageURL(&text)
					if image == nil {
						value = nil
					} else {
						value = *image
					}
				}
			}
			if kind := part.IDs[name]; kind != "" {
				value = m.id(kind, fmt.Sprint(value))
			}
			if kind := part.JSON[name]; kind != "" {
				text, ok := value.(string)
				if !ok {
					return nil, ErrAccountExport
				}
				var err error
				value, err = m.jsonValue(kind, text)
				if err != nil {
					return nil, ErrAccountExport
				}
			}
		}
		out[name] = value
	}
	return out, nil
}
func writeExportPart(ctx context.Context, tx *gorm.DB, part exportPart, user int64, m exportMapper, w io.Writer, options ...AccountExportOptions) error {
	if part.Name == "recipe-import-drafts.json" {
		var opts AccountExportOptions
		if len(options) > 0 {
			opts = options[0]
		}
		return writeRetainedImportDrafts(ctx, tx, user, m, w, opts)
	}
	rows, err := tx.Raw(part.Query, user).Rows()
	if err != nil {
		return ErrAccountExport
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	if err != nil {
		return ErrAccountExport
	}
	if _, err = io.WriteString(w, "[\n"); err != nil {
		return err
	}
	first := true
	for rows.Next() {
		if err = ctx.Err(); err != nil {
			return err
		}
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err = rows.Scan(pointers...); err != nil {
			return ErrAccountExport
		}
		object, err := m.row(part, columns, values)
		if err != nil {
			return err
		}
		if !first {
			if _, err = io.WriteString(w, ",\n"); err != nil {
				return err
			}
		}
		first = false
		if err = json.NewEncoder(w).Encode(object); err != nil {
			return err
		}
	}
	if rows.Err() != nil {
		return ErrAccountExport
	}
	_, err = io.WriteString(w, "\n]\n")
	return err
}

// WriteAccountExport performs a bounded two-pass traversal inside one snapshot.
// No archive or complete category is buffered or persisted by the service.
// On any failure the ZIP is intentionally not finalized.
func (s *Store) WriteAccountExport(ctx context.Context, actor AccountActor, w io.Writer, now time.Time, options ...AccountExportOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	tx := s.DB.WithContext(ctx).Begin(&sql.TxOptions{ReadOnly: true})
	if tx.Error != nil {
		return ErrAccountExport
	}
	defer func() { _ = tx.Rollback().Error }()
	if err := accountAuthority(tx, actor, now); err != nil {
		return err
	}
	var opts AccountExportOptions
	if len(options) > 0 {
		opts = options[0]
	}
	opts.now = now
	var mapper exportMapper
	if _, err := rand.Read(mapper.key[:]); err != nil {
		return ErrAccountExport
	}
	for _, part := range accountExportParts {
		if err := writeExportPart(ctx, tx, part, actor.UserID, mapper, io.Discard, opts); err != nil {
			return err
		}
	}
	if err := eachAccountPhoto(ctx, tx, actor.UserID, func(asset PersonalPhotoAsset) error {
		data, err := readAccountPhoto(ctx, opts, asset)
		clear(data)
		return err
	}); err != nil {
		return err
	}
	archive := zip.NewWriter(w)
	manifest, err := archive.Create("manifest.json")
	if err != nil {
		return err
	}
	if err = writeAccountManifest(ctx, tx, actor.UserID, mapper, manifest, now); err != nil {
		return err
	}
	guide, err := archive.Create("README.txt")
	if err != nil {
		return err
	}
	if _, err = io.WriteString(guide, "MyChef account data\n\nVersion 2. JSON files are arrays grouped by category.\nThis includes owned history, favorites, shopping choices, planning inputs, feedback and private ratings.\nPersonal recipes include every retained saved version, source links and queued snapshots. Deleted library sources may still be referenced by planned meals.\nRecipe imports include saved source attribution and still-retained private drafts. Source uploads and dispatch capabilities are excluded.\nRelated records use identifiers local to this download. Normalized uploaded photos are in photos/; personal-photos.json maps their identifiers to files. Existing generated images remain URL references.\nWeekly planning reminders include preferences and exclude push tokens.\nEntitlements include grant categories and dates; internal grant and fixture identifiers are excluded.\nNative installation metadata includes only its platform, expiry and export-local identifier. Passwords, session credentials, browser sessions, verification links, private provider targets, upload receipts, security logs and opaque legacy fields are excluded.\nKeep this archive private. MyChef does not retain a copy or send it by email.\n"); err != nil {
		return err
	}
	for _, part := range accountExportParts {
		file, err := archive.Create(part.Name)
		if err != nil {
			return err
		}
		if err = writeExportPart(ctx, tx, part, actor.UserID, mapper, file, opts); err != nil {
			return err
		}
	}
	if err := eachAccountPhoto(ctx, tx, actor.UserID, func(asset PersonalPhotoAsset) error {
		data, err := readAccountPhoto(ctx, opts, asset)
		if err != nil {
			return err
		}
		defer clear(data)
		file, err := archive.CreateHeader(&zip.FileHeader{Name: mapper.id("photo-file", asset.ID), Method: zip.Store})
		if err != nil {
			return err
		}
		_, err = file.Write(data)
		return err
	}); err != nil {
		return err
	}
	if err = tx.Commit().Error; err != nil {
		return ErrAccountExport
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return archive.Close()
}
