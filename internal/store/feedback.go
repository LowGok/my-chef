package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"
)

var (
	ErrFeedbackInput       = errors.New("invalid feedback input")
	ErrFeedbackSession     = errors.New("verified feedback session required")
	ErrFeedbackConflict    = errors.New("feedback request key conflict")
	ErrFeedbackNotFound    = errors.New("feedback resource unavailable")
	ErrFeedbackUnavailable = errors.New("feedback mailbox unavailable")
	ErrFeedbackStorage     = errors.New("feedback storage unavailable")
	ErrFeedbackUnchanged   = errors.New("rating unchanged")
)

const FeedbackRecipeSource = "legacy_meal"

type FeedbackLimit struct{ RetryAfter int }

func (*FeedbackLimit) Error() string { return "feedback rate limit reached" }

type FeedbackActor struct {
	UserID      int64
	SessionHash string
}

type FeedbackInput struct {
	RequestKey string
	Topic      string
	Message    string
	RecipeID   *int64
}

type FeedbackRatingInput struct {
	RequestKey string
	RecipeID   int64
	Rating     int
}

type FeedbackReceipt struct {
	Reference   string
	CreatedUnix int64
}

type FeedbackNotice struct {
	OwnerID int64
	FeedbackReceipt
	Topic, Message, Priority, ContactEmail string
	RecipeID                               *int64
}

type FeedbackEntitlement struct {
	Premium     bool
	ExpiresUnix int64
}

type FeedbackEntitlementReader func(context.Context, *gorm.DB, int64) (FeedbackEntitlement, error)
type FeedbackEnqueue func(*gorm.DB, FeedbackNotice) (int64, error)

type feedbackOperation string

const (
	reportOperation feedbackOperation = "feedback"
	ratingOperation feedbackOperation = "rating"
)

func (o feedbackOperation) table() string {
	if o == ratingOperation {
		return "feedback_rating_receipts"
	}
	return "feedback_reports"
}

func validFeedbackKey(key string) bool {
	// The app generates 128 random bits as lowercase hexadecimal.
	return len(key) == 32 && strings.IndexFunc(key, func(r rune) bool {
		return (r < '0' || r > '9') && (r < 'a' || r > 'f')
	}) == -1
}

func validFeedback(in FeedbackInput) bool {
	switch in.Topic {
	case "technical_issue", "subscription", "other":
		if in.RecipeID != nil {
			return false
		}
	case "recipe_issue":
		if in.RecipeID != nil && *in.RecipeID <= 0 {
			return false
		}
	default:
		return false
	}
	n := utf8.RuneCountInString(in.Message)
	return validFeedbackKey(in.RequestKey) && n >= 20 && n <= 4000 &&
		utf8.ValidString(in.Message) && !strings.ContainsRune(in.Message, 0) && strings.TrimSpace(in.Message) != ""
}

func feedbackAccount(tx *gorm.DB, actor FeedbackActor, now int64) (string, error) {
	var email string
	err := tx.Raw(`SELECT u.email FROM users u JOIN sessions s ON s.user_id=u.user_id
WHERE u.user_id=? AND u.email_verified_at IS NOT NULL AND s.hash=? AND s.expires_unix>?`,
		actor.UserID, actor.SessionHash, now).Row().Scan(&email)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrFeedbackSession
	}
	if err != nil {
		return "", ErrFeedbackStorage
	}
	return email, nil
}

func feedbackRecipe(tx *gorm.DB, owner, recipe int64) error {
	var exists bool
	err := tx.Raw(`SELECT EXISTS (
SELECT 1 FROM meals WHERE creator_id=? AND meal_id=?
UNION ALL
SELECT 1 FROM favorites WHERE user_id=? AND recipe_id=? AND saved=1
)`, owner, recipe, owner, recipe).Row().Scan(&exists)
	if err != nil {
		return ErrFeedbackStorage
	}
	if !exists {
		return ErrFeedbackNotFound
	}
	return nil
}

func feedbackHash(payload any) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", ErrFeedbackStorage
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func feedbackReference(now int64) (FeedbackReceipt, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return FeedbackReceipt{}, ErrFeedbackStorage
	}
	return FeedbackReceipt{Reference: hex.EncodeToString(raw[:]), CreatedUnix: now}, nil
}

func feedbackExisting(tx *gorm.DB, operation feedbackOperation, user int64, key string) (FeedbackReceipt, string, error) {
	var receipt FeedbackReceipt
	var hash string
	err := tx.Raw("SELECT reference,created_unix,request_hash FROM "+operation.table()+" WHERE user_id=? AND request_key=?",
		user, key).Row().Scan(&receipt.Reference, &receipt.CreatedUnix, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return FeedbackReceipt{}, "", ErrFeedbackNotFound
	}
	if err != nil {
		return FeedbackReceipt{}, "", ErrFeedbackStorage
	}
	return receipt, hash, nil
}

