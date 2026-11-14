package newsletter

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/LowGok/my-chef/internal/email/mailqueue"
	"github.com/LowGok/my-chef/internal/store"
	"github.com/ncruces/go-sqlite3"
	"gorm.io/gorm"
)

var ErrInput = errors.New("invalid newsletter request")
var ErrUnavailable = errors.New("newsletter unavailable")
var ErrProof = errors.New("invalid newsletter link")

type Input struct {
	Email          string `json:"email"`
	Locale         string `json:"locale"`
	ConsentVersion string `json:"consentVersion"`
}

type proposal struct {
	Input
	RequestedUnix int64 `json:"requestedUnix"`
}

type Service struct {
	Store          *store.Store
	Keys           *mailqueue.ProofKeys
	RateKey        []byte
	Origin         string
	ConsentVersion string
	Now            func() time.Time
	Lifecycle      *store.LifecycleJournal
}

type requestRecord struct {
	OrderID       int64 `gorm:"primaryKey"`
	RequestedUnix int64
	ExpiresUnix   int64
	Payload       string
}

func (requestRecord) TableName() string { return "newsletter_requests" }

type subscription struct {
	UserID         int64 `gorm:"primaryKey"`
	EmailKey       string
	Status         string
	Locale         string
	ConsentVersion string
	Source         string
	RequestedUnix  int64
	ConfirmedUnix  *int64
	WithdrawnUnix  *int64
	Revision       int64
	ProvedOrder    int64
}

func (subscription) TableName() string { return "newsletter_subscriptions" }

type proofRecord struct {
	Hash         string `gorm:"primaryKey"`
	UserID       int64
	Purpose      string
	EmailKey     string
	Revision     int64
	RequestOrder int64
	CreatedUnix  int64
	ExpiresUnix  *int64
	ConsumedUnix *int64
	Proposal     string
}

func (proofRecord) TableName() string { return "newsletter_proofs" }

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) valid() bool {
	if s == nil || s.Store == nil || s.Keys == nil || len(s.RateKey) != 32 || s.ConsentVersion == "" || len(s.ConsentVersion) > 80 {
		return false
	}
	u, err := url.Parse(s.Origin)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
}

func localePath(locale string) (string, bool) {
	switch locale {
	case "en":
		return "/", true
	case "fr-BE":
		return "/fr/", true
	case "nl-BE":
		return "/nl/", true
	default:
		return "", false
	}
}

func nextOrder(tx *gorm.DB) (int64, error) {
	var order int64
	err := tx.Raw("UPDATE newsletter_sequence SET value=value+1 WHERE singleton=1 RETURNING value").Row().Scan(&order)
	return order, err
}

func (s *Service) seal(purpose, binding string, value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	box, err := s.Keys.Seal(purpose, binding, raw)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(box)
	return string(encoded), err
}

func (s *Service) open(purpose, binding, encoded string, value any) error {
	var box mailqueue.SealedProof
	if json.Unmarshal([]byte(encoded), &box) != nil {
		return ErrUnavailable
	}
	raw, err := s.Keys.Open(purpose, binding, box)
	if err != nil || json.Unmarshal(raw, value) != nil {
		return ErrUnavailable
	}
	return nil
}

func requestBinding(job requestRecord) string {
	return fmt.Sprintf("%d/%d/%d", job.OrderID, job.RequestedUnix, job.ExpiresUnix)
}

// Request acknowledges a protected job before any lookup of the recipient.
func (s *Service) Request(ctx context.Context, source string, input Input) (int, error) {
	if !s.valid() {
		return 0, ErrUnavailable
	}
	display, key, err := store.EmailIdentity(input.Email)
	_, localeOK := localePath(input.Locale)
	if err != nil || !localeOK || input.ConsentVersion != s.ConsentVersion || source == "" || len(source) > 128 {
		return 0, ErrInput
	}
	input.Email = display
	addressBucket, err := store.EmailRateBucket(s.RateKey, "newsletter/address", key)
	if err != nil {
		return 0, ErrUnavailable
	}
	sourceBucket, err := store.EmailRateBucket(s.RateKey, "newsletter/source", source)
	if err != nil {
		return 0, ErrUnavailable
	}
	now := s.now()
	var wait int
	err = s.Store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rate, err := s.Store.ReserveEmailLimits(ctx, tx, now,
			store.EmailLimit{Bucket: addressBucket + "/minute", Maximum: 1, Window: time.Minute},
			store.EmailLimit{Bucket: addressBucket + "/hour", Maximum: 3, Window: time.Hour},
			store.EmailLimit{Bucket: sourceBucket, Maximum: 10, Window: time.Hour})
		if err != nil {
			return err
		}
		if wait = rate.RetryAfter; wait != 0 {
			return nil
		}
		order, err := nextOrder(tx)
		if err != nil {
			return err
		}
		job := requestRecord{OrderID: order, RequestedUnix: now.Unix(), ExpiresUnix: now.Add(30 * time.Minute).Unix()}
		job.Payload, err = s.seal("newsletter-request", requestBinding(job), input)
		if err != nil {
			return err
		}
		return tx.Create(&job).Error
	})
	if err != nil {
		return 0, ErrUnavailable
	}
	return wait, nil
}

