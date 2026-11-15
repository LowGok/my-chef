package store

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync/atomic"

	"github.com/ncruces/go-sqlite3"
	"gorm.io/gorm"
)

const newsletterIdentitySetting = "mychef:newsletter-identity-key"

var ErrNewsletterIdentityUnavailable = errors.New("newsletter identity unavailable")

// NewsletterIdentityKey is separate from the rate/erasure domains. Its opaque
// fingerprint may be persisted; the derived secret remains only in memory.
type NewsletterIdentityKey struct {
	version    int
	identifier string
	secret     [32]byte
}

func DeriveNewsletterIdentityKey(root []byte) (*NewsletterIdentityKey, error) {
	if len(root) != 32 {
		return nil, ErrNewsletterIdentityUnavailable
	}
	mac := hmac.New(sha256.New, root)
	_, _ = mac.Write([]byte("mychef/newsletter/identity-match/v1/key"))
	key := &NewsletterIdentityKey{version: 1}
	copy(key.secret[:], mac.Sum(nil))
	key.identifier = newsletterIdentityFingerprint(key.secret[:])
	return key, nil
}

func newsletterIdentityFingerprint(secret []byte) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("mychef/newsletter/identity-match/v1/id\x00"))
	_, _ = hash.Write(secret)
	return hex.EncodeToString(hash.Sum(nil))
}

func (k *NewsletterIdentityKey) Version() int {
	if k == nil {
		return 0
	}
	return k.version
}

func (k *NewsletterIdentityKey) ID() string {
	if k == nil {
		return ""
	}
	return k.identifier
}

func (k *NewsletterIdentityKey) valid() bool {
	return k != nil && k.version == 1 && k.identifier == newsletterIdentityFingerprint(k.secret[:])
}

func (k *NewsletterIdentityKey) Tag(address string) (string, error) {
	if !k.valid() {
		return "", ErrNewsletterIdentityUnavailable
	}
	_, canonical, err := EmailIdentity(address)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, k.secret[:])
	_, _ = mac.Write([]byte("mychef/newsletter/identity-match/v1/email\x00"))
	_, _ = mac.Write([]byte(canonical))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func newsletterIdentityKey(tx *gorm.DB) *NewsletterIdentityKey {
	value, ok := tx.Get(newsletterIdentitySetting)
	if !ok {
		return nil
	}
	state, ok := value.(*atomic.Pointer[NewsletterIdentityKey])
	if !ok || state == nil {
		return nil
	}
	return state.Load()
}

// PrepareNewsletterIdentityNamespace is an explicit stopped-migration step. It
// neither installs a runtime key nor marks backfill/physical maintenance ready.
// Journal coordination must precede the production call and final readiness.
func (s *Store) PrepareNewsletterIdentityNamespace(ctx context.Context, key *NewsletterIdentityKey) error {
	if !key.valid() {
		return ErrNewsletterIdentityUnavailable
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing NewsletterIdentityNamespace
		err := tx.Take(&existing, "singleton=1").Error
		if err == nil {
			if existing.KeyVersion != key.Version() || existing.KeyID != key.ID() {
				return ErrNewsletterIdentityUnavailable
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var reduced int64
		if err := tx.Raw(`SELECT (SELECT count(*) FROM users WHERE newsletter_minimised=1)
+ (SELECT count(*) FROM newsletter_identity_matches)`).Scan(&reduced).Error; err != nil {
			return err
		}
		if reduced != 0 {
			return ErrNewsletterIdentityUnavailable
		}
		return tx.Create(&NewsletterIdentityNamespace{Singleton: 1, KeyVersion: key.Version(), KeyID: key.ID(), LegacyVacuumPending: true}).Error
	})
}

// ConfigureNewsletterIdentity verifies an existing binding, then installs an
// immutable shared pointer for ORM transactions. It never adopts or rotates it.
func (s *Store) ConfigureNewsletterIdentity(ctx context.Context, key *NewsletterIdentityKey) error {
	if !key.valid() {
		return ErrNewsletterIdentityUnavailable
	}
	var binding NewsletterIdentityNamespace
	if err := s.DB.WithContext(ctx).Take(&binding, "singleton=1").Error; err != nil {
		return err
	}
	if binding.KeyVersion != key.Version() || binding.KeyID != key.ID() {
		return ErrNewsletterIdentityUnavailable
	}
	current := s.newsletterIdentity.Load()
	if current != nil && (current.ID() != key.ID() || current.Version() != key.Version()) {
		return ErrNewsletterIdentityUnavailable
	}
	copy := *key
	s.newsletterIdentity.Store(&copy)
	return nil
}

func newsletterIdentityNamespace(tx *gorm.DB, key *NewsletterIdentityKey) (bool, error) {
	var tables int64
	if err := tx.Raw("SELECT count(*) FROM sqlite_schema WHERE type='table' AND name IN ('newsletter_identity_namespace','newsletter_identity_matches')").Scan(&tables).Error; err != nil {
		return false, err
	}
	if tables == 0 && key == nil {
		return false, nil // Published pre-27 generic stores have no protected matches.
	}
	if tables != 2 {
		return false, ErrNewsletterIdentityUnavailable
	}
	var binding NewsletterIdentityNamespace
	err := tx.Take(&binding, "singleton=1").Error
	if errors.Is(err, gorm.ErrRecordNotFound) && key == nil {
		var reduced int64
		if err := tx.Raw(`SELECT (SELECT count(*) FROM users WHERE newsletter_minimised=1)
+ (SELECT count(*) FROM newsletter_identity_matches)`).Scan(&reduced).Error; err != nil {
			return false, err
		}
		if reduced == 0 {
			return false, nil
		}
	}
	if err != nil {
		return false, ErrNewsletterIdentityUnavailable
	}
	if !key.valid() || binding.KeyVersion != key.Version() || binding.KeyID != key.ID() || binding.PhysicalCleanupPending || binding.LegacyVacuumPending {
		return false, ErrNewsletterIdentityUnavailable
	}
	return true, nil
}

// ResolveEmailOwner provides one SQL snapshot. Callers already holding account
// work ownership use the Tx variant; neither helper recursively takes that fence.
func (s *Store) ResolveEmailOwner(ctx context.Context, address string) (User, bool, error) {
	var owner User
	var found bool
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		owner, found, err = s.ResolveEmailOwnerTx(ctx, tx, address)
		return err
	})
	return owner, found, err
}

