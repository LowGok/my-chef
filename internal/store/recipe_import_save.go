package store

import (
	"errors"
	"strings"
	"time"

	"github.com/LowGok/my-chef/internal/domain"
	"github.com/LowGok/my-chef/internal/imports"
	"gorm.io/gorm"
)

// Vault is supplied by the application, never decoded from a client request or
// included in the personal-save receipt hash.
type PersonalImportSave struct {
	OperationID  string                     `json:"operationId"`
	Confirmation imports.ReviewConfirmation `json:"confirmation"`
	Vault        *imports.DraftVault        `json:"-"`
}
type personalImportSource struct {
	VersionID, SourceKind, SourceURL, SourceTitle string
	ImportedNS                                    int64
}

func (personalImportSource) TableName() string { return "personal_recipe_import_sources" }

func domainImportSource(source imports.ImportSource) *domain.RecipeImportSource {
	return &domain.RecipeImportSource{Kind: source.Kind, URL: source.URL, Title: source.Title, ImportedAt: source.ImportedAt.UTC().Format(time.RFC3339Nano)}
}
func checkedImportSource(source *domain.RecipeImportSource) (imports.ImportSource, error) {
	if source == nil {
		return imports.ImportSource{}, ErrRecipeImportStorage
	}
	at, err := time.Parse(time.RFC3339Nano, source.ImportedAt)
	if err != nil {
		return imports.ImportSource{}, ErrRecipeImportStorage
	}
	value, err := imports.CanonicalImportSource(imports.ImportSource{Kind: source.Kind, URL: source.URL, Title: source.Title, ImportedAt: at})
	if err != nil || *domainImportSource(value) != *source {
		return imports.ImportSource{}, ErrRecipeImportStorage
	}
	return value, nil
}
func loadPersonalImportSource(tx *gorm.DB, version string) (*domain.RecipeImportSource, error) {
	var row personalImportSource
	if err := tx.Where("version_id=?", version).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, ErrRecipeImportStorage
	}
	source := domainImportSource(imports.ImportSource{Kind: row.SourceKind, URL: row.SourceURL, Title: row.SourceTitle, ImportedAt: time.Unix(0, row.ImportedNS).UTC()})
	if _, err := checkedImportSource(source); err != nil {
		return nil, err
	}
	return source, nil
}
func prepareReviewedImport(tx *gorm.DB, actor PersonalActor, incarnation string, input PersonalSave, now time.Time) (*recipeImportOperation, *domain.RecipeImportSource, []string, error) {
	if input.Import == nil {
		return nil, nil, nil, nil
	}
	proof := input.Import
	if proof.Vault == nil {
		return nil, nil, nil, ErrRecipeImportStorage
	}
	row, err := loadImport(tx, actor, incarnation, proof.OperationID, now)
	if err != nil {
		return nil, nil, nil, err
	}
	if row.Stage != "ready" {
		return nil, nil, nil, ErrRecipeImportConflict
	}
	op, err := importView(row, proof.Vault)
	if err != nil {
		return nil, nil, nil, err
	}
	if op.Draft == nil {
		return nil, nil, nil, ErrRecipeImportConflict
	}
	if _, err := op.Draft.Confirm(input.Content, proof.Confirmation); err != nil {
		return nil, nil, nil, err
	}
	return &row, domainImportSource(op.Private.Source), op.Draft.SourceYield, nil
}
func retainImportWarning(content domain.PersonalRecipeContent) (domain.PersonalRecipeContent, error) {
	if !strings.HasPrefix(content.AllergenNote, imports.ImportedAllergenWarning) {
		content.AllergenNote = strings.TrimSpace(imports.ImportedAllergenWarning + " " + content.AllergenNote)
	}
	return content.Normalize()
}
func attachPersonalImport(tx *gorm.DB, source *domain.RecipeImportSource, operation *recipeImportOperation, recipeID, versionID string, now time.Time, options []PersonalWriteOptions) error {
	if source == nil {
		return nil
	}
	checked, err := checkedImportSource(source)
	if err != nil {
		return err
	}
	row := personalImportSource{VersionID: versionID, SourceKind: checked.Kind, SourceURL: checked.URL, SourceTitle: checked.Title, ImportedNS: checked.ImportedAt.UnixNano()}
	if err := tx.Create(&row).Error; err != nil {
		return ErrRecipeImportStorage
	}
	if err := personalFault(options, "import-source"); err != nil {
		return err
	}
	if operation != nil {
		result := tx.Model(&recipeImportOperation{}).Where("id=? AND user_id=? AND incarnation=? AND stage='ready' AND expires_ns>?", operation.ID, operation.UserID, operation.Incarnation, now.UnixNano()).Updates(map[string]any{"stage": "saved", "code": "", "cipher": nil, "lease_hash": "", "recipe_id": recipeID, "version_id": versionID, "updated_ns": now.UnixNano()})
		if result.Error != nil || result.RowsAffected != 1 {
			return ErrRecipeImportConflict
		}
	}
	return personalFault(options, "import-saved")
}
