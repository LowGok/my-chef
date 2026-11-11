package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"time"

	"github.com/LowGok/my-chef/internal/imports"
	"gorm.io/gorm"
)

var (
	ErrRecipeImportNotFound = errors.New("recipe import unavailable")
	ErrRecipeImportConflict = errors.New("this import has already changed; start a new attempt if needed")
	ErrRecipeImportBusy     = errors.New("finish or cancel the current import first")
	ErrRecipeImportStorage  = errors.New("recipe import storage unavailable")
)

type RecipeImportStart struct {
	RequestKey   string
	Source       imports.ImportSource
	ImageDigests []string
}
type RecipeImport struct {
	OwnerInstance string                        `json:"-"`
	ID            string                        `json:"id"`
	Kind          string                        `json:"kind"`
	Stage         string                        `json:"stage"`
	Code          string                        `json:"code,omitempty"`
	ExpiresAt     string                        `json:"expiresAt"`
	CanReadWithAI bool                          `json:"canReadWithAI"`
	Candidates    []imports.StructuredCandidate `json:"candidates"`
	Draft         *imports.Draft                `json:"draft,omitempty"`
	RecipeID      string                        `json:"recipeId,omitempty"`
	VersionID     string                        `json:"versionId,omitempty"`
	Private       imports.PrivateDraft          `json:"-"`
}

func (RecipeImport) String() string   { return "private recipe import" }
func (RecipeImport) GoString() string { return "private recipe import" }

type RecipeImportLease struct {
	ID, Token string
	Dispatch  bool
}

func (RecipeImportLease) String() string   { return "private recipe-import lease" }
func (RecipeImportLease) GoString() string { return "private recipe-import lease" }

type recipeImportOperation struct {
	ID                                                                                                                                string `gorm:"primaryKey"`
	UserID                                                                                                                            int64
	Incarnation, RequestKey, RequestHash, SourceKind, SourceFingerprint, Stage, Code, LeaseHash, RecipeID, VersionID, Provider, Model string
	AIAttempts                                                                                                                        int
	Cipher                                                                                                                            []byte
	CreatedNS, UpdatedNS, ExpiresNS                                                                                                   int64
}

func (recipeImportOperation) TableName() string { return "recipe_import_operations" }
func (r recipeImportOperation) binding() string {
	return r.ID + ":" + strconv.FormatInt(r.UserID, 10) + ":" + r.Incarnation + ":" + strconv.FormatInt(r.ExpiresNS, 10)
}
func importLease(id string) (RecipeImportLease, string, error) {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return RecipeImportLease{}, "", ErrRecipeImportStorage
	}
	encoded := hex.EncodeToString(token[:])
	hash := sha256.Sum256([]byte(encoded))
	return RecipeImportLease{ID: id, Token: encoded, Dispatch: true}, hex.EncodeToString(hash[:]), nil
}
func importLeaseMatches(row recipeImportOperation, lease RecipeImportLease) bool {
	if !lease.Dispatch || row.ID != lease.ID || len(lease.Token) != 64 || len(row.LeaseHash) != 64 {
		return false
	}
	hash := sha256.Sum256([]byte(lease.Token))
	return subtle.ConstantTimeCompare([]byte(row.LeaseHash), []byte(hex.EncodeToString(hash[:]))) == 1
}
func importView(row recipeImportOperation, vault *imports.DraftVault) (RecipeImport, error) {
	out := RecipeImport{OwnerInstance: row.Incarnation, ID: row.ID, Kind: row.SourceKind, Stage: row.Stage, Code: row.Code, ExpiresAt: time.Unix(0, row.ExpiresNS).UTC().Format(time.RFC3339Nano), Candidates: []imports.StructuredCandidate{}, RecipeID: row.RecipeID, VersionID: row.VersionID}
	if len(row.Cipher) == 0 {
		return out, nil
	}
	raw, err := vault.Open(row.binding(), row.Cipher)
	if err != nil {
		return RecipeImport{}, ErrRecipeImportStorage
	}
	defer clear(raw)
	private, err := imports.DecodePrivateDraft(raw)
	if err != nil {
		return RecipeImport{}, ErrRecipeImportStorage
	}
	if private.Source.Kind != row.SourceKind || private.Source.ImportedAt.UnixNano() != row.CreatedNS {
		return RecipeImport{}, ErrRecipeImportStorage
	}
	out.Private = private
	if row.Stage == "ready" {
		out.Draft = private.Draft
	}
	if row.Stage == "selection" && private.Inspection != nil {
		out.Candidates = private.Inspection.Candidates
	}
	out.CanReadWithAI = row.AIAttempts == 0 && (row.Stage == "ready" || row.Stage == "awaiting-ai") && private.Inspection != nil && private.Inspection.FallbackText != ""
	return out, nil
}
func encryptImport(row recipeImportOperation, vault *imports.DraftVault, private imports.PrivateDraft) ([]byte, error) {
	raw, err := private.Encode()
	if err != nil {
		return nil, ErrPersonalInput
	}
	defer clear(raw)
	sealed, err := vault.Seal(row.binding(), raw)
	if err != nil {
		return nil, ErrRecipeImportStorage
	}
	return sealed, nil
}
func loadImport(tx *gorm.DB, actor PersonalActor, incarnation, id string, now time.Time) (recipeImportOperation, error) {
	if !validPersonalID(id) {
		return recipeImportOperation{}, ErrRecipeImportNotFound
	}
	var row recipeImportOperation
	if err := tx.Where("id=? AND user_id=? AND incarnation=?", id, actor.UserID, incarnation).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return row, ErrRecipeImportNotFound
		}
		return row, ErrRecipeImportStorage
	}
	if row.ExpiresNS <= now.UnixNano() {
		return row, ErrRecipeImportNotFound
	}
	return row, nil
}