// Step commits recipient resolution, proofs, email and job removal together.
func (s *Service) Step(ctx context.Context) (bool, error) {
	if !s.valid() {
		return false, ErrUnavailable
	}
	s.Store.AccountWork.RLock()
	defer s.Store.AccountWork.RUnlock()
	if s.Lifecycle != nil && s.Lifecycle.CheckReady(ctx) != nil {
		return false, ErrUnavailable
	}
	s.Store.AccountWork.RLock()
	defer s.Store.AccountWork.RUnlock()
	var worked bool
	err := s.Store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job requestRecord
		err := tx.Order("order_id").Take(&job).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		worked = true
		now := s.now()
		if job.ExpiresUnix > now.Unix() {
			var input Input
			if err := s.open("newsletter-request", requestBinding(job), job.Payload, &input); err != nil {
				return err
			}
			if input.ConsentVersion == s.ConsentVersion {
				if err := s.resolve(ctx, tx, now, job, input); err != nil {
					return err
				}
			}
		}
		return tx.Delete(&job).Error
	})
	if err != nil {
		return worked, ErrUnavailable
	}
	return worked, nil
}

func (s *Service) resolve(ctx context.Context, tx *gorm.DB, now time.Time, job requestRecord, input Input) error {
	_, key, err := store.EmailIdentity(input.Email)
	if err != nil {
		return err
	}
	tag, err := store.EmailRateBucket(s.RateKey, "newsletter/erasure", key)
	if err != nil {
		return err
	}
	var erased int64
	if err := tx.Raw("SELECT count(*) FROM newsletter_erasures WHERE address_tag=? AND through_order>=?", tag, job.OrderID).Scan(&erased).Error; err != nil {
		return err
	}
	if erased != 0 {
		return nil
	}
	user, err := resolveUser(tx, input.Email, key)
	if err != nil {
		return err
	}
	var sub subscription
	err = tx.Where("user_id=?", user.ID).Take(&sub).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		prefix, _ := localePath(input.Locale)
		sub = subscription{UserID: user.ID, EmailKey: key, Status: "pending", Locale: input.Locale, ConsentVersion: input.ConsentVersion, Source: prefix + "blog/", RequestedUnix: job.RequestedUnix, Revision: 1}
		if err := tx.Create(&sub).Error; err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if job.OrderID <= sub.ProvedOrder {
		return nil
	}
	if sub.EmailKey != key {
		if err := s.invalidate(ctx, tx, now, sub.UserID); err != nil {
			return err
		}
		sub.EmailKey = key
		sub.Status = "pending"
		sub.Revision++
		sub.ConfirmedUnix = nil
		sub.WithdrawnUnix = nil
		if err := tx.Save(&sub).Error; err != nil {
			return err
		}
	}
	if sub.Status == "confirmed" {
		return nil
	}
	var pending int64
	if err := tx.Model(&proofRecord{}).Where("user_id=? AND purpose='confirm' AND revision=? AND consumed_unix IS NULL AND expires_unix>?", user.ID, sub.Revision, now.Unix()).Count(&pending).Error; err != nil {
		return err
	}
	if pending != 0 {
		return nil
	}
	return s.issue(ctx, tx, now, user, sub, job, input)
}

func resolveUser(tx *gorm.DB, address, key string) (store.User, error) {
	for range 3 {
		var user store.User
		err := tx.Where("email_key=?", key).Take(&user).Error
		if err == nil {
			return user, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return user, err
		}
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return user, err
		}
		user = store.User{Email: address, Username: "newsletter-" + hex.EncodeToString(random[:]), IsNewsletterOnly: true}
		if err := tx.Create(&user).Error; err == nil {
			return user, nil
		} else if !errors.Is(err, sqlite3.CONSTRAINT_UNIQUE) {
			return store.User{}, err
		}
	}
	return store.User{}, ErrUnavailable
}

func newProof() (string, string, error) {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(random[:])
	return base64.RawURLEncoding.EncodeToString(random[:]), hex.EncodeToString(sum[:]), nil
}

func proofBinding(proof proofRecord) string {
	return fmt.Sprintf("%s/%d/%s/%d/%d", proof.Hash, proof.UserID, proof.EmailKey, proof.Revision, proof.RequestOrder)
}

