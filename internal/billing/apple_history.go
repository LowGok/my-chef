package billing

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	appleHistoryBatch    = 8
	appleHistoryPageSize = 20
	appleHistorySpan     = 24 * time.Hour
	appleHistoryOverlap  = 30 * time.Minute
	appleHistoryLag      = time.Minute
)

var errAppleHistoryCursor = errors.New("notification history cursor expired")

type appleHistoryCursor struct {
	Through, Start, End int64
	Page                string
}

// SyncNotificationHistory recovers notifications the callback did not record.
// Only routing fields enter the protected queue; the worker must still fetch
// authoritative current transaction facts. The caller schedules this method.
func (p *AppleProvider) SyncNotificationHistory(ctx context.Context, j *Journal) error {
	if j == nil || p.cfg.Environment == Production && p.cfg.AppAppleID <= 0 {
		return ErrStoreUnavailable
	}
	at := p.now()
	cursor, err := p.historyCursor(ctx, j, at)
	if err != nil || cursor.End == 0 {
		return err
	}
	for range appleHistoryBatch {
		notices, next, err := p.readHistoryPage(ctx, cursor, at)
		if errors.Is(err, errAppleHistoryCursor) && cursor.Page != "" {
			// Retry the very same window on the next run. Duplicates remain
			// harmless. Never mark a window complete on a provider error.
			_, reset := j.db.ExecContext(ctx, `UPDATE billing_apple_history SET page=''
 WHERE environment=? AND app=? AND start_ms=? AND end_ms=? AND page=? AND `+currentGenerationSQL+`=?`,
				p.cfg.Environment, p.cfg.BundleID, cursor.Start, cursor.End, cursor.Page, j.generation)
			if reset != nil {
				return reset
			}
			return ErrStoreUnavailable
		}
		if err != nil {
			return err
		}
		if err := p.persistHistoryPage(ctx, j, cursor, next, notices, at); err != nil {
			return err
		}
		if next == "" {
			return nil
		}
		cursor.Page = next
	}
	return nil
}

func (p *AppleProvider) historyCursor(ctx context.Context, j *Journal, at time.Time) (appleHistoryCursor, error) {
	var c appleHistoryCursor
	end := at.Add(-appleHistoryLag).UnixMilli()
	err := transaction(ctx, j.db, func(tx *sql.Tx) error {
		if err := j.requireRuntime(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO billing_apple_history(environment,app,through_ms) VALUES(?,?,?) ON CONFLICT DO NOTHING`,
			p.cfg.Environment, p.cfg.BundleID, end-appleHistorySpan.Milliseconds()); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT through_ms,start_ms,end_ms,page FROM billing_apple_history WHERE environment=? AND app=?`,
			p.cfg.Environment, p.cfg.BundleID).Scan(&c.Through, &c.Start, &c.End, &c.Page); err != nil {
			return err
		}
		if c.End != 0 || c.Through >= end {
			return nil
		}
		c.Start = c.Through - appleHistoryOverlap.Milliseconds()
		c.End = min(end, c.Through+appleHistorySpan.Milliseconds())
		_, err := tx.ExecContext(ctx, `UPDATE billing_apple_history SET start_ms=?,end_ms=? WHERE environment=? AND app=?`,
			c.Start, c.End, p.cfg.Environment, p.cfg.BundleID)
		return err
	})
	return c, err
}