// LookupRecipeImport returns only a source-bound existing receipt. It never
// creates an operation or authorizes dispatch, even if the worker is unavailable.
func (s *Store) LookupRecipeImport(ctx context.Context, actor PersonalActor, reader PersonalEntitlementReader, vault *imports.DraftVault, input RecipeImportStart, now time.Time) (RecipeImport, error) {
	op, _, err := s.beginRecipeImport(ctx, actor, reader, vault, input, now, true)
	return op, err
}
func (s *Store) BeginRecipeImport(ctx context.Context, actor PersonalActor, reader PersonalEntitlementReader, vault *imports.DraftVault, input RecipeImportStart, now time.Time) (RecipeImport, RecipeImportLease, error) {
	return s.beginRecipeImport(ctx, actor, reader, vault, input, now, false)
}
func (s *Store) beginRecipeImport(ctx context.Context, actor PersonalActor, reader PersonalEntitlementReader, vault *imports.DraftVault, input RecipeImportStart, now time.Time, lookupOnly bool) (RecipeImport, RecipeImportLease, error) {
	if !validPersonalID(input.RequestKey) || now.IsZero() {
		return RecipeImport{}, RecipeImportLease{}, ErrPersonalInput
	}
	input.Source.ImportedAt = now.UTC()
	source, err := imports.CanonicalImportSource(input.Source)
	if err != nil {
		return RecipeImport{}, RecipeImportLease{}, ErrPersonalInput
	}
	if source.Kind == "photos" {
		if len(input.ImageDigests) < 1 || len(input.ImageDigests) > 3 {
			return RecipeImport{}, RecipeImportLease{}, ErrPersonalInput
		}
	} else if len(input.ImageDigests) != 0 {
		return RecipeImport{}, RecipeImportLease{}, ErrPersonalInput
	}
	for _, digest := range input.ImageDigests {
		if !validPersonalDigest(digest) {
			return RecipeImport{}, RecipeImportLease{}, ErrPersonalInput
		}
	}
	fingerprintBody, err := json.Marshal(struct {
		Kind, URL string
		Images    []string
	}{source.Kind, source.URL, input.ImageDigests})
	if err != nil {
		return RecipeImport{}, RecipeImportLease{}, ErrPersonalInput
	}
	fingerprint := vault.Fingerprint("request", fingerprintBody)
	clear(fingerprintBody)
	if fingerprint == "" {
		return RecipeImport{}, RecipeImportLease{}, ErrRecipeImportStorage
	}
	var out RecipeImport
	var lease RecipeImportLease
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		incarnation, e := personalAuthority(tx, actor, now)
		if e != nil {
			return e
		}
		if e = personalPermission(ctx, tx, actor, incarnation, reader, now); e != nil {
			return e
		}
		var existing recipeImportOperation
		e = tx.Where("user_id=? AND incarnation=? AND request_key=?", actor.UserID, incarnation, input.RequestKey).Take(&existing).Error
		if e == nil {
			if existing.RequestHash != fingerprint {
				return ErrRecipeImportConflict
			}
			if existing.ExpiresNS <= now.UnixNano() {
				return ErrRecipeImportNotFound
			}
			out, e = importView(existing, vault)
			return e
		}
		if !errors.Is(e, gorm.ErrRecordNotFound) {
			return ErrRecipeImportStorage
		}
		if lookupOnly {
			return ErrRecipeImportNotFound
		}
		var active, retained int64
		if e = tx.Model(&recipeImportOperation{}).Where("user_id=? AND incarnation=? AND expires_ns>? AND stage IN ('checking','reading')", actor.UserID, incarnation, now.UnixNano()).Count(&active).Error; e != nil {
			return ErrRecipeImportStorage
		}
		if e = tx.Model(&recipeImportOperation{}).Where("user_id=? AND incarnation=? AND cipher IS NOT NULL AND expires_ns>?", actor.UserID, incarnation, now.UnixNano()).Count(&retained).Error; e != nil {
			return ErrRecipeImportStorage
		}
		if active != 0 || retained >= 20 {
			return ErrRecipeImportBusy
		}
		// Stable across app-snapshot restoration; the independent worker can still
		// recognize the same client request even if the app row was rolled back.
		identity := sha256.Sum256([]byte("mychef-recipe-import-v1:" + incarnation + ":" + input.RequestKey))
		id := hex.EncodeToString(identity[:16])
		var leaseHash string
		lease, leaseHash, e = importLease(id)
		if e != nil {
			return e
		}
		domain := source.Kind
		if source.Kind == "url" {
			address, _ := url.Parse(source.URL)
			domain = address.Hostname()
		}
		row := recipeImportOperation{ID: id, UserID: actor.UserID, Incarnation: incarnation, RequestKey: input.RequestKey, RequestHash: fingerprint, SourceKind: source.Kind, SourceFingerprint: vault.Fingerprint("source", []byte(domain)), Stage: "checking", LeaseHash: leaseHash, CreatedNS: now.UnixNano(), UpdatedNS: now.UnixNano(), ExpiresNS: now.Add(24*time.Hour - time.Minute).UnixNano()}
		if source.Kind == "photos" {
			row.Stage = "reading"
			row.AIAttempts = 1
		}
		row.Cipher, e = encryptImport(row, vault, imports.PrivateDraft{Source: source})
		if e != nil {
			return e
		}
		if e = tx.Create(&row).Error; e != nil {
			return ErrRecipeImportStorage
		}
		out, e = importView(row, vault)
		return e
	})
	if err != nil {
		return RecipeImport{}, RecipeImportLease{}, err
	}
	return out, lease, nil
}
func (s *Store) ReadRecipeImport(ctx context.Context, actor PersonalActor, vault *imports.DraftVault, id string, now time.Time) (RecipeImport, error) {
	var out RecipeImport
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		incarnation, err := personalAuthority(tx, actor, now)
		if err != nil {
			return err
		}
		row, err := loadImport(tx, actor, incarnation, id, now)
		if err != nil {
			return err
		}
		out, err = importView(row, vault)
		return err
	})
	return out, err
}
func (s *Store) CompleteRecipeImport(ctx context.Context, actor PersonalActor, vault *imports.DraftVault, lease RecipeImportLease, private imports.PrivateDraft, now time.Time) (RecipeImport, error) {
	var out RecipeImport
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		incarnation, err := personalAuthority(tx, actor, now)
		if err != nil {
			return err
		}
		row, err := loadImport(tx, actor, incarnation, lease.ID, now)
		if err != nil {
			return err
		}
		if (row.Stage != "checking" && row.Stage != "reading") || !importLeaseMatches(row, lease) {
			return ErrRecipeImportConflict
		}
		if private.Source.Kind != row.SourceKind || private.Source.ImportedAt.UnixNano() != row.CreatedNS {
			return ErrPersonalInput
		}
		stage := "failed"
		code := "source-unavailable"
		if private.Draft != nil {
			stage = "ready"
			code = ""
		} else if private.Inspection != nil {
			if len(private.Inspection.Candidates) > 1 {
				stage = "selection"
				code = ""
			} else if len(private.Inspection.Candidates) == 1 {
				copy := private.Inspection.Candidates[0].Draft
				private.Draft = &copy
				stage = "ready"
				code = ""
			} else if private.Inspection.FallbackText != "" {
				stage = "awaiting-ai"
				code = ""
			}
		}
		cipher, err := encryptImport(row, vault, private)
		if err != nil {
			return err
		}
		if stage == "failed" {
			cipher = nil
		}
		if err := tx.Model(&row).Updates(map[string]any{"stage": stage, "code": code, "cipher": cipher, "lease_hash": "", "updated_ns": now.UnixNano()}).Error; err != nil {
			return ErrRecipeImportStorage
		}
		row.Stage, row.Code, row.Cipher, row.LeaseHash = stage, code, cipher, ""
		out, err = importView(row, vault)
		return err
	})
	return out, err
}
func (s *Store) BeginRecipeImportAI(ctx context.Context, actor PersonalActor, reader PersonalEntitlementReader, vault *imports.DraftVault, id string, now time.Time) (RecipeImport, RecipeImportLease, error) {
	var out RecipeImport
	var lease RecipeImportLease
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		incarnation, err := personalAuthority(tx, actor, now)
		if err != nil {
			return err
		}
		if err = personalPermission(ctx, tx, actor, incarnation, reader, now); err != nil {
			return err
		}
		row, err := loadImport(tx, actor, incarnation, id, now)
		if err != nil {
			return err
		}
		view, err := importView(row, vault)
		if err != nil {
			return err
		}
		if !view.CanReadWithAI || row.SourceKind != "url" {
			return ErrRecipeImportConflict
		}
		var hash string
		lease, hash, err = importLease(id)
		if err != nil {
			return err
		}
		if err := tx.Model(&row).Updates(map[string]any{"stage": "reading", "ai_attempts": 1, "lease_hash": hash, "updated_ns": now.UnixNano()}).Error; err != nil {
			return ErrRecipeImportStorage
		}
		row.Stage, row.AIAttempts, row.LeaseHash = "reading", 1, hash
		out, err = importView(row, vault)
		return err
	})
	if err != nil {
		return RecipeImport{}, RecipeImportLease{}, err
	}
	return out, lease, nil
}
func (s *Store) SelectRecipeImport(ctx context.Context, actor PersonalActor, reader PersonalEntitlementReader, vault *imports.DraftVault, id, candidate string, now time.Time) (RecipeImport, error) {
	var out RecipeImport
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		incarnation, err := personalAuthority(tx, actor, now)
		if err != nil {
			return err
		}
		if err = personalPermission(ctx, tx, actor, incarnation, reader, now); err != nil {
			return err
		}
		row, err := loadImport(tx, actor, incarnation, id, now)
		if err != nil {
			return err
		}
		if row.Stage != "selection" {
			return ErrRecipeImportConflict
		}
		view, err := importView(row, vault)
		if err != nil {
			return err
		}
		if view.Private.Inspection == nil {
			return ErrRecipeImportStorage
		}
		draft, err := view.Private.Inspection.Select(candidate)
		if err != nil {
			return err
		}
		private := imports.PrivateDraft{Source: view.Private.Source, Draft: &draft}
		cipher, err := encryptImport(row, vault, private)
		if err != nil {
			return err
		}
		if err := tx.Model(&row).Updates(map[string]any{"stage": "ready", "cipher": cipher, "updated_ns": now.UnixNano()}).Error; err != nil {
			return ErrRecipeImportStorage
		}
		row.Stage, row.Cipher = "ready", cipher
		out, err = importView(row, vault)
		return err
	})
	return out, err
}
func (s *Store) CancelRecipeImport(ctx context.Context, actor PersonalActor, id string, now time.Time) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		incarnation, err := personalAuthority(tx, actor, now)
		if err != nil {
			return err
		}
		row, err := loadImport(tx, actor, incarnation, id, now)
		if err != nil {
			return err
		}
		if row.Stage == "saved" {
			return ErrRecipeImportConflict
		}
		if err := tx.Model(&row).Updates(map[string]any{"stage": "cancelled", "code": "cancelled", "cipher": nil, "lease_hash": "", "updated_ns": now.UnixNano()}).Error; err != nil {
			return ErrRecipeImportStorage
		}
		return nil
	})
}
func (s *Store) FailRecipeImport(ctx context.Context, actor PersonalActor, lease RecipeImportLease, code string, now time.Time) error {
	if code != "unavailable" && code != "source-unavailable" && code != "invalid-source" {
		return ErrPersonalInput
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		incarnation, err := personalAuthority(tx, actor, now)
		if err != nil {
			return err
		}
		row, err := loadImport(tx, actor, incarnation, lease.ID, now)
		if err != nil {
			return err
		}
		if (row.Stage != "checking" && row.Stage != "reading") || !importLeaseMatches(row, lease) {
			return ErrRecipeImportConflict
		}
		if err := tx.Model(&row).Updates(map[string]any{"stage": "failed", "code": code, "cipher": nil, "lease_hash": "", "updated_ns": now.UnixNano()}).Error; err != nil {
			return ErrRecipeImportStorage
		}
		return nil
	})
}
func (s *Store) SweepRecipeImports(ctx context.Context, now time.Time, startup bool) error {
	if now.IsZero() {
		return ErrPersonalInput
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&recipeImportOperation{}).Where("expires_ns<=? AND stage!='saved'", now.UnixNano()).Updates(map[string]any{"stage": "expired", "code": "expired", "cipher": nil, "lease_hash": "", "updated_ns": now.UnixNano()}).Error; err != nil {
			return ErrRecipeImportStorage
		}
		if startup {
			if err := tx.Model(&recipeImportOperation{}).Where("stage IN ('checking','reading')").Updates(map[string]any{"stage": "failed", "code": "interrupted", "cipher": nil, "lease_hash": "", "updated_ns": now.UnixNano()}).Error; err != nil {
				return ErrRecipeImportStorage
			}
		}
		return nil
	})
}