func feedbackReplay(tx *gorm.DB, operation feedbackOperation, user int64, key, hash string) (FeedbackReceipt, bool, error) {
	receipt, prior, err := feedbackExisting(tx, operation, user, key)
	switch {
	case errors.Is(err, ErrFeedbackNotFound):
		return FeedbackReceipt{}, false, nil
	case err != nil:
		return FeedbackReceipt{}, true, err
	case prior != hash:
		return FeedbackReceipt{}, true, ErrFeedbackConflict
	default:
		return receipt, true, nil
	}
}

func feedbackQuota(tx *gorm.DB, operation feedbackOperation, user, now int64, capacity int) error {
	const window = int64(time.Hour / time.Second)
	var boundary int64
	err := tx.Raw("SELECT created_unix FROM "+operation.table()+
		" WHERE user_id=? AND created_unix>? ORDER BY created_unix DESC,id DESC LIMIT 1 OFFSET ?",
		user, now-window, capacity-1).Row().Scan(&boundary)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return ErrFeedbackStorage
	}
	return &FeedbackLimit{RetryAfter: int(boundary + window - now)}
}

func feedbackError(err error) error {
	var limit *FeedbackLimit
	switch {
	case err == nil, errors.Is(err, ErrFeedbackInput), errors.Is(err, ErrFeedbackSession),
		errors.Is(err, ErrFeedbackConflict), errors.Is(err, ErrFeedbackNotFound),
		errors.Is(err, ErrFeedbackUnavailable), errors.Is(err, ErrFeedbackUnchanged), errors.As(err, &limit):
		return err
	default:
		return ErrFeedbackStorage
	}
}

// WriteFeedback commits the report and its operator notification together.
func (s *Store) WriteFeedback(ctx context.Context, actor FeedbackActor, in FeedbackInput, now time.Time,
	entitlement FeedbackEntitlementReader, enqueue FeedbackEnqueue) (FeedbackReceipt, error) {
	if !validFeedback(in) {
		return FeedbackReceipt{}, ErrFeedbackInput
	}
	hash, err := feedbackHash(in)
	if err != nil {
		return FeedbackReceipt{}, err
	}
	var result FeedbackReceipt
	stamp := now.Unix()
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		email, err := feedbackAccount(tx, actor, stamp)
		if err != nil {
			return err
		}
		if prior, handled, err := feedbackReplay(tx, reportOperation, actor.UserID, in.RequestKey, hash); handled {
			result = prior
			return err
		}
		if enqueue == nil {
			return ErrFeedbackUnavailable
		}
		var kind *string
		if in.RecipeID != nil {
			if err := feedbackRecipe(tx, actor.UserID, *in.RecipeID); err != nil {
				return err
			}
			kind = Ptr(FeedbackRecipeSource)
		}
		if err := feedbackQuota(tx, reportOperation, actor.UserID, stamp, 5); err != nil {
			return err
		}
		priority := "standard"
		if entitlement != nil {
			state, err := entitlement(ctx, tx, actor.UserID)
			if err != nil {
				return ErrFeedbackStorage
			}
			if state.Premium && state.ExpiresUnix > stamp {
				priority = "priority"
			}
		}
		result, err = feedbackReference(stamp)
		if err != nil {
			return err
		}
		outbox, err := enqueue(tx, FeedbackNotice{OwnerID: actor.UserID, FeedbackReceipt: result, Topic: in.Topic, Message: in.Message,
			Priority: priority, ContactEmail: email, RecipeID: in.RecipeID})
		if err != nil || outbox <= 0 {
			return ErrFeedbackStorage
		}
		return tx.Exec(`INSERT INTO feedback_reports(user_id,request_key,request_hash,reference,topic,message,source_kind,source_id,priority,created_unix,outbox_id)
VALUES(?,?,?,?,?,?,?,?,?,?,?)`, actor.UserID, in.RequestKey, hash, result.Reference, in.Topic, in.Message, kind, in.RecipeID,
			priority, stamp, outbox).Error
	})
	if err != nil {
		return FeedbackReceipt{}, feedbackError(err)
	}
	return result, nil
}

func feedbackCurrentRating(tx *gorm.DB, user, recipe int64) (*int, error) {
	var rating int
	err := tx.Raw("SELECT rating FROM feedback_ratings WHERE user_id=? AND source_kind=? AND source_id=?",
		user, FeedbackRecipeSource, recipe).Row().Scan(&rating)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, ErrFeedbackStorage
	}
	return &rating, nil
}