func (s *Store) ResolveEmailOwnerTx(ctx context.Context, tx *gorm.DB, address string) (User, bool, error) {
	if tx == nil {
		return User{}, false, ErrNewsletterIdentityUnavailable
	}
	return resolveNewsletterEmailOwner(tx.WithContext(ctx), s.newsletterIdentity.Load(), address)
}

func resolveNewsletterEmailOwner(tx *gorm.DB, key *NewsletterIdentityKey, address string) (User, bool, error) {
	// Keep the caller's transaction/Context, but never its model, conditions or
	// query destination. Hooks and unrelated ORM operations share no statements.
	tx = tx.Session(&gorm.Session{NewDB: true})
	_, canonical, err := EmailIdentity(address)
	if err != nil {
		return User{}, false, err
	}
	bound, err := newsletterIdentityNamespace(tx, key)
	if err != nil {
		return User{}, false, err
	}
	var raw User
	rawErr := tx.Where("email_key=?", canonical).Take(&raw).Error
	if rawErr != nil && !errors.Is(rawErr, gorm.ErrRecordNotFound) {
		return User{}, false, rawErr
	}
	rawFound := rawErr == nil
	if !bound {
		return raw, rawFound, nil
	}
	tag, err := key.Tag(canonical)
	if err != nil {
		return User{}, false, err
	}
	var match NewsletterIdentityMatch
	err = tx.Where("key_version=? AND address_tag=?", key.Version(), tag).Take(&match).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return raw, rawFound, nil
	}
	if err != nil {
		return User{}, false, err
	}
	if rawFound {
		return User{}, false, ErrNewsletterIdentityUnavailable
	}
	var reduced User
	if err := tx.First(&reduced, match.UserID).Error; err != nil {
		return User{}, false, ErrNewsletterIdentityUnavailable
	}
	var incarnation string
	if err := tx.Raw("SELECT incarnation FROM account_incarnations WHERE user_id=?", match.UserID).Row().Scan(&incarnation); err != nil || incarnation != match.Incarnation || !reduced.IsNewsletterOnly || !reduced.NewsletterMinimised || reduced.Email != "" || reduced.EmailKey != "" || reduced.Username != "" || reduced.Password != "" || reduced.EmailVerifiedAt != nil {
		return User{}, false, ErrNewsletterIdentityUnavailable
	}
	return reduced, true, nil
}

func guardNewsletterIdentityCreate(tx *gorm.DB, address string) error {
	owner, found, err := resolveNewsletterEmailOwner(tx, newsletterIdentityKey(tx), address)
	if err != nil {
		return err
	}
	if found && owner.NewsletterMinimised {
		// Preserve callers' ordinary duplicate-email handling without returning
		// the protected owner or granting signup/promotion authority.
		return sqlite3.CONSTRAINT_UNIQUE
	}
	return nil
}
