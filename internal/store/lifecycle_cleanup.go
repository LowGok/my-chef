package store

import (
	"context"
	"errors"
	"math"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"
)

type AssetDeletionResult struct{ Deleted, Missing, Permanent bool }
type AccountAssetCleaner interface {
	AssetBinding() string
	DeleteImage(context.Context, string) (AssetDeletionResult, error)
}
type PersonalAssetCleaner interface {
	DeletePersonalPhoto(context.Context, string) (AssetDeletionResult, error)
}
type AccountCleanupStatus struct {
	Reference string `json:"reference"`
	Kind      string `json:"kind"`
	State     string `json:"state"`
	Code      string `json:"code"`
	Attempts  int    `json:"attempts"`
}

var cloudinaryBindingPattern = regexp.MustCompile(`^cloudinary/[A-Za-z0-9_-]+$`)

func assetURLBinding(value *string) string {
	if value == nil {
		return "unknown"
	}
	parsed, err := url.Parse(*value)
	if err != nil || parsed.User != nil || parsed.Hostname() != "res.cloudinary.com" {
		return "unknown"
	}
	parts := strings.Split(strings.TrimPrefix(parsed.Path, "/"), "/")
	if len(parts) < 4 || parts[1] != "image" || parts[2] != "upload" {
		return "unknown"
	}
	binding := "cloudinary/" + parts[0]
	if !cloudinaryBindingPattern.MatchString(binding) {
		return "unknown"
	}
	return binding
}
func assetURLMatches(raw, target, binding string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return true
	}
	if parsed.Host == "" {
		return false
	}
	actual := assetURLBinding(&raw)
	if actual != "unknown" && actual != binding {
		return false
	}
	suffix := "/" + target
	return strings.HasSuffix(parsed.Path, suffix) || strings.HasSuffix(strings.TrimSuffix(parsed.Path, path.Ext(parsed.Path)), suffix) || actual == "unknown"
}
func (j *LifecycleJournal) assetReferenced(ctx context.Context, work LifecycleWork) (bool, error) {
	if work.DeliveryType == "authenticated" {
		var count int
		if err := j.app.SQL.QueryRowContext(ctx, `SELECT COUNT(*) FROM personal_photo_assets WHERE target=? AND binding=? AND delivery_type=? AND (`+personalPhotoReferences+`)`, work.Target, work.Provider, work.DeliveryType).Scan(&count); err != nil {
			return true, ErrLifecycleJournal
		}
		return count != 0, nil
	}
	var count int
	if err := j.app.SQL.QueryRowContext(ctx, "SELECT COUNT(*) FROM meals WHERE cloudinary_public_id=?", work.Target).Scan(&count); err != nil {
		return true, ErrLifecycleJournal
	}
	if count != 0 {
		return true, nil
	}
	for _, query := range []string{
		`SELECT 1,image_url FROM personal_recipe_versions WHERE image_url IS NOT NULL`,
		`SELECT 1,recipeimg FROM meals WHERE recipeimg IS NOT NULL`,
		`SELECT json_valid(body),CASE WHEN json_valid(body) THEN json_extract(body,'$.imageUrl') ELSE NULL END FROM favorites`,
		`SELECT json_valid(body),CASE WHEN json_valid(body) THEN json_extract(body,'$.imageUrl') ELSE NULL END FROM favorite_queue`,
		`SELECT json_valid(body),CASE WHEN json_valid(body) THEN json_extract(body,'$.imageUrl') ELSE NULL END FROM favorite_swaps`,
	} {
		rows, err := j.app.SQL.QueryContext(ctx, query)
		if err != nil {
			return true, ErrLifecycleJournal
		}
		referenced := false
		for rows.Next() {
			var valid int
			var value *string
			if err := rows.Scan(&valid, &value); err != nil {
				_ = rows.Close()
				return true, ErrLifecycleJournal
			}
			if valid != 1 || (value != nil && *value != "" && assetURLMatches(*value, work.Target, work.Provider)) {
				referenced = true
				break
			}
		}
		rowErr := rows.Err()
		_ = rows.Close()
		if rowErr != nil {
			return true, ErrLifecycleJournal
		}
		if referenced {
			return true, nil
		}
	}
	return false, nil
}
func (j *LifecycleJournal) cleanupUpdate(ctx context.Context, work LifecycleWork, state, code string, next int64, attempt bool, now time.Time) error {
	values := map[string]any{"state": state, "code": code, "next_ns": next}
	if attempt {
		values["attempts"] = work.Attempts + 1
		if work.CleanupNS == 0 {
			values["cleanup_ns"] = now.UnixNano()
		}
	}
	// A late confirmed upload is fresh information and must not be overwritten
	// by an older worker result that saw an unconfirmed request.
	result := j.store.DB.WithContext(ctx).Model(&LifecycleWork{}).Where("id=? AND confirmed=? AND finished=?", work.ID, work.Confirmed, work.Finished).Updates(values)
	if result.Error != nil {
		return ErrLifecycleJournal
	}
	return nil
}
func cleanupDelay(attempt int) time.Duration {
	return min(30*time.Second<<min(attempt, 6), 30*time.Minute)
}

