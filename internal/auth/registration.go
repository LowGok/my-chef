package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"time"

	"github.com/LowGok/my-chef/internal/domain"
	"github.com/LowGok/my-chef/internal/email/mailqueue"
	"github.com/LowGok/my-chef/internal/store"
	"github.com/ncruces/go-sqlite3"
	"gorm.io/gorm"
)

var ErrSignUpProof = errors.New("invalid sign-up link")
var ErrSignUpUnavailable = errors.New("sign-up unavailable")

type SignUp struct {
	Store             *store.Store
	Keys              *mailqueue.ProofKeys
	RateKey           []byte
	Origin            string
	AllowLoopbackLink bool
	Now               func() time.Time
}

type SignUpDetails struct {
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
	Username  string `json:"username"`
}

type SignUpRequest struct {
	SignUpDetails
	Email string `json:"email"`
}

type SignUpCompletion struct {
	SignUpDetails
	Proof    string `json:"proof"`
	Password string `json:"password"`
}

type signUpDraft struct {
	SignUpRequest
	EmailKey string
}

func (s *SignUp) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (d SignUpDetails) valid() bool {
	return ValidName(d.FirstName, 100) && ValidName(d.LastName, 100) && ValidName(d.Username, 80)
}

func randomSignUpID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", ErrSignUpUnavailable
	}
	return hex.EncodeToString(id[:]), nil
}

func signUpProofHash(token string) (string, error) {
	if len(token) != 43 {
		return "", ErrSignUpProof
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(raw) != 32 {
		return "", ErrSignUpProof
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (s *SignUp) proofLink(token string) (string, error) {
	return s.proofURL("/app/signup", token)
}

func (s *SignUp) proofURL(path, token string) (string, error) {
	link, err := url.Parse(s.Origin)
	if err != nil || link.Hostname() == "" || link.User != nil || link.Path != "" || link.RawQuery != "" || link.Fragment != "" {
		return "", ErrSignUpUnavailable
	}
	if link.Scheme != "https" {
		host, err := netip.ParseAddr(link.Hostname())
		if !s.AllowLoopbackLink || link.Scheme != "http" || err != nil || !host.IsLoopback() {
			return "", ErrSignUpUnavailable
		}
	}
	link.Path, link.Fragment = path, token
	return link.String(), nil
}

func signUpJobBinding(job store.SignUpJob) string {
	return fmt.Sprintf("%s:%d:%d", job.ID, job.Requested, job.Expires)
}

func signUpDraftBinding(proof store.SignUpProof) string {
	return fmt.Sprintf("%s:%s:%s:%d", proof.ID, proof.Hash, proof.AddressTag, proof.Expires)
}

func (s *SignUp) sealDraft(purpose, binding string, draft signUpDraft) (string, error) {
	raw, err := json.Marshal(draft)
	if err != nil {
		return "", ErrSignUpUnavailable
	}
	box, err := s.Keys.Seal(purpose, binding, raw)
	if err != nil {
		return "", ErrSignUpUnavailable
	}
	data, err := json.Marshal(box)
	if err != nil {
		return "", ErrSignUpUnavailable
	}
	return string(data), nil
}

func (s *SignUp) openDraft(purpose, binding, data string) (signUpDraft, error) {
	var box mailqueue.SealedProof
	if json.Unmarshal([]byte(data), &box) != nil {
		return signUpDraft{}, ErrSignUpProof
	}
	raw, err := s.Keys.Open(purpose, binding, box)
	var draft signUpDraft
	if err != nil || json.Unmarshal(raw, &draft) != nil {
		return signUpDraft{}, ErrSignUpProof
	}
	return draft, nil
}

// Request does the same write for each eligible address. The worker is the
// first code to look up the account.
func (s *SignUp) Request(ctx context.Context, source string, request SignUpRequest) (int, error) {
	display, key, err := store.EmailIdentity(request.Email)
	if err != nil || !request.valid() {
		return 0, ErrInput
	}
	if _, err := s.proofLink(""); err != nil {
		return 0, err
	}
	emailBucket, err := store.EmailRateBucket(s.RateKey, "proof-delivery/address", key)
	if err != nil {
		return 0, ErrSignUpUnavailable
	}
	sourceBucket, err := store.EmailRateBucket(s.RateKey, "signup/source", source)
	if err != nil {
		return 0, ErrSignUpUnavailable
	}
	id, err := randomSignUpID()
	if err != nil {
		return 0, err
	}
	now := s.now()
	job := store.SignUpJob{ID: id, Requested: now.Unix(), Expires: now.Add(30 * time.Minute).Unix()}
	request.Email = display
	job.Draft, err = s.sealDraft("signup-job", signUpJobBinding(job), signUpDraft{SignUpRequest: request, EmailKey: key})
	if err != nil {
		return 0, err
	}
	var wait int
	err = s.Store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rate, err := s.Store.ReserveEmailLimits(ctx, tx, now,
			store.EmailLimit{Bucket: emailBucket, Maximum: 5, Window: 30 * time.Minute},
			store.EmailLimit{Bucket: sourceBucket, Maximum: 20, Window: 30 * time.Minute})
		if err != nil {
			return err
		}
		if wait = rate.RetryAfter; wait != 0 {
			return nil
		}
		return tx.Create(&job).Error
	})
	if err != nil {
		return 0, ErrSignUpUnavailable
	}
	return wait, nil
}

// Step resolves one protected request. The token and delivery row share a
// transaction with deletion of the job, so a retry cannot issue another link.
func (s *SignUp) Step(ctx context.Context) (bool, error) {
	now := s.now()
	var processed bool
	err := s.Store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.clearExpired(ctx, tx, now); err != nil {
			return err
		}
		var job store.SignUpJob
		err := tx.Order("requested, id").First(&job).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		processed = true
		draft, err := s.openDraft("signup-job", signUpJobBinding(job), job.Draft)
		if err != nil {
			return tx.Delete(&job).Error
		}
		claimed, err := addressClaimed(tx, draft.EmailKey)
		if err != nil {
			return err
		}
		if !claimed {
			if err := s.issue(ctx, tx, now, job.ID, draft); err != nil {
				return err
			}
		}
		return tx.Delete(&job).Error
	})
	if err != nil {
		return false, ErrSignUpUnavailable
	}
	return processed, nil
}

