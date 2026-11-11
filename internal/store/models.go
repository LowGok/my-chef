package store

import "github.com/LowGok/my-chef/internal/domain"

type User struct {
	ID               int64   `gorm:"column:user_id;primaryKey;autoIncrement"`
	FirstName        string  `gorm:"column:firstname"`
	LastName         string  `gorm:"column:lastname"`
	Sex              *string `gorm:"column:sex"`
	Birthdate        *string `gorm:"column:birthdate"`
	Email            string  `gorm:"column:email"`
	EmailKey         string  `gorm:"column:email_key"`
	EmailVerifiedAt  *int64  `gorm:"column:email_verified_at"`
	EmailChangedAt   *int64  `gorm:"column:email_changed_at"`
	Password         string  `gorm:"column:password"`
	Username         string  `gorm:"column:username"`
	Allergens        *string `gorm:"column:allergens"`
	Diet             *string `gorm:"column:diet"`
	Dislikes         *string `gorm:"column:dislikes"`
	LastMeal         *string `gorm:"column:lastmeal"`
	CookingEfforts   *string `gorm:"column:cooking_efforts"`
	CookingTechnique *string `gorm:"column:cooking_technique"`
	HasPref          *bool   `gorm:"column:haspref"`
	PreferencesJSON  *string `gorm:"column:preferences_json"`
}
type MenuRecord struct {
	ID          int64 `gorm:"primaryKey;autoIncrement"`
	UserID      int64
	CreatedUnix int64
}

func (MenuRecord) TableName() string { return "menus" }

type Generation struct {
	ID           int64 `gorm:"primaryKey;autoIncrement"`
	UserID       int64
	Key          string `gorm:"column:request_key"`
	RequestHash  string
	RequestJSON  string
	Status       string
	Stage        string
	MenuID       *int64
	ErrorCode    *string
	ErrorMessage *string
	CreatedUnix  int64
}
type Meal struct {
	PersonalSource *string `gorm:"column:personal_source"`
	ID             int64   `gorm:"column:meal_id;primaryKey;autoIncrement"`
	CreatorID      int64   `gorm:"column:creator_id"`
	MenuID         *int64
	CreationDate   string  `gorm:"column:creationdate"`
	RecipeTitle    string  `gorm:"column:recipetitle"`
	Yield          *string `gorm:"column:yield"`
	PrepTime       *string `gorm:"column:preptime"`
	CookTime       *string `gorm:"column:cooktime"`
	Notes          *string `gorm:"column:mychefnotes"`
	Equipment      *string `gorm:"column:equipment"`
	Instructions   *string `gorm:"column:instructions"`
	Allergens      *string `gorm:"column:allergens"`
	AllergenNote   *string `gorm:"column:allergenssafetynote"`
	IsMeatOrFish   *bool   `gorm:"column:ismeatorfish"`
	TotalTime      *string `gorm:"column:totaltime"`
	ImageURL       *string `gorm:"column:recipeimg"`
	CloudinaryID   *string `gorm:"column:cloudinary_public_id"`
	TipsJSON       *string
	RecipeJSON     *string
	ImageStatus    string
	LegacyRaw      *string
	Ingredients    []Ingredient `gorm:"foreignKey:RecipeID;references:ID"`
}
type Ingredient struct {
	ID       int64 `gorm:"column:pk_ingredients;primaryKey;autoIncrement"`
	RecipeID int64 `gorm:"column:recipe_id"`
	Quantity *string
	Unit     *string
	Name     string `gorm:"column:ingredient_name"`
	Date     *string
}
type Session struct {
	Hash        string `gorm:"primaryKey"`
	UserID      *int64
	ExpiresUnix int64
}

type Profile struct {
	AccountInstance string             `json:"accountInstance,omitempty"`
	ID              string             `json:"id"`
	Email           string             `json:"email"`
	Username        string             `json:"username"`
	FirstName       string             `json:"firstName"`
	LastName        string             `json:"lastName"`
	Birthdate       *string            `json:"birthdate"`
	Sex             *string            `json:"sex"`
	Preferences     domain.Preferences `json:"preferences"`
}