// CheckRecipeImportDispatch rechecks authority immediately before private work.
// Metadata comes from the configured worker, never from a public request.
func (s *Store) CheckRecipeImportDispatch(ctx context.Context, actor PersonalActor, reader PersonalEntitlementReader, vault *imports.DraftVault, lease RecipeImportLease, extracting, fixture bool, now time.Time) (RecipeImport, error) {
	var out RecipeImport
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		incarnation, err := personalAuthority(tx, actor, now)
		if err != nil {
			return err
		}
		if err := personalPermission(ctx, tx, actor, incarnation, reader, now); err != nil {
			return err
		}
		row, err := loadImport(tx, actor, incarnation, lease.ID, now)
		if err != nil {
			return err
		}
		if !importLeaseMatches(row, lease) || extracting && row.Stage != "reading" || !extracting && row.Stage != "checking" {
			return ErrRecipeImportConflict
		}
		if extracting {
			provider := "gemini"
			if fixture {
				provider = "fixture"
			}
			if err := tx.Model(&row).Updates(map[string]any{"provider": provider, "model": imports.RecipeExtractionModel, "updated_ns": now.UnixNano()}).Error; err != nil {
				return ErrRecipeImportStorage
			}
		}
		out, err = importView(row, vault)
		return err
	})
	return out, err
}