func (s *SignUp) clearExpired(ctx context.Context, tx *gorm.DB, now time.Time) error {
	if err := pruneEmailChanges(ctx, tx, now); err != nil {
		return err
	}
	if err := pruneVerificationProofs(ctx, tx, now); err != nil {
		return err
	}
	if err := tx.Where("expires <= ?", now.Unix()).Delete(&store.SignUpJob{}).Error; err != nil {
		return err
	}
	var proofs []store.SignUpProof
	if err := tx.Where("expires <= ?", now.Unix()).Order("expires, id").Limit(32).Find(&proofs).Error; err != nil {
		return err
	}
	for _, proof := range proofs {
		if err := mailqueue.InvalidateProtected(ctx, tx, now, "signup/"+proof.ID); err != nil {
			return err
		}
		if err := tx.Delete(&proof).Error; err != nil {
			return err
		}
	}
	return nil
}

func addressClaimed(db *gorm.DB, key string) (bool, error) {
	var count int64
	err := db.Model(&store.User{}).Where("email_key = ?", key).Count(&count).Error
	return count != 0, err
}

func (s *SignUp) issue(ctx context.Context, tx *gorm.DB, now time.Time, id string, draft signUpDraft) error {
	var entropy [32]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return ErrSignUpUnavailable
	}
	token := base64.RawURLEncoding.EncodeToString(entropy[:])
	sum := sha256.Sum256(entropy[:])
	tag, err := store.EmailRateBucket(s.RateKey, "signup-proof/address", draft.EmailKey)
	if err != nil {
		return err
	}
	expires := now.Add(24 * time.Hour)
	proof := store.SignUpProof{ID: id, Hash: hex.EncodeToString(sum[:]), AddressTag: tag, Expires: expires.Unix()}
	proof.Draft, err = s.sealDraft("signup-draft", signUpDraftBinding(proof), draft)
	if err != nil {
		return err
	}
	link, err := s.proofLink(token)
	if err != nil {
		return err
	}
	if err = tx.Create(&proof).Error; err != nil {
		return err
	}
	_, err = mailqueue.EnqueueProtected(ctx, s.Store, tx, now, expires, s.Keys, mailqueue.Request{
		Key: "signup/" + id, Recipient: draft.Email, Template: mailqueue.SignUpProofTemplate, Fields: map[string]string{"link": link},
	})
	return err
}

func (s *SignUp) readProof(db *gorm.DB, hash string, now time.Time) (store.SignUpProof, signUpDraft, error) {
	var proof store.SignUpProof
	err := db.Where("hash = ? AND consumed IS NULL AND expires > ?", hash, now.Unix()).First(&proof).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return proof, signUpDraft{}, ErrSignUpProof
	}
	if err != nil {
		return proof, signUpDraft{}, ErrSignUpUnavailable
	}
	draft, err := s.openDraft("signup-draft", signUpDraftBinding(proof), proof.Draft)
	return proof, draft, err
}

