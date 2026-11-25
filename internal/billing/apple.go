package billing

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"math/big"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var ErrStoreUnavailable = errors.New("billing provider is unavailable")

var (
	appleTransactionID   = regexp.MustCompile(`^[0-9]{1,32}$`)
	appleKeyID           = regexp.MustCompile(`^[A-Z0-9]{10}$`)
	accountUUID          = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	appleCurrency        = regexp.MustCompile(`^[A-Z]{3}$`)
	appleSignedDataOID   = asn1.ObjectIdentifier{1, 2, 840, 113635, 100, 6, 11, 1}
	appleDistributionOID = asn1.ObjectIdentifier{1, 2, 840, 113635, 100, 6, 2, 1}
)

type AppleConfig struct {
	BundleID, GroupID, Issuer, KeyID string
	AppAppleID                       int64 // Required for production notification history.
	Environment                      Environment
	Key                              *ecdsa.PrivateKey
	// Roots are the Apple root certificates provisioned by the server.
	Roots    *x509.CertPool
	Products map[string]Interval
}

type AppleProvider struct {
	cfg    AppleConfig
	http   *http.Client
	origin string
	now    func() time.Time
}

func NewAppleProvider(cfg AppleConfig, client *http.Client, now func() time.Time) (*AppleProvider, error) {
	if cfg.BundleID == "" || cfg.GroupID == "" || !validEnvironment(cfg.Environment) ||
		!accountUUID.MatchString(cfg.Issuer) || !appleKeyID.MatchString(cfg.KeyID) ||
		cfg.Key == nil || cfg.Key.Curve != elliptic.P256() || cfg.Roots == nil || len(cfg.Products) != 2 {
		return nil, ErrPurchaseBinding
	}
	products := make(map[string]Interval, 2)
	intervals := map[Interval]bool{}
	for product, interval := range cfg.Products {
		if product == "" || !validOffer(Apple, cfg.Environment, interval) || intervals[interval] {
			return nil, ErrPurchaseBinding
		}
		products[product] = interval
		intervals[interval] = true
	}
	cfg.Products, cfg.Roots = products, cfg.Roots.Clone()
	if now == nil {
		now = time.Now
	}
	transport := http.DefaultTransport
	if client != nil && client.Transport != nil {
		transport = client.Transport
	}
	httpClient := &http.Client{Transport: transport, Timeout: 8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	origin := "https://api.storekit.apple.com"
	if cfg.Environment == Sandbox {
		origin = "https://api.storekit-sandbox.apple.com"
	}
	return &AppleProvider{cfg: cfg, http: httpClient, origin: origin, now: now}, nil
}

func (p *AppleProvider) bearer(now time.Time) (string, error) {
	// All fields are server configuration. No client proof enters the JWT.
	head, err := json.Marshal(map[string]string{"alg": "ES256", "kid": p.cfg.KeyID, "typ": "JWT"})
	if err != nil {
		return "", ErrStoreUnavailable
	}
	claims, err := json.Marshal(map[string]any{"iss": p.cfg.Issuer, "aud": "appstoreconnect-v1", "bid": p.cfg.BundleID,
		"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix()})
	if err != nil {
		return "", ErrStoreUnavailable
	}
	encode := base64.RawURLEncoding.EncodeToString
	unsigned := encode(head) + "." + encode(claims)
	digest := sha256.Sum256([]byte(unsigned))
	r, s, err := ecdsa.Sign(rand.Reader, p.cfg.Key, digest[:])
	if err != nil {
		return "", ErrStoreUnavailable
	}
	var raw [64]byte
	r.FillBytes(raw[:32])
	s.FillBytes(raw[32:])
	return unsigned + "." + encode(raw[:]), nil
}

type appleTransaction struct {
	BundleID            string         `json:"bundleId"`
	GroupID             string         `json:"subscriptionGroupIdentifier"`
	Product             string         `json:"productId"`
	Transaction         string         `json:"transactionId"`
	OriginalTransaction string         `json:"originalTransactionId"`
	Binding             string         `json:"appAccountToken"`
	Environment         string         `json:"environment"`
	Storefront          string         `json:"storefront"`
	Type                string         `json:"type"`
	Ownership           string         `json:"inAppOwnershipType"`
	BillingPlan         string         `json:"billingPlanType"`
	Commitment          jsontext.Value `json:"commitmentInfo"`
	Currency            string         `json:"currency"`
	Price               int64          `json:"price"`
	OfferType           *int64         `json:"offerType"`
	OfferIdentifier     string         `json:"offerIdentifier"`
	OfferDiscount       string         `json:"offerDiscountType"`
	PurchaseDate        int64          `json:"purchaseDate"`
	ExpiresDate         int64          `json:"expiresDate"`
	SignedDate          int64          `json:"signedDate"`
	RevocationDate      int64          `json:"revocationDate"`
	Upgraded            bool           `json:"isUpgraded"`
}

// Verify accepts only an opaque transaction ID from the native client. It
// fetches the signed facts from Apple's authenticated endpoint rather than
// trusting or storing the client's receipt or claiming that a 404 is terminal.
func (p *AppleProvider) Verify(ctx context.Context, id string) (ProviderProof, error) {
	return p.verify(ctx, id, true)
}

// VerifyExisting allows an existing subscriber's storefront or price to
// change. The journal still has to match the verified lineage to that owner.
func (p *AppleProvider) VerifyExisting(ctx context.Context, id string) (ProviderProof, error) {
	return p.verify(ctx, id, false)
}

func (p *AppleProvider) verify(ctx context.Context, id string, initial bool) (ProviderProof, error) {
	if !appleTransactionID.MatchString(id) {
		return ProviderProof{}, ErrPurchaseBinding
	}
	at := p.now()
	token, err := p.bearer(at)
	if err != nil {
		return ProviderProof{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.origin+"/inApps/v1/transactions/"+id, nil)
	if err != nil {
		return ProviderProof{}, ErrStoreUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	res, err := p.http.Do(req)
	if err != nil {
		return ProviderProof{}, ErrStoreUnavailable
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return ProviderProof{}, ErrStoreUnavailable
	}
	const limit = 32 << 10
	raw, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	var reply struct {
		Transaction string `json:"signedTransactionInfo"`
	}
	if err != nil || len(raw) > limit || json.Unmarshal(raw, &reply) != nil {
		return ProviderProof{}, ErrStoreUnavailable
	}
	tx, err := p.verifyTransaction(reply.Transaction, at)
	if err != nil {
		return ProviderProof{}, err
	}
	if tx.Transaction != id {
		return ProviderProof{}, ErrPurchaseBinding
	}
	return p.normalizeTransaction(tx, initial, at)
}

func (p *AppleProvider) normalizeTransaction(tx appleTransaction, initial bool, at time.Time) (ProviderProof, error) {
	env := "Production"
	if p.cfg.Environment == Sandbox {
		env = "Sandbox"
	}
	interval, ok := p.cfg.Products[tx.Product]
	expectedPrice := int64(6990)
	if interval == Annual {
		expectedPrice = 69900
	}
	revoked := tx.RevocationDate > 0 || tx.Upgraded
	ordinaryPrice := tx.Price > 0 && appleCurrency.MatchString(tx.Currency) && tx.Storefront != "" &&
		tx.OfferType == nil && tx.OfferIdentifier == "" && tx.OfferDiscount == ""
	if !ok || tx.BundleID != p.cfg.BundleID || tx.GroupID != p.cfg.GroupID || !appleTransactionID.MatchString(tx.Transaction) ||
		!appleTransactionID.MatchString(tx.OriginalTransaction) || !accountUUID.MatchString(tx.Binding) ||
		tx.Environment != env || tx.Type != "Auto-Renewable Subscription" || tx.Ownership != "PURCHASED" ||
		(tx.BillingPlan != "" && tx.BillingPlan != "BILLED_UPFRONT") || len(tx.Commitment) != 0 ||
		(initial && (!ordinaryPrice || tx.Currency != "EUR" || tx.Price != expectedPrice)) ||
		(!initial && !revoked && !ordinaryPrice) || tx.PurchaseDate <= 0 || tx.ExpiresDate <= tx.PurchaseDate ||
		tx.ExpiresDate > at.AddDate(2, 0, 0).UnixMilli() || tx.SignedDate < tx.PurchaseDate || tx.SignedDate > at.Add(time.Minute).UnixMilli() {
		return ProviderProof{}, ErrPurchaseBinding
	}
	return ProviderProof{App: tx.BundleID, Product: tx.Product, Region: tx.Storefront, Period: VerifiedPeriod{
		Store: Apple, Environment: p.cfg.Environment, Binding: strings.ToLower(tx.Binding),
		Subscription: tx.OriginalTransaction, Transaction: tx.Transaction, Interval: interval,
		StartsAt: time.UnixMilli(tx.PurchaseDate), PaidThrough: time.UnixMilli(tx.ExpiresDate), ObservedAt: time.UnixMilli(tx.SignedDate),
		Revoked: revoked}}, nil
}

func (p *AppleProvider) verifyTransaction(jws string, at time.Time) (appleTransaction, error) {
	var tx appleTransaction
	payload, err := p.verifySignedPayload(jws, at)
	if err != nil || json.Unmarshal(payload, &tx) != nil {
		return appleTransaction{}, ErrPurchaseBinding
	}
	return tx, nil
}

func (p *AppleProvider) verifySignedPayload(jws string, at time.Time) ([]byte, error) {
	parts := strings.Split(jws, ".")
	if len(parts) != 3 {
		return nil, ErrPurchaseBinding
	}
	decode := base64.RawURLEncoding.Strict().DecodeString
	encodedHead, err := decode(parts[0])
	var head struct {
		Alg   string   `json:"alg"`
		Chain []string `json:"x5c"`
	}
	if err != nil || json.Unmarshal(encodedHead, &head, json.RejectUnknownMembers(true)) != nil ||
		head.Alg != "ES256" || len(head.Chain) != 3 {
		return nil, ErrPurchaseBinding
	}
	// A root accompanying the payload is not a trust anchor. Only the roots
	// independently provisioned for the Apple API can authenticate the signer.
	chain := make([]*x509.Certificate, 3)
	for i, s := range head.Chain {
		der, e := base64.StdEncoding.Strict().DecodeString(s)
		if e != nil {
			return nil, ErrPurchaseBinding
		}
		chain[i], err = x509.ParseCertificate(der)
		if err != nil {
			return nil, ErrPurchaseBinding
		}
	}
	pool := x509.NewCertPool()
	pool.AddCert(chain[1])
	verified, err := chain[0].Verify(x509.VerifyOptions{Roots: p.cfg.Roots, Intermediates: pool, CurrentTime: at,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}})
	if err != nil || len(verified) == 0 || len(verified[0]) != 3 || !chain[2].Equal(verified[0][2]) ||
		chain[0].IsCA || chain[0].KeyUsage&x509.KeyUsageDigitalSignature == 0 ||
		!hasAppleExtension(chain[0], appleSignedDataOID) || !hasAppleExtension(chain[1], appleDistributionOID) {
		return nil, ErrPurchaseBinding
	}
	key, ok := chain[0].PublicKey.(*ecdsa.PublicKey)
	sig, err := decode(parts[2])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ok || key.Curve != elliptic.P256() || err != nil || len(sig) != 64 ||
		!ecdsa.Verify(key, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		return nil, ErrPurchaseBinding
	}
	payload, err := decode(parts[1])
	if err != nil {
		return nil, ErrPurchaseBinding
	}
	return payload, nil
}

func hasAppleExtension(cert *x509.Certificate, oid asn1.ObjectIdentifier) bool {
	for _, e := range cert.Extensions {
		if e.Id.Equal(oid) {
			return true
		}
	}
	return false
}

var _ RestoreProvider = (*AppleProvider)(nil)
