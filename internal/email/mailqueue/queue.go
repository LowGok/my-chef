package mailqueue

import (
	"context"
	"encoding/json"
	"errors"
	"net/mail"
	"strings"
	"time"

	"github.com/LowGok/my-chef/internal/store"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrKeyConflict = errors.New("email idempotency key conflict")
var ErrStorage = errors.New("email outbox unavailable")

type Request struct {
	OwnerID                  int64
	Key, Recipient, Template string
	Fields                   map[string]string
}

type Record struct {
	ID                                                                  int64
	Key, Recipient, Template, Data, State, Outcome, Reason, Fingerprint string
	ProviderID                                                          *string
	Attempts                                                            int
	NextAt                                                              int64
	CreatedAt                                                           int64 `gorm:"autoCreateTime:false"`
	UpdatedAt                                                           int64 `gorm:"autoUpdateTime:false"`
}

func (Record) TableName() string { return "email_outbox" }

func validRequest(req Request) bool {
	address, err := mail.ParseAddress(req.Recipient)
	return err == nil && address.Address == req.Recipient && len(req.Recipient) <= 254 &&
		len(req.Key) > 0 && len(req.Key) <= 256 && strings.IndexFunc(req.Key, func(r rune) bool { return r < 33 || r > 126 }) < 0
}

// Enqueue joins tx when provided. The returned ID is durable after commit.
func Enqueue(ctx context.Context, s *store.Store, tx *gorm.DB, at time.Time, req Request) (int64, error) {
	if !validRequest(req) {
		return 0, errors.New("invalid email recipient or key")
	}
	if ProtectedTemplate(req.Template) {
		return 0, ErrProofUnavailable
	}
	if _, err := Render(req.Template, req.Fields); err != nil {
		return 0, err
	}
	payload, err := json.Marshal(req.Fields)
	if err != nil {
		return 0, errors.New("invalid email fields")
	}
	now := at.Unix()
	row := Record{Key: req.Key, Recipient: req.Recipient, Template: req.Template, Data: string(payload), State: "queued", NextAt: now, CreatedAt: now, UpdatedAt: now}
	save := func(db *gorm.DB) error {
		result := db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoNothing: true}).Create(&row)
		if result.Error != nil {
			return ErrStorage
		}
		if result.RowsAffected == 1 {
			return store.LinkAccountMail(db, req.OwnerID, row.ID)
		}
		var existing Record
		if err := db.Where("key = ?", req.Key).Take(&existing).Error; err != nil {
			return ErrStorage
		}
		if existing.Recipient != row.Recipient || existing.Template != row.Template || existing.Data != row.Data {
			return ErrKeyConflict
		}
		row.ID = existing.ID
		return store.LinkAccountMail(db, req.OwnerID, row.ID)
	}
	if tx == nil {
		err = s.DB.WithContext(ctx).Transaction(save)
	} else {
		err = save(tx.WithContext(ctx))
	}
	if err != nil && !errors.Is(err, ErrKeyConflict) {
		return 0, ErrStorage
	}
	if err != nil {
		return 0, err
	}
	return row.ID, nil
}
