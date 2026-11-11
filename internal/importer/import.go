package importer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/LowGok/my-chef/internal/domain"
	"github.com/LowGok/my-chef/internal/store"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type Warning struct {
	Table  string `json:"table"`
	Column string `json:"column"`
	ID     string `json:"id"`
}
type Report struct {
	Destination string    `json:"destination"`
	Users       int       `json:"users"`
	Meals       int       `json:"meals"`
	Ingredients int       `json:"ingredients"`
	Warnings    []Warning `json:"legacyReview"`
}

func Import(ctx context.Context, source, destination, schema string) (Report, error) {
	report := Report{Warnings: []Warning{}}
	target, err := filepath.Abs(destination)
	if err != nil {
		return report, err
	}
	report.Destination = target
	if _, err = os.Lstat(target); err == nil {
		return report, &Error{Code: "destination_exists"}
	} else if !os.IsNotExist(err) {
		return report, &Error{Code: "destination_unavailable"}
	}
	src, err := openSnapshot(ctx, source, schema)
	if err != nil {
		return report, err
	}
	defer src.close()
	if err = src.validate(ctx); err != nil {
		return report, sourceError(err, "")
	}
	temp, err := os.CreateTemp(filepath.Dir(target), ".mychef-import-*.db")
	if err != nil {
		return report, &Error{Code: "destination_unavailable"}
	}
	name := temp.Name()
	if err = temp.Close(); err != nil {
		return report, err
	}
	defer func() {
		for _, suffix := range []string{"", "-wal", "-shm"} {
			_ = os.Remove(name + suffix)
		}
	}()
	db, err := store.Open(name, false)
	if err != nil {
		return report, err
	}
	defer func() { _ = db.Close() }()
	if err = db.Migrate(ctx); err != nil {
		return report, &Error{Code: "target_schema"}
	}
	err = db.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if e := copyUsers(ctx, src, tx, &report); e != nil {
			return e
		}
		if e := copyMeals(ctx, src, tx, &report); e != nil {
			return e
		}
		if e := copyIngredients(ctx, src, tx, &report); e != nil {
			return e
		}
		rows, e := tx.Raw("PRAGMA foreign_key_check").Rows()
		if e != nil {
			return e
		}
		bad := rows.Next()
		rowError := rows.Err()
		_ = rows.Close()
		if bad || rowError != nil {
			return &Error{Code: "target_integrity"}
		}
		var integrity string
		if e = tx.Raw("PRAGMA integrity_check").Scan(&integrity).Error; e != nil || integrity != "ok" {
			return &Error{Code: "target_integrity"}
		}
		return nil
	})
	if err != nil {
		return report, err
	}
	var busy, log, checkpointed int
	if err = db.SQL.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &checkpointed); err != nil || busy != 0 {
		return report, &Error{Code: "target_checkpoint"}
	}
	if err = db.Close(); err != nil {
		return report, err
	}
	file, err := os.Open(name)
	if err != nil {
		return report, err
	}
	err = file.Sync()
	_ = file.Close()
	if err != nil {
		return report, err
	}
	if err = ctx.Err(); err != nil {
		return report, err
	}
	// A same-directory hard link publishes the complete closed database atomically
	// and refuses to replace a destination created by another process meanwhile.
	if err = os.Link(name, target); err != nil {
		return report, &Error{Code: "destination_publish"}
	}
	directory, err := os.Open(filepath.Dir(target))
	if err == nil {
		err = directory.Sync()
		_ = directory.Close()
	}
	if err != nil {
		return report, &Error{Code: "destination_sync"}
	}
	sort.Slice(report.Warnings, func(i, j int) bool {
		a, b := report.Warnings[i], report.Warnings[j]
		if a.Table != b.Table {
			return a.Table < b.Table
		}
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		return a.Column < b.Column
	})
	return report, nil
}
func validDate(value *string) bool {
	if value == nil {
		return true
	}
	d, e := time.Parse(time.DateOnly, *value)
	return e == nil && d.Format(time.DateOnly) == *value
}
func valueError(table, column string, id int64) error {
	return &Error{Code: "unsupported_value", Table: table, Column: column, ID: strconv.FormatInt(id, 10)}
}
func targetError(err error, table string) error {
	var known *Error
	if errors.As(err, &known) {
		return err
	}
	return &Error{Code: "target_integrity", Table: table}
}