func (s *SignUp) Inspect(ctx context.Context, token string) (SignUpDetails, error) {
	hash, err := signUpProofHash(token)
	if err != nil {
		return SignUpDetails{}, err
	}
	_, draft, err := s.readProof(s.Store.DB.WithContext(ctx), hash, s.now())
	return draft.SignUpDetails, err
}

func (s *SignUp) Complete(ctx context.Context, anonymousToken string, input SignUpCompletion) error {
	hash, err := signUpProofHash(input.Proof)
	if err != nil {
		return err
	}
	if _, _, err := s.readProof(s.Store.DB.WithContext(ctx), hash, s.now()); err != nil {
		return err
	}
	if !input.valid() {
		return ErrInput
	}
	normalized, err := checkNewPassword(input.Password)
	if err != nil {
		return err
	}
	credential, err := newPasswordHash(ctx, normalized)
	if err != nil {
		return ErrSignUpUnavailable
	}
	err = s.Store.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := s.now()
		var session store.Session
		err := tx.Where("hash = ? AND user_id IS NULL AND expires_unix > ?", Hash(anonymousToken), now.Unix()).Take(&session).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrSession
		}
		if err != nil {
			return err
		}
		proof, draft, err := s.readProof(tx, hash, now)
		if err != nil {
			return err
		}
		claimed, err := addressClaimed(tx, draft.EmailKey)
		if err != nil {
			return err
		}
		if claimed {
			return ErrSignUpProof
		}
		prefs, _ := json.Marshal(domain.EmptyPreferences())
		user := store.User{Email: draft.Email, EmailVerifiedAt: store.Ptr(now.Unix()), Password: credential,
			Username: input.Username, FirstName: input.FirstName, LastName: input.LastName, PreferencesJSON: store.Ptr(string(prefs))}
		if err = tx.Create(&user).Error; errors.Is(err, sqlite3.CONSTRAINT_UNIQUE) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		if err := invalidateRecovery(ctx, tx, now, user.ID, s.RateKey, draft.EmailKey); err != nil {
			return err
		}
		var accountIncarnation string
		if err := tx.Raw("SELECT incarnation FROM account_incarnations WHERE user_id=?", user.ID).Row().Scan(&accountIncarnation); err != nil {
			return err
		}
		var claimedProofs []store.SignUpProof
		if err := tx.Where("address_tag = ? AND consumed IS NULL", proof.AddressTag).Find(&claimedProofs).Error; err != nil {
			return err
		}
		for _, claimedProof := range claimedProofs {
			if err := tx.Exec("INSERT INTO account_signup_owners(proof_id,incarnation) VALUES(?,?) ON CONFLICT(proof_id) DO NOTHING", claimedProof.ID, accountIncarnation).Error; err != nil {
				return err
			}
			var outboxID int64
			if err := tx.Raw("SELECT id FROM email_outbox WHERE key=?", "signup/"+claimedProof.ID).Row().Scan(&outboxID); err != nil {
				return err
			}
			if err := store.LinkAccountMail(tx, user.ID, outboxID); err != nil {
				return err
			}
		}
		return consumeSignUpAddress(ctx, tx, now, proof.AddressTag)
	})
	if err == nil || errors.Is(err, ErrSignUpProof) || errors.Is(err, ErrConflict) || errors.Is(err, ErrSession) {
		return err
	}
	return ErrSignUpUnavailable
}

func (s *SignUp) Run(ctx context.Context) error {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for ctx.Err() == nil {
		worked, err := s.Step(ctx)
		if err != nil {
			return err
		}
		if worked {
			continue
		}
		select {
		case <-ctx.Done():
		case <-tick.C:
		}
	}
	return nil
}

func consumeSignUpAddress(ctx context.Context, tx *gorm.DB, now time.Time, tag string) error {
	var proofs []store.SignUpProof
	if err := tx.Where("address_tag = ? AND consumed IS NULL", tag).Find(&proofs).Error; err != nil {
		return err
	}
	for _, proof := range proofs {
		if err := mailqueue.InvalidateProtected(ctx, tx, now, "signup/"+proof.ID); err != nil {
			return err
		}
	}
	return tx.Model(&store.SignUpProof{}).Where("address_tag = ? AND consumed IS NULL", tag).
		Updates(map[string]any{"consumed": now.Unix(), "draft": ""}).Error
}
