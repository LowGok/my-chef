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

//go:embed 019_analytics.sql
var Analytics string

//go:embed 020_analytics_generation.sql
var AnalyticsGeneration string

//go:embed 021_analytics_installation.sql
var AnalyticsInstallation string

//go:embed 022_analytics_signup_source.sql
var AnalyticsSignupSource string

//go:embed 023_analytics_gaps.sql
var AnalyticsGaps string

//go:embed 024_newsletter.sql
var Newsletter string

//go:embed 025_weekly_reminders.sql
var WeeklyReminders string

//go:embed 026_reminder_dispatch_expiry.sql
var ReminderDispatchExpiry string

//go:embed 027_newsletter_identity_minimisation.sql
var NewsletterIdentityMinimisation string

//go:embed 028_login_username_keys.sql
var LoginUsernameKeys string