// WriteFeedbackRating changes only this user's current rating and records
// the request receipt. Replaying a receipt never reapplies its score.
func (s *Store) WriteFeedbackRating(ctx context.Context, actor FeedbackActor, in FeedbackRatingInput, now time.Time) (FeedbackReceipt, error) {
	if !validFeedbackKey(in.RequestKey) || in.RecipeID <= 0 || in.Rating < 1 || in.Rating > 5 {
		return FeedbackReceipt{}, ErrFeedbackInput
	}
	hash, err := feedbackHash(in)
	if err != nil {
		return FeedbackReceipt{}, err
	}
	var result FeedbackReceipt
	stamp := now.Unix()
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := feedbackAccount(tx, actor, stamp); err != nil {
			return err
		}
		if prior, handled, err := feedbackReplay(tx, ratingOperation, actor.UserID, in.RequestKey, hash); handled {
			result = prior
			return err
		}
		if err := feedbackRecipe(tx, actor.UserID, in.RecipeID); err != nil {
			return err
		}
		current, err := feedbackCurrentRating(tx, actor.UserID, in.RecipeID)
		if err != nil {
			return err
		}
		if current != nil && *current == in.Rating {
			return ErrFeedbackUnchanged
		}
		if err := feedbackQuota(tx, ratingOperation, actor.UserID, stamp, 30); err != nil {
			return err
		}
		result, err = feedbackReference(stamp)
		if err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO feedback_ratings(user_id,source_kind,source_id,rating) VALUES(?,?,?,?)
ON CONFLICT(user_id,source_kind,source_id) DO UPDATE SET rating=excluded.rating`,
			actor.UserID, FeedbackRecipeSource, in.RecipeID, in.Rating).Error; err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO feedback_rating_receipts(user_id,request_key,request_hash,reference,created_unix) VALUES(?,?,?,?,?)`,
			actor.UserID, in.RequestKey, hash, result.Reference, stamp).Error
	})
	if err != nil {
		return FeedbackReceipt{}, feedbackError(err)
	}
	return result, nil
}

func (s *Store) FeedbackReceipt(ctx context.Context, actor FeedbackActor, operation, key string, now time.Time) (FeedbackReceipt, error) {
	kind := feedbackOperation(operation)
	if !validFeedbackKey(key) || kind != reportOperation && kind != ratingOperation {
		return FeedbackReceipt{}, ErrFeedbackInput
	}
	var receipt FeedbackReceipt
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := feedbackAccount(tx, actor, now.Unix()); err != nil {
			return err
		}
		var err error
		receipt, _, err = feedbackExisting(tx, kind, actor.UserID, key)
		return err
	})
	return receipt, feedbackError(err)
}

func (s *Store) FeedbackRating(ctx context.Context, actor FeedbackActor, recipe int64, now time.Time) (*int, error) {
	if recipe <= 0 {
		return nil, ErrFeedbackInput
	}
	var current *int
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := feedbackAccount(tx, actor, now.Unix()); err != nil {
			return err
		}
		if err := feedbackRecipe(tx, actor.UserID, recipe); err != nil {
			return err
		}
		var err error
		current, err = feedbackCurrentRating(tx, actor.UserID, recipe)
		return err
	})
	return current, feedbackError(err)
}

type FeedbackDeliveryFailure struct {
	ID        int64  `json:"id"`
	Reference string `json:"reference"`
	Outcome   string `json:"outcome"`
}

// FeedbackDeliveryFailures returns only metadata for a private operator check.
func (s *Store) FeedbackDeliveryFailures(ctx context.Context, beforeID int64) ([]FeedbackDeliveryFailure, error) {
	if beforeID < 0 {
		return nil, ErrFeedbackInput
	}
	rows := make([]FeedbackDeliveryFailure, 0)
	err := s.DB.WithContext(ctx).Raw(`SELECT r.id, r.reference,
CASE WHEN o.outcome IN ('failed','bounced','complained','suppressed') THEN o.outcome
WHEN o.reason IN ('provider_rejected','provider_suppressed','retry_exhausted','configuration_changed','invalid_template') THEN o.reason
ELSE 'failed' END AS outcome
FROM feedback_reports r JOIN email_outbox o ON o.id=r.outbox_id
WHERE (?=0 OR r.id<?) AND (o.state='failed' OR o.outcome IN ('failed','bounced','complained','suppressed'))
ORDER BY r.id DESC LIMIT 100`, beforeID, beforeID).Scan(&rows).Error
	if err != nil {
		return nil, ErrFeedbackStorage
	}
	return rows, nil
}
