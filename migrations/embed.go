package migrations

import _ "embed"

//go:embed 001_initial.sql
var Initial string

//go:embed 002_shopping.sql
var Shopping string

//go:embed 003_favorites.sql
var Favorites string

//go:embed 004_email.sql
var Email string

//go:embed 005_email_identity.sql
var EmailIdentity string

//go:embed 006_email_limits.sql
var EmailLimits string

//go:embed 007_signup.sql
var SignUp string

//go:embed 008_email_verification.sql
var EmailVerification string

//go:embed 009_email_change.sql
var EmailChange string

//go:embed 010_password_recovery.sql
var PasswordRecovery string

//go:embed 011_feedback.sql
var Feedback string

//go:embed 012_account_lifecycle.sql
var AccountLifecycle string

//go:embed 013_personal_recipes.sql
var PersonalRecipes string

//go:embed 014_personal_photos.sql
var PersonalPhotos string

//go:embed 015_personal_planning.sql
var PersonalPlanning string

//go:embed 016_recipe_imports.sql
var RecipeImports string

//go:embed 017_native_sessions.sql
var NativeSessions string

//go:embed 018_entitlement_grants.sql
var EntitlementGrants string