func (s *Service) issue(ctx context.Context, tx *gorm.DB, now time.Time, user store.User, sub subscription, job requestRecord, input Input) error {
	confirmation, hash, err := newProof()
	if err != nil {
		return err
	}
	withdrawal, withdrawalHash, err := newProof()
	if err != nil {
		return err
	}
	expires := now.Add(24 * time.Hour)
	proof := proofRecord{Hash: hash, UserID: user.ID, Purpose: "confirm", EmailKey: user.EmailKey, Revision: sub.Revision, RequestOrder: job.OrderID, CreatedUnix: now.Unix(), ExpiresUnix: store.Ptr(expires.Unix())}
	proof.Proposal, err = s.seal("newsletter-proposal", proofBinding(proof), proposal{Input: input, RequestedUnix: job.RequestedUnix})
	if err != nil {
		return err
	}
	if err := tx.Create(&proof).Error; err != nil {
		return err
	}
	withdraw := proofRecord{Hash: withdrawalHash, UserID: user.ID, Purpose: "unsubscribe", EmailKey: user.EmailKey, Revision: sub.Revision, RequestOrder: job.OrderID, CreatedUnix: now.Unix()}
	if err := tx.Create(&withdraw).Error; err != nil {
		return err
	}
	prefix, _ := localePath(input.Locale)
	template := map[string]string{"en": mailqueue.NewsletterENTemplate, "fr-BE": mailqueue.NewsletterFRTemplate, "nl-BE": mailqueue.NewsletterNLTemplate}[input.Locale]
	_, err = mailqueue.EnqueueProtected(ctx, s.Store, tx, now, expires, s.Keys, mailqueue.Request{OwnerID: user.ID, Key: "newsletter/" + hash, Recipient: user.Email, Template: template,
		Fields: map[string]string{"confirm": s.Origin + prefix + "newsletter/confirm/#" + confirmation, "unsubscribe": s.Origin + prefix + "newsletter/unsubscribe/#" + withdrawal}})
	return err
}

func (s *Service) invalidate(ctx context.Context, tx *gorm.DB, now time.Time, userID int64) error {
	var proofs []proofRecord
	if err := tx.Where("user_id=? AND purpose='confirm'", userID).Find(&proofs).Error; err != nil {
		return err
	}
	for _, proof := range proofs {
		if err := mailqueue.InvalidateProtected(ctx, tx, now, "newsletter/"+proof.Hash); err != nil {
			return err
		}
	}
	return tx.Model(&proofRecord{}).Where("user_id=? AND consumed_unix IS NULL", userID).Update("consumed_unix", now.Unix()).Error
}

func proofHash(token string) (string, error) {
	if len(token) != 43 {
		return "", ErrProof
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(raw) != 32 {
		return "", ErrProof
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// ContactConsent is an internal transaction snapshot, never a public contract.
type ContactConsent struct {
	state subscription
	found bool
}

func ReadContactConsent(tx *gorm.DB, userID int64) (ContactConsent, error) {
	var saved ContactConsent
	err := tx.Where("user_id=?", userID).Take(&saved.state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return saved, nil
	}
	saved.found = err == nil
	return saved, err
}

// TransferContactConsent joins verified email change after placeholder deletion.
func (s *Service) TransferContactConsent(ctx context.Context, tx *gorm.DB, now time.Time, saved ContactConsent, account store.User) error {
	if !saved.found {
		return nil
	}
	if !s.valid() {
		return ErrUnavailable
	}
	var previous int64
	if err := tx.Raw("SELECT COALESCE(MAX(revision),0) FROM newsletter_subscriptions WHERE user_id=?", account.ID).Scan(&previous).Error; err != nil {
		return err
	}
	order, err := nextOrder(tx)
	if err != nil {
		return err
	}
	sub := saved.state
	if sub.EmailKey != account.EmailKey {
		return ErrProof
	}
	sub.UserID, sub.EmailKey = account.ID, account.EmailKey
	sub.Revision, sub.ProvedOrder = max(previous, sub.Revision)+1, order
	if err := tx.Where("user_id=?", account.ID).Delete(&subscription{}).Error; err != nil {
		return err
	}
	if err := tx.Create(&sub).Error; err != nil {
		return err
	}
	if sub.Status != "confirmed" {
		return nil
	}
	token, hash, err := newProof()
	if err != nil {
		return err
	}
	proof := proofRecord{Hash: hash, UserID: account.ID, Purpose: "unsubscribe", EmailKey: account.EmailKey, Revision: sub.Revision, RequestOrder: order, CreatedUnix: now.Unix()}
	if err := tx.Create(&proof).Error; err != nil {
		return err
	}
	prefix, ok := localePath(sub.Locale)
	if !ok {
		return ErrUnavailable
	}
	template := map[string]string{"en": mailqueue.NewsletterWithdrawalENTemplate, "fr-BE": mailqueue.NewsletterWithdrawalFRTemplate, "nl-BE": mailqueue.NewsletterWithdrawalNLTemplate}[sub.Locale]
	_, err = mailqueue.EnqueueProtected(ctx, s.Store, tx, now, now.Add(24*time.Hour), s.Keys, mailqueue.Request{OwnerID: account.ID, Key: "newsletter/" + hash, Recipient: account.Email, Template: template, Fields: map[string]string{"unsubscribe": s.Origin + prefix + "newsletter/unsubscribe/#" + token}})
	return err
}