func copyUsers(ctx context.Context, src *snapshot, tx *gorm.DB, report *Report) error {
	rows, err := src.tx.Query(ctx, "SELECT user_id,firstname,lastname,sex,birthdate::text,email,password,username,allergens,diet,dislikes,lastmeal::text,cooking_efforts,cooking_technique,haspref FROM "+src.table("users")+" ORDER BY user_id")
	if err != nil {
		return sourceError(err, "users")
	}
	defer rows.Close()
	for rows.Next() {
		var u store.User
		if err = rows.Scan(&u.ID, &u.FirstName, &u.LastName, &u.Sex, &u.Birthdate, &u.Email, &u.Password, &u.Username, &u.Allergens, &u.Diet, &u.Dislikes, &u.LastMeal, &u.CookingEfforts, &u.CookingTechnique, &u.HasPref); err != nil {
			return sourceError(err, "users")
		}
		if u.ID <= 0 {
			return valueError("users", "user_id", u.ID)
		}
		if _, err = bcrypt.Cost([]byte(u.Password)); err != nil {
			return valueError("users", "password", u.ID)
		}
		if !validDate(u.Birthdate) || !validDate(u.LastMeal) {
			return valueError("users", "date", u.ID)
		}
		if err = tx.Create(&u).Error; err != nil {
			return targetError(err, "users")
		}
		report.Users++
	}
	return sourceErrorIfAny(rows.Err(), "users")
}
func copyMeals(ctx context.Context, src *snapshot, tx *gorm.DB, report *Report) error {
	rows, err := src.tx.Query(ctx, "SELECT meal_id,creator_id,creationdate::text,recipetitle,yield,preptime,cooktime,totaltime,mychefnotes,equipment,instructions,allergens,ismeatorfish,recipeimg,cloudinary_public_id,allergenssafetynote FROM "+src.table("meals")+" ORDER BY meal_id")
	if err != nil {
		return sourceError(err, "meals")
	}
	defer rows.Close()
	for rows.Next() {
		var m store.Meal
		if err = rows.Scan(&m.ID, &m.CreatorID, &m.CreationDate, &m.RecipeTitle, &m.Yield, &m.PrepTime, &m.CookTime, &m.TotalTime, &m.Notes, &m.Equipment, &m.Instructions, &m.Allergens, &m.IsMeatOrFish, &m.ImageURL, &m.CloudinaryID, &m.AllergenNote); err != nil {
			return sourceError(err, "meals")
		}
		if m.ID <= 0 || m.CreatorID <= 0 || !validDate(&m.CreationDate) {
			return valueError("meals", "identity_or_date", m.ID)
		}
		original := map[string]*string{"yield": m.Yield, "instructions": m.Instructions, "allergens": m.Allergens, "preptime": m.PrepTime, "cooktime": m.CookTime, "totaltime": m.TotalTime}
		for column, target := range map[string]**string{"yield": &m.Yield, "instructions": &m.Instructions, "allergens": &m.Allergens} {
			converted, e := LegacyList(*target)
			if e != nil {
				report.Warnings = append(report.Warnings, Warning{Table: "meals", Column: column, ID: strconv.FormatInt(m.ID, 10)})
			}
			*target = converted
		}
		raw, _ := json.Marshal(original)
		m.LegacyRaw = store.Ptr(string(raw))
		m.ImageStatus = "unavailable"
		if m.ImageURL != nil {
			m.ImageStatus = "ready"
		}
		if err = tx.Create(&m).Error; err != nil {
			return targetError(err, "meals")
		}
		report.Meals++
	}
	return sourceErrorIfAny(rows.Err(), "meals")
}
func copyIngredients(ctx context.Context, src *snapshot, tx *gorm.DB, report *Report) error {
	rows, err := src.tx.Query(ctx, "SELECT pk_ingredients,recipe_id,quantity::text,unit,ingredient_name,date::text FROM "+src.table("ingredients")+" ORDER BY pk_ingredients")
	if err != nil {
		return sourceError(err, "ingredients")
	}
	defer rows.Close()
	for rows.Next() {
		var ingredient store.Ingredient
		if err = rows.Scan(&ingredient.ID, &ingredient.RecipeID, &ingredient.Quantity, &ingredient.Unit, &ingredient.Name, &ingredient.Date); err != nil {
			return sourceError(err, "ingredients")
		}
		if ingredient.ID <= 0 || ingredient.RecipeID <= 0 || !validDate(ingredient.Date) {
			return valueError("ingredients", "identity_or_date", ingredient.ID)
		}
		if ingredient.Quantity != nil && !domain.Decimal(*ingredient.Quantity).Valid() {
			return valueError("ingredients", "quantity", ingredient.ID)
		}
		if err = tx.Create(&ingredient).Error; err != nil {
			return targetError(err, "ingredients")
		}
		report.Ingredients++
	}
	return sourceErrorIfAny(rows.Err(), "ingredients")
}
func sourceErrorIfAny(err error, table string) error {
	if err == nil {
		return nil
	}
	return sourceError(err, table)
}
