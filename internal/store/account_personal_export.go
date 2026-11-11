package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image/jpeg"
	"io"
	"time"

	"github.com/LowGok/my-chef/internal/imports"
	"gorm.io/gorm"
)

type AccountExportOptions struct {
	ImportVault *imports.DraftVault
	now         time.Time
	// ReadPhoto returns owned bounded JPEG bytes and respects cancellation. It
	// must not query this Store: the export already holds a consistent snapshot.
	// The exporter clears the returned buffer after each bounded pass.
	ReadPhoto func(context.Context, PersonalPhotoAsset) ([]byte, error)
}

func eachAccountPhoto(ctx context.Context, tx *gorm.DB, user int64, visit func(PersonalPhotoAsset) error) error {
	rows, err := tx.Raw(`SELECT id,user_id,incarnation,target,binding,delivery_type,digest,width,height,size,created_ns FROM personal_photo_assets WHERE user_id=? AND (`+personalPhotoReferences+`) ORDER BY id`, user).Rows()
	if err != nil {
		return ErrAccountExport
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var asset PersonalPhotoAsset
		if err := rows.Scan(&asset.ID, &asset.UserID, &asset.Incarnation, &asset.Target, &asset.Binding, &asset.DeliveryType, &asset.Digest, &asset.Width, &asset.Height, &asset.Size, &asset.CreatedNS); err != nil {
			return ErrAccountExport
		}
		if err := visit(asset); err != nil {
			return err
		}
	}
	if rows.Err() != nil {
		return ErrAccountExport
	}
	return nil
}

func readAccountPhoto(ctx context.Context, options AccountExportOptions, asset PersonalPhotoAsset) ([]byte, error) {
	if options.ReadPhoto == nil || asset.DeliveryType != "authenticated" || !validPersonalTarget(asset.Target) || !validPersonalDigest(asset.Digest) {
		return nil, ErrAccountExport
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := options.ReadPhoto(ctx, asset)
	if err != nil || len(data) < 1 || len(data) > 2<<20 || len(data) != asset.Size {
		clear(data)
		return nil, ErrAccountExport
	}
	sum := sha256.Sum256(data)
	config, decodeErr := jpeg.DecodeConfig(bytes.NewReader(data))
	if hex.EncodeToString(sum[:]) != asset.Digest || decodeErr != nil || config.Width != asset.Width || config.Height != asset.Height || config.Width < 1 || config.Width > 2048 || config.Height < 1 || config.Height > 2048 {
		clear(data)
		return nil, ErrAccountExport
	}
	if err := ctx.Err(); err != nil {
		clear(data)
		return nil, err
	}
	return data, nil
}

func writeAccountManifest(ctx context.Context, tx *gorm.DB, user int64, mapper exportMapper, w io.Writer, now time.Time) error {
	header, err := json.Marshal(map[string]any{"format": "mychef-account-export", "version": 2, "exportedAt": now.UTC().Format(time.RFC3339), "relationships": "Random export-local identifiers; stable only within this archive."})
	if err != nil {
		return err
	}
	if _, err := w.Write(header[:len(header)-1]); err != nil {
		return err
	}
	if _, err := io.WriteString(w, `,"files":[`); err != nil {
		return err
	}
	first := true
	name := func(value string) error {
		if !first {
			if _, err := io.WriteString(w, ","); err != nil {
				return err
			}
		}
		first = false
		return json.NewEncoder(w).Encode(value)
	}
	if err := name("README.txt"); err != nil {
		return err
	}
	for _, part := range accountExportParts {
		if err := name(part.Name); err != nil {
			return err
		}
	}
	if err := eachAccountPhoto(ctx, tx, user, func(asset PersonalPhotoAsset) error { return name(mapper.id("photo-file", asset.ID)) }); err != nil {
		return err
	}
	_, err = io.WriteString(w, "]}\n")
	return err
}
