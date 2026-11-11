package email

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/LowGok/my-chef/internal/email/mailqueue"
	"gorm.io/gorm"
)

const retryHorizon = 23 * time.Hour
const maxAttempts = 16
const sendTimeout = 10 * time.Second

type deliveryResult struct {
	id                    string
	temporary, suppressed bool
}

type sender interface {
	send(context.Context, string, []byte) deliveryResult
	bindingID() string
}

type Worker struct {
	onlyID         int64
	Queue          Queue
	sender         sender
	From, ReplyTo  string
	FingerprintKey []byte
	Random         func() float64
	mu             sync.Mutex
}

type sendPayload struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	ReplyTo string   `json:"reply_to,omitempty"`
	Subject string   `json:"subject"`
	Text    string   `json:"text"`
	HTML    string   `json:"html"`
}

func (w *Worker) fingerprint(body []byte) string {
	if len(w.FingerprintKey) < 32 {
		return ""
	}
	mac := hmac.New(sha256.New, w.FingerprintKey)
	binding, _ := json.Marshal(w.sender.bindingID())
	mac.Write(binding)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func (w *Worker) delay(attempt int) time.Duration {
	base := min(30*time.Second<<(attempt-1), 30*time.Minute)
	random := rand.Float64
	if w.Random != nil {
		random = w.Random
	}
	return min(time.Duration(float64(base)*(.8+.4*random())), 30*time.Minute)
}

func (w *Worker) Step(ctx context.Context) (bool, error) {
	w.Queue.Store.AccountWork.RLock()
	defer w.Queue.Store.AccountWork.RUnlock()
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.Queue.now()
	var row outbox
	var body []byte
	var proofExpiry int64
	var claimed, found bool
	err := w.Queue.background(ctx, func(tx *gorm.DB) error {
		pending := tx.Where("state IN ? AND next_at <= ?", []string{"queued", "sending"}, now.Unix())
		if w.onlyID != 0 {
			pending = pending.Where("id = ?", w.onlyID)
		}
		err := pending.Order("next_at, id").Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		protected := mailqueue.ProtectedTemplate(row.Template)
		fail := func(reason string) error {
			update := map[string]any{"state": "failed", "reason": reason, "updated_at": now.Unix()}
			if row.Template == mailqueue.AccountDeletedTemplate {
				update["recipient"] = ""
				update["data"] = "{}"
				update["fingerprint"] = ""
			}
			if protected {
				update["data"] = mailqueue.ErasedProtectedData(row)
			} else if row.Template == mailqueue.FeedbackTemplate {
				update["data"] = "{}"
			}
			return tx.Model(&row).Updates(update).Error
		}
		if row.Attempts >= maxAttempts || !now.Before(time.Unix(row.CreatedAt, 0).Add(retryHorizon)) {
			return fail("retry_exhausted")
		}
		var fields map[string]string
		if protected {
			fields, proofExpiry, err = mailqueue.OpenProtected(w.Queue.ProofKeys, row, now, w.Queue.AllowLoopbackProofLinks)
			switch {
			case errors.Is(err, mailqueue.ErrProofExpired):
				return fail("proof_expired")
			case err != nil:
				return fail("proof_unavailable")
			}
		} else if json.Unmarshal([]byte(row.Data), &fields) != nil {
			return fail("invalid_template")
		}
		msg, err := renderTemplate(row.Template, fields)
		if err != nil {
			return fail("invalid_template")
		}
		body, err = json.Marshal(sendPayload{From: w.From, To: []string{row.Recipient}, ReplyTo: w.ReplyTo, Subject: msg.Subject, Text: msg.Text, HTML: msg.HTML})
		if err != nil {
			return err
		}
		fp := w.fingerprint(body)
		if fp == "" || (row.Attempts > 0 && !hmac.Equal([]byte(row.Fingerprint), []byte(fp))) {
			return fail("configuration_changed")
		}
		row.Attempts++
		claimed = true
		next := now.Add(30 * time.Second).Unix()
		if proofExpiry != 0 {
			next = min(next, proofExpiry)
		}
		return tx.Model(&row).Updates(map[string]any{"state": "sending", "attempts": row.Attempts, "fingerprint": fp, "next_at": next, "updated_at": now.Unix()}).Error
	})
	if err != nil {
		return false, errStorage
	}
	if !claimed {
		return found, nil
	}
	sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
	result := w.sender.send(sendCtx, row.Key, body)
	cancel()
	now = w.Queue.now()
	update := map[string]any{"state": "failed", "reason": "provider_rejected", "updated_at": now.Unix()}
	switch {
	case result.suppressed:
		update["outcome"], update["reason"] = "suppressed", "provider_suppressed"
	case result.id != "":
		update["state"], update["reason"], update["provider_id"] = "accepted", "", result.id
	case result.temporary:
		next := now.Add(w.delay(row.Attempts))
		if row.Attempts < maxAttempts && next.Before(time.Unix(row.CreatedAt, 0).Add(retryHorizon)) {
			if proofExpiry == 0 || next.Unix() < proofExpiry {
				update["state"], update["reason"], update["next_at"] = "queued", "", next.Unix()
			} else {
				update["reason"] = "proof_expired"
			}
		} else {
			update["reason"] = "retry_exhausted"
		}
	}
	if update["state"] != "queued" {
		if row.Template == mailqueue.AccountDeletedTemplate {
			update["recipient"] = ""
			update["data"] = "{}"
			update["fingerprint"] = ""
		}
		if proofExpiry != 0 {
			update["data"] = mailqueue.ErasedProtectedData(row)
		} else if row.Template == mailqueue.FeedbackTemplate {
			update["data"] = "{}"
		}
	}
	err = w.Queue.background(ctx, func(tx *gorm.DB) error {
		saved := tx.Model(&row).Where("attempts = ? AND state = ?", row.Attempts, "sending").Updates(update)
		if saved.Error != nil || saved.RowsAffected == 0 || update["provider_id"] == nil {
			return saved.Error
		}
		return reconcile(tx, result.id)
	})
	if err != nil {
		return true, errStorage
	}
	return true, nil
}
