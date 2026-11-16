package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
	"gorm.io/gorm"
)

func newsletterIdentityFixtureKey(t *testing.T, offset byte) *NewsletterIdentityKey {
	t.Helper()
	root := make([]byte, 32)
	for i := range root {
		root[i] = byte(i) + offset
	}
	key, err := DeriveNewsletterIdentityKey(root)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func newsletterIdentityReadyFixture(t *testing.T) (*Store, *NewsletterIdentityKey) {
	t.Helper()
	s := minimisationSchemaStore(t)
	key := newsletterIdentityFixtureKey(t, 0)
	if err := s.PrepareNewsletterIdentityNamespace(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfigureNewsletterIdentity(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	// A synthetic identity-resolution fixture, not physical cleanup evidence.
	// The actual maintenance/byte-canary tasks separately verify these markers.
	minimisationSQL(t, s, "UPDATE newsletter_identity_namespace SET physical_cleanup_pending=0,legacy_vacuum_pending=0")
	return s, key
}

func newsletterReducedFixture(t *testing.T, s *Store, key *NewsletterIdentityKey, address string) User {
	t.Helper()
	u := User{Email: address, Username: "opaque-" + strings.ReplaceAll(address, "@", "-"), IsNewsletterOnly: true}
	if err := s.DB.WithContext(t.Context()).Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	var incarnation string
	if err := s.SQL.QueryRowContext(t.Context(), "SELECT incarnation FROM account_incarnations WHERE user_id=?", u.ID).Scan(&incarnation); err != nil {
		t.Fatal(err)
	}
	tag, err := key.Tag(address)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DB.WithContext(t.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("UPDATE users SET newsletter_minimised=1,email=NULL,email_key=NULL,username=NULL WHERE user_id=?", u.ID).Error; err != nil {
			return err
		}
		return tx.Create(&NewsletterIdentityMatch{UserID: u.ID, Incarnation: incarnation, KeyVersion: key.Version(), AddressTag: tag}).Error
	}); err != nil {
		t.Fatal(err)
	}
	return u
}

func TestNewsletterIdentityPurposeKeyAndCanonicalVector(t *testing.T) {
	root := make([]byte, 32)
	for i := range root {
		root[i] = byte(i)
	}
	key, err := DeriveNewsletterIdentityKey(root)
	if err != nil {
		t.Fatal(err)
	}
	if key.Version() != 1 || key.ID() != "5a3d50aae29d1bc7112761569c779e1d7586ec2bc921ffc2c64ad15fb54b5a1f" {
		t.Fatal("purpose-derived key identity changed")
	}
	tag, err := key.Tag(" Alice@EXAMPLE.TEST ")
	if err != nil || tag != "82b99e9d699502873bddcbe20fb2cc2762f0397520079407ddce76a1f825bb31" {
		t.Fatal("canonical protected-match vector changed", err)
	}
	rate, err := EmailRateBucket(root, "newsletter/email", "alice@example.test")
	if err != nil || tag == rate {
		t.Fatal("identity matching reused the rate-limit domain", err)
	}
	for i := range root {
		root[i] = 0
	}
	again, err := key.Tag("alice@example.test")
	if err != nil || again != tag {
		t.Fatal("key retained the caller's mutable root buffer", err)
	}
	for _, address := range []string{"", "invalid", "Alice <alice@example.test>", "a\n@example.test"} {
		if _, err := key.Tag(address); err == nil {
			t.Fatal("invalid address accepted")
		}
	}
	for _, root := range [][]byte{nil, make([]byte, 31), make([]byte, 33)} {
		if _, err := DeriveNewsletterIdentityKey(root); err == nil {
			t.Fatal("invalid purpose root accepted")
		}
	}
}

func TestNewsletterIdentityExplicitImmutableNamespace(t *testing.T) {
	s := minimisationSchemaStore(t)
	key := newsletterIdentityFixtureKey(t, 0)
	if err := s.ConfigureNewsletterIdentity(t.Context(), key); err == nil {
		t.Fatal("runtime silently initialized its missing namespace")
	}
	var count int
	if err := s.SQL.QueryRowContext(t.Context(), "SELECT count(*) FROM newsletter_identity_namespace").Scan(&count); err != nil || count != 0 {
		t.Fatal("failed configuration wrote a namespace", err)
	}
	if err := s.PrepareNewsletterIdentityNamespace(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	if err := s.PrepareNewsletterIdentityNamespace(t.Context(), key); err != nil {
		t.Fatal("same-key migration retry failed", err)
	}
	if err := s.ConfigureNewsletterIdentity(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ResolveEmailOwner(t.Context(), "fresh@example.test"); err == nil {
		t.Fatal("unfinished backfill/physical maintenance allowed matching")
	}
	minimisationSQL(t, s, "UPDATE newsletter_identity_namespace SET physical_cleanup_pending=0,legacy_vacuum_pending=0")
	other := newsletterIdentityFixtureKey(t, 1)
	if err := s.PrepareNewsletterIdentityNamespace(t.Context(), other); err == nil {
		t.Fatal("migration adopted a replacement key")
	}
	if err := s.ConfigureNewsletterIdentity(t.Context(), other); err == nil {
		t.Fatal("runtime adopted a replacement key")
	}
	var actual string
	if err := s.SQL.QueryRowContext(t.Context(), "SELECT key_id FROM newsletter_identity_namespace").Scan(&actual); err != nil || actual != key.ID() {
		t.Fatal("failed key change altered the namespace", err)
	}
}

func TestNewsletterIdentityResolvesRawAndReducedOwners(t *testing.T) {
	s, key := newsletterIdentityReadyFixture(t)
	reduced := newsletterReducedFixture(t, s, key, "alice@example.test")
	raw := User{Email: "active@example.test", Username: "active", FirstName: "Existing", LastName: "Account", Password: "retained-hash"}
	if err := s.DB.WithContext(t.Context()).Create(&raw).Error; err != nil {
		t.Fatal(err)
	}
	for address, expected := range map[string]int64{" ALICE@EXAMPLE.TEST ": reduced.ID, "ACTIVE@example.test": raw.ID} {
		owner, found, err := s.ResolveEmailOwner(t.Context(), address)
		if err != nil || !found || owner.ID != expected {
			t.Fatal("same canonical owner was not resolved", err)
		}
		if owner.ID == reduced.ID && (!owner.NewsletterMinimised || owner.Email != "" || owner.EmailKey != "" || owner.Username != "") {
			t.Fatal("resolution repopulated a reduced identity")
		}
	}
	if _, found, err := s.ResolveEmailOwner(t.Context(), "new@example.test"); err != nil || found {
		t.Fatal("new address unexpectedly owns an identity", err)
	}
}

func TestNewsletterIdentityCreationGuardUsesItsTransaction(t *testing.T) {
	s, key := newsletterIdentityReadyFixture(t)
	reduced := newsletterReducedFixture(t, s, key, "alice@example.test")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		owner, found, err := s.ResolveEmailOwnerTx(ctx, tx, "ALICE@example.test")
		if err != nil || !found || owner.ID != reduced.ID {
			t.Fatal("transactional owner resolution failed", err)
		}
		novel := User{Email: "rolled-back@example.test", Username: "rolled-back", Password: "hash"}
		if err := tx.Create(&novel).Error; err != nil {
			return err
		}
		duplicate := User{Email: "ALICE@example.test", Username: "duplicate", Password: "hash"}
		return tx.Create(&duplicate).Error
	})
	if !errors.Is(err, sqlite3.CONSTRAINT_UNIQUE) {
		t.Fatal("low-level collision did not preserve normal uniqueness handling", err)
	}
	var count int
	if err := s.SQL.QueryRowContext(t.Context(), "SELECT count(*) FROM users").Scan(&count); err != nil || count != 1 {
		t.Fatal("guard allocated a duplicate identity or lost transaction rollback", err)
	}
}

func TestNewsletterIdentityMissingKeyAndAmbiguityFailClosed(t *testing.T) {
	s, key := newsletterIdentityReadyFixture(t)
	reduced := newsletterReducedFixture(t, s, key, "alice@example.test")
	other, err := Open(s.Path, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	if _, _, err := other.ResolveEmailOwner(t.Context(), "alice@example.test"); err == nil {
		t.Fatal("matching continued with no configured key")
	}
	newUser := User{Email: "fresh@example.test", Username: "fresh", Password: "hash"}
	if err := other.DB.WithContext(t.Context()).Create(&newUser).Error; err == nil {
		t.Fatal("missing key allowed creation in a bound namespace")
	}
	// Deliberately corrupt the fixture with a second clear owner. The shared
	// resolver must refuse, rather than favor one representation or expose IDs.
	minimisationSQL(t, s, `INSERT INTO users(firstname,lastname,email,email_key,password,username)
VALUES('Conflicting','Owner','ALICE@example.test','alice@example.test','hash','conflict')`)
	if _, _, err := s.ResolveEmailOwner(t.Context(), "alice@example.test"); err == nil {
		t.Fatal("inconsistent raw/protected owners were accepted")
	}
	var count int
	if err := s.SQL.QueryRowContext(t.Context(), "SELECT count(*) FROM users WHERE user_id=? AND newsletter_minimised=1", reduced.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("failed resolution changed the reduced owner", err)
	}
}

func TestNewsletterIdentityLostBindingCannotBeAdopted(t *testing.T) {
	s, key := newsletterIdentityReadyFixture(t)
	newsletterReducedFixture(t, s, key, "alice@example.test")
	minimisationSQL(t, s, "UPDATE newsletter_identity_namespace SET key_id=?", strings.Repeat("c", 64))
	if _, _, err := s.ResolveEmailOwner(t.Context(), "alice@example.test"); err == nil {
		t.Fatal("runtime ignored a changed binding")
	}
	minimisationSQL(t, s, "DELETE FROM newsletter_identity_namespace")
	if err := s.PrepareNewsletterIdentityNamespace(t.Context(), key); err == nil {
		t.Fatal("orphaned reduced records silently adopted a key")
	}
	if _, _, err := s.ResolveEmailOwner(t.Context(), "fresh@example.test"); err == nil {
		t.Fatal("missing binding was treated as an ordinary empty namespace")
	}
}

func TestNewsletterIdentityGenericAccountBehaviorIsPreserved(t *testing.T) {
	s := minimisationSchemaStore(t)
	u := User{Email: "normal@example.test", Username: "normal", Password: "hash"}
	if err := s.DB.WithContext(t.Context()).Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	owner, found, err := s.ResolveEmailOwner(t.Context(), "NORMAL@example.test")
	if err != nil || !found || owner.ID != u.ID {
		t.Fatal("ordinary unbound account behavior changed", err)
	}
	duplicate := User{Email: "NORMAL@example.test", Username: "another", Password: "hash"}
	if err := s.DB.WithContext(t.Context()).Create(&duplicate).Error; !errors.Is(err, sqlite3.CONSTRAINT_UNIQUE) {
		t.Fatal("ordinary duplicate handling changed", err)
	}
}

func TestNewsletterIdentityFreshSessionsRetainGuardWithoutQueryState(t *testing.T) {
	s, key := newsletterIdentityReadyFixture(t)
	newsletterReducedFixture(t, s, key, "alice@example.test")
	var sessions int64
	if err := s.DB.Model(&Session{}).Where("hash=?", "absent").Count(&sessions).Error; err != nil || sessions != 0 {
		t.Fatal("unrelated model query failed", err)
	}
	fresh := User{Email: "fresh@example.test", Username: "fresh", Password: "hash"}
	if err := s.DB.Session(&gorm.Session{NewDB: true}).WithContext(t.Context()).Create(&fresh).Error; err != nil {
		t.Fatal("fresh ORM session lost the configured namespace", err)
	}
	duplicate := User{Email: "ALICE@example.test", Username: "duplicate", Password: "hash"}
	if err := s.DB.Session(&gorm.Session{NewDB: true}).WithContext(t.Context()).Create(&duplicate).Error; !errors.Is(err, sqlite3.CONSTRAINT_UNIQUE) {
		t.Fatal("fresh ORM session bypassed the protected collision guard", err)
	}
}
