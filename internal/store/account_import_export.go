package store

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/LowGok/my-chef/internal/imports"
	"gorm.io/gorm"
)

func writeRetainedImportDrafts(ctx context.Context, tx *gorm.DB, user int64, mapper exportMapper, w io.Writer, options AccountExportOptions) error {
	rows, err := tx.Raw(`SELECT o.id,o.user_id,o.incarnation,o.source_kind,o.stage,o.code,o.cipher,o.created_ns,o.updated_ns,o.expires_ns FROM recipe_import_operations o JOIN account_incarnations a ON a.user_id=o.user_id AND a.incarnation=o.incarnation WHERE o.user_id=? AND o.cipher IS NOT NULL AND o.expires_ns>? ORDER BY o.created_ns,o.id`, user, options.now.UnixNano()).Rows()
	if err != nil {
		return ErrAccountExport
	}
	defer func() { _ = rows.Close() }()
	if _, err := io.WriteString(w, "[\n"); err != nil {
		return err
	}
	first := true
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var row recipeImportOperation
		if err := rows.Scan(&row.ID, &row.UserID, &row.Incarnation, &row.SourceKind, &row.Stage, &row.Code, &row.Cipher, &row.CreatedNS, &row.UpdatedNS, &row.ExpiresNS); err != nil {
			return ErrAccountExport
		}
		view, err := importView(row, options.ImportVault)
		clear(row.Cipher)
		if err != nil {
			return ErrAccountExport
		}
		if !first {
			if _, err := io.WriteString(w, ",\n"); err != nil {
				return err
			}
		}
		first = false
		value := struct {
			ID        string               `json:"id"`
			Stage     string               `json:"stage"`
			ExpiresAt string               `json:"expiresAt"`
			Content   imports.PrivateDraft `json:"content"`
		}{mapper.id("import", row.ID), row.Stage, time.Unix(0, row.ExpiresNS).UTC().Format(time.RFC3339Nano), view.Private}
		if err := json.NewEncoder(w).Encode(value); err != nil {
			return err
		}
	}
	if rows.Err() != nil {
		return ErrAccountExport
	}
	_, err = io.WriteString(w, "\n]\n")
	return err
}
