//go:build mychef_billing_fixture

package billing

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json/v2"
	"strings"
	"time"
)

// FixturePurchase is only available in the loopback fixture build. These
// receipts are signed for that process and have no store or payment authority.
type FixturePurchase struct {
	Binding               string
	Order                 string
	Interval              Interval
	StartsAt, PaidThrough time.Time
}

type FixtureProvider struct {
	key      [32]byte
	store    Store
	deadline time.Time
}

func NewFixtureProvider(seed [32]byte, store Store, deadline time.Time) *FixtureProvider {
	return &FixtureProvider{key: FixtureKey(seed, "proof/"+string(store)), store: store, deadline: deadline}
}

func FixtureKey(seed [32]byte, label string) [32]byte {
	mac := hmac.New(sha256.New, seed[:])
	_, _ = mac.Write([]byte("mychef/billing/fixture/v1\x00" + label))
	return [32]byte(mac.Sum(nil))
}

func (p *FixtureProvider) Offer(interval Interval) Offer {
	product, plan := "fixture.billing."+string(interval), ""
	if p.store == Play {
		product, plan = "fixture.billing.premium", string(interval)
	}
	return Offer{Store: p.store, Environment: Sandbox, App: "test.invalid.mychef.fixture", Product: product, BasePlan: plan, Interval: interval}
}

func (p *FixtureProvider) Mint(purchase FixturePurchase) (string, error) {
	body, err := json.Marshal(purchase)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, p.key[:])
	_, _ = mac.Write(body)
	return "fixture." + base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (p *FixtureProvider) Verify(_ context.Context, token string) (ProviderProof, error) {
	now := time.Now().UTC()
	if !now.Before(p.deadline) || len(token) > 2048 || !strings.HasPrefix(token, "fixture.") {
		return ProviderProof{}, ErrPurchaseBinding
	}
	raw, tag, ok := strings.Cut(strings.TrimPrefix(token, "fixture."), ".")
	body, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	sig, badTag := base64.RawURLEncoding.Strict().DecodeString(tag)
	mac := hmac.New(sha256.New, p.key[:])
	_, _ = mac.Write(body)
	if !ok || err != nil || badTag != nil || !hmac.Equal(sig, mac.Sum(nil)) {
		return ProviderProof{}, ErrPurchaseBinding
	}
	var purchase FixturePurchase
	if json.Unmarshal(body, &purchase) != nil || !accountUUID.MatchString(purchase.Binding) ||
		!validOffer(p.store, Sandbox, purchase.Interval) || !accountUUID.MatchString(purchase.Order) ||
		purchase.StartsAt.IsZero() || purchase.StartsAt.After(now) || !purchase.PaidThrough.After(purchase.StartsAt) ||
		!purchase.PaidThrough.After(now) || purchase.PaidThrough.After(p.deadline) {
		return ProviderProof{}, ErrPurchaseBinding
	}
	offer := p.Offer(purchase.Interval)
	region, lineage := "BEL", purchase.Order
	if p.store == Play {
		region, lineage = "BE", playTokenDigest(token)
	}
	return ProviderProof{App: offer.App, Product: offer.Product, BasePlan: offer.BasePlan, Region: region,
		Period: VerifiedPeriod{Store: p.store, Environment: Sandbox, Binding: purchase.Binding, Subscription: lineage,
			Transaction: purchase.Order, Interval: purchase.Interval, StartsAt: purchase.StartsAt, PaidThrough: purchase.PaidThrough, ObservedAt: now,
			Renewal: &RenewalObservation{State: RenewalRenews, ObservedAt: now}}}, nil
}

func (p *FixtureProvider) VerifyExisting(ctx context.Context, token string) (ProviderProof, error) {
	return p.Verify(ctx, token)
}