func (p *AppleProvider) persistHistoryPage(ctx context.Context, j *Journal, c appleHistoryCursor, next string, notices []AppleNotice, at time.Time) error {
	through, start, end := c.Through, c.Start, c.End
	if next == "" {
		through, start, end = c.End, 0, 0
	}
	return transaction(ctx, j.db, func(tx *sql.Tx) error {
		if err := j.requireRuntime(ctx, tx); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE billing_apple_history SET through_ms=?,start_ms=?,end_ms=?,page=?
 WHERE environment=? AND app=? AND through_ms=? AND start_ms=? AND end_ms=? AND page=?`,
			through, start, end, next, p.cfg.Environment, p.cfg.BundleID, c.Through, c.Start, c.End, c.Page)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrUnresolved
		}
		for _, notice := range notices {
			if err := enqueueAppleNotice(ctx, tx, notice, at); err != nil && !errors.Is(err, errUnownedAppleNotice) {
				return err
			}
		}
		return nil
	})
}

func (p *AppleProvider) readHistoryPage(ctx context.Context, c appleHistoryCursor, at time.Time) ([]AppleNotice, string, error) {
	endpoint := p.origin + "/inApps/v1/notifications/history"
	if c.Page != "" {
		endpoint += "?" + url.Values{"paginationToken": []string{c.Page}}.Encode()
	}
	body, err := json.Marshal(struct {
		Start int64 `json:"startDate"`
		End   int64 `json:"endDate"`
	}{c.Start, c.End})
	if err != nil {
		return nil, "", ErrStoreUnavailable
	}
	token, err := p.bearer(p.now())
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, "", ErrStoreUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := p.http.Do(req)
	if err != nil {
		return nil, "", ErrStoreUnavailable
	}
	defer func() { _ = res.Body.Close() }()
	const limit = 1 << 20
	raw, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil || len(raw) > limit {
		return nil, "", ErrStoreUnavailable
	}
	if res.StatusCode == http.StatusBadRequest {
		var failure struct {
			Code int64 `json:"errorCode"`
		}
		if json.Unmarshal(raw, &failure) == nil && (failure.Code == 4000014 || failure.Code == 4000017) {
			return nil, "", errAppleHistoryCursor
		}
	}
	var page struct {
		HasMore *bool  `json:"hasMore"`
		Next    string `json:"paginationToken"`
		Records []struct {
			Payload string `json:"signedPayload"`
		} `json:"notificationHistory"`
	}
	if res.StatusCode != http.StatusOK || json.Unmarshal(raw, &page) != nil || page.HasMore == nil || len(page.Records) > appleHistoryPageSize {
		return nil, "", ErrStoreUnavailable
	}
	if !*page.HasMore {
		page.Next = ""
	} else if page.Next == "" || page.Next == c.Page || len(page.Next) > 4096 {
		return nil, "", ErrStoreUnavailable
	}
	var notices []AppleNotice
	for _, record := range page.Records {
		n, err := p.verifiedNotice(record.Payload, at)
		if err != nil {
			return nil, "", err
		}
		if n != nil {
			notices = append(notices, *n)
		}
	}
	return notices, page.Next, nil
}

func (p *AppleProvider) verifiedNotice(signed string, at time.Time) (*AppleNotice, error) {
	data, err := p.verifySignedPayload(signed, at)
	var notice struct {
		ID         string `json:"notificationUUID"`
		Version    string `json:"version"`
		SignedDate int64  `json:"signedDate"`
		Data       *struct {
			AppAppleID  int64  `json:"appAppleId"`
			BundleID    string `json:"bundleId"`
			Environment string `json:"environment"`
			Transaction string `json:"signedTransactionInfo"`
		} `json:"data"`
	}
	if err != nil || json.Unmarshal(data, &notice) != nil || notice.Version != "2.0" || !accountUUID.MatchString(notice.ID) ||
		notice.SignedDate <= 0 || notice.SignedDate > at.Add(time.Minute).UnixMilli() {
		return nil, ErrPurchaseBinding
	}
	// Summary and test notices can have no transaction payload.
	if notice.Data == nil {
		return nil, nil
	}
	env := "Production"
	if p.cfg.Environment == Sandbox {
		env = "Sandbox"
	}
	if notice.Data.BundleID != p.cfg.BundleID || notice.Data.Environment != env ||
		(p.cfg.Environment == Production && notice.Data.AppAppleID != p.cfg.AppAppleID) {
		return nil, ErrPurchaseBinding
	}
	if notice.Data.Transaction == "" {
		return nil, nil
	}
	tx, err := p.verifyTransaction(notice.Data.Transaction, at)
	if err != nil || tx.BundleID != p.cfg.BundleID || tx.Environment != env || tx.SignedDate <= 0 ||
		tx.SignedDate > notice.SignedDate+time.Minute.Milliseconds() {
		return nil, ErrPurchaseBinding
	}
	_, known := p.cfg.Products[tx.Product]
	if !known || tx.Binding == "" {
		return nil, nil
	}
	if tx.Type != "Auto-Renewable Subscription" || tx.GroupID != p.cfg.GroupID || !accountUUID.MatchString(tx.Binding) ||
		!appleTransactionID.MatchString(tx.Transaction) {
		return nil, ErrPurchaseBinding
	}
	return &AppleNotice{Environment: p.cfg.Environment, ID: strings.ToLower(notice.ID), Binding: strings.ToLower(tx.Binding), Transaction: tx.Transaction}, nil
}