// AbandonRecipeImport is an internal cleanup capability, not a public route.
// Its unguessable dispatch lease and exact owner incarnation authorize only
// erasing an in-flight payload. It works after session revocation, never
// inserts a row, and cannot erase a ready/saved result or another owner's data.
func (s *Store) AbandonRecipeImport(ctx context.Context, user int64, incarnation string, lease RecipeImportLease, now time.Time) error {
	token, err := hex.DecodeString(lease.Token)
	if user < 1 || !validPersonalID(incarnation) || !validPersonalID(lease.ID) || !lease.Dispatch || err != nil || len(token) != 32 || hex.EncodeToString(token) != lease.Token || now.IsZero() {
		return ErrPersonalInput
	}
	sum := sha256.Sum256([]byte(lease.Token))
	result := s.DB.WithContext(ctx).Model(&recipeImportOperation{}).Where("id=? AND user_id=? AND incarnation=? AND lease_hash=? AND stage IN ('checking','reading')", lease.ID, user, incarnation, hex.EncodeToString(sum[:])).Updates(map[string]any{"stage": "failed", "code": "unavailable", "cipher": nil, "lease_hash": "", "updated_ns": now.UnixNano()})
	if result.Error != nil {
		return ErrRecipeImportStorage
	}
	return nil
}

// RunRecipeImportExpiry runs even when import dispatch is disabled. Existing
// encrypted drafts must not outlive their retention window after configuration changes.
func (s *Store) RunRecipeImportExpiry(ctx context.Context) error {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	return s.runRecipeImportExpiry(ctx, ticker.C)
}
func (s *Store) runRecipeImportExpiry(ctx context.Context, ticks <-chan time.Time) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case now, ok := <-ticks:
			if !ok {
				return nil
			}
			if err := s.SweepRecipeImports(ctx, now.UTC(), false); err != nil {
				return err
			}
		}
	}
}