// CleanupStep touches at most one due item. Account deletion is already
// committed; provider latency never holds its database transaction or fence.
func (j *LifecycleJournal) CleanupStep(ctx context.Context, cleaner AccountAssetCleaner, now time.Time) (bool, error) {
	if err := j.CheckAvailable(ctx); err != nil {
		return false, err
	}
	j.cleanupMu.Lock()
	defer j.cleanupMu.Unlock()
	var work LifecycleWork
	err := j.store.DB.WithContext(ctx).Table("lifecycle_work AS w").Select("w.*").Joins("LEFT JOIN lifecycle_deletions d ON d.incarnation=w.incarnation AND d.state='committed'").Where("w.next_ns<=? AND (d.operation IS NOT NULL OR w.purpose='personal')", now.UnixNano()).Order("w.next_ns,w.id").Take(&work).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, ErrLifecycleJournal
	}
	review := func(code string) (bool, error) {
		return true, j.cleanupUpdate(ctx, work, "review", code, math.MaxInt64, false, now)
	}
	if work.Kind == "trace" {
		if !validTraceTarget(work.Root, work.Target) {
			return review("trace_target_unavailable")
		}
		if err := os.RemoveAll(work.Target); err == nil {
			return true, j.CompleteWork(ctx, work.ID)
		}
		// Permission failures remain retryable until the bounded attempt policy.
	} else {
		if work.Purpose == "personal" {
			var active int64
			if err := j.app.SQL.QueryRowContext(ctx, "SELECT COALESCE(MAX(lease_ns),0) FROM personal_photo_attempts WHERE work_id=? AND state IN ('reserved','uploading') AND lease_ns>?", work.ID, now.UnixNano()).Scan(&active); err != nil {
				return true, ErrLifecycleJournal
			}
			if active != 0 {
				return true, j.cleanupUpdate(ctx, work, work.State, work.Code, active, false, now)
			}
		}
		if work.StartedNS == 0 && !work.Confirmed {
			return true, j.CompleteWork(ctx, work.ID)
		}
		referenced, err := j.assetReferenced(ctx, work)
		if err != nil {
			return true, err
		}
		if referenced {
			return true, j.cleanupUpdate(ctx, work, "review", "asset_retained", now.Add(24*time.Hour).UnixNano(), false, now)
		}
		if cleaner == nil {
			return review("provider_unavailable")
		}
		if work.Provider != cleaner.AssetBinding() {
			return review("provider_changed")
		}
		if work.Attempts >= 16 || (work.CleanupNS != 0 && !now.Before(time.Unix(0, work.CleanupNS).Add(23*time.Hour))) {
			return review("cleanup_exhausted")
		}
		call, cancel := context.WithTimeout(ctx, 10*time.Second)
		var result AssetDeletionResult
		if work.DeliveryType == "authenticated" {
			private, ok := cleaner.(PersonalAssetCleaner)
			if !ok {
				cancel()
				return review("private_provider_unavailable")
			}
			result, err = private.DeletePersonalPhoto(call, work.Target)
		} else {
			result, err = cleaner.DeleteImage(call, work.Target)
		}
		cancel()
		if err == nil && (result.Deleted || result.Missing) {
			if work.Confirmed && work.Finished {
				return true, j.CompleteWork(ctx, work.ID)
			}
			return review("upload_outcome_unconfirmed")
		}
		if result.Permanent {
			return true, j.cleanupUpdate(ctx, work, "review", "provider_rejected", math.MaxInt64, true, now)
		}
	}
	if work.Attempts+1 >= 16 || (work.CleanupNS != 0 && !now.Add(cleanupDelay(work.Attempts)).Before(time.Unix(0, work.CleanupNS).Add(23*time.Hour))) {
		return true, j.cleanupUpdate(ctx, work, "review", "cleanup_exhausted", math.MaxInt64, true, now)
	}
	return true, j.cleanupUpdate(ctx, work, "deleting", "cleanup_retry", now.Add(cleanupDelay(work.Attempts)).UnixNano(), true, now)
}
func (j *LifecycleJournal) CleanupStatus(ctx context.Context, limit int, after string) ([]AccountCleanupStatus, error) {
	if limit < 1 || limit > 100 || (after != "" && !opaqueLifecycleID(after)) {
		return nil, ErrLifecycleConflict
	}
	var result []AccountCleanupStatus
	err := j.store.DB.WithContext(ctx).Table("lifecycle_work AS w").Joins("LEFT JOIN lifecycle_deletions d ON d.incarnation=w.incarnation AND d.state='committed'").Select("w.id AS reference,w.kind,w.state,w.code,w.attempts").Where("w.id>? AND (d.operation IS NOT NULL OR w.purpose='personal')", after).Order("w.id").Limit(limit).Scan(&result).Error
	if err != nil {
		return nil, ErrLifecycleJournal
	}
	return result, nil
}
