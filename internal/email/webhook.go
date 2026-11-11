package email

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type emailEvent struct {
	ID, ProviderID, Kind   string
	OccurredAt, ReceivedAt int64
}

func (emailEvent) TableName() string { return "email_events" }

func precedence(kind string) int {
	switch kind {
	case "failed":
		return 1
	case "delivered":
		return 2
	case "suppressed":
		return 3
	case "bounced":
		return 4
	case "complained":
		return 5
	default:
		return 0
	}
}

func reconcile(tx *gorm.DB, id string) error {
	var kinds []string
	if err := tx.Model(&emailEvent{}).Where("provider_id = ?", id).Distinct().Pluck("kind", &kinds).Error; err != nil {
		return err
	}
	outcome := ""
	for _, kind := range kinds {
		if precedence(kind) > precedence(outcome) {
			outcome = kind
		}
	}
	if outcome == "" {
		return nil
	}
	return tx.Model(&outbox{}).Where("provider_id = ?", id).Update("outcome", outcome).Error
}

func (q Queue) Webhook(key []byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := q.now()
		id, stamp := r.Header.Get("svix-id"), r.Header.Get("svix-timestamp")
		unix, err := strconv.ParseInt(stamp, 10, 64)
		if err != nil || unix < now.Unix()-300 || unix > now.Unix()+300 || len(id) == 0 || len(id) > 128 || len(key) < 16 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(id + "." + stamp + "."))
		mac.Write(body)
		want := mac.Sum(nil)
		valid := false
		for _, sig := range strings.Fields(r.Header.Get("svix-signature")) {
			if encoded, ok := strings.CutPrefix(sig, "v1,"); ok {
				got, err := base64.StdEncoding.DecodeString(encoded)
				valid = valid || (err == nil && hmac.Equal(want, got))
			}
		}
		if !valid {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var payload struct {
			Type      string
			CreatedAt time.Time `json:"created_at"`
			Data      struct {
				ID string `json:"email_id"`
			}
		}
		if json.Unmarshal(body, &payload) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		kind, ok := strings.CutPrefix(payload.Type, "email.")
		if !ok || (precedence(kind) == 0 && kind != "sent" && kind != "delivery_delayed") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if payload.Data.ID == "" || len(payload.Data.ID) > 128 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		occurred := payload.CreatedAt
		if occurred.IsZero() {
			occurred = time.Unix(unix, 0)
		}
		err = q.Store.DB.WithContext(r.Context()).Transaction(func(tx *gorm.DB) error {
			event := emailEvent{ID: id, ProviderID: payload.Data.ID, Kind: kind, OccurredAt: occurred.Unix(), ReceivedAt: now.Unix()}
			result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&event)
			if result.Error != nil || result.RowsAffected == 0 {
				return result.Error
			}
			return reconcile(tx, event.ProviderID)
		})
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
