//go:build mychef_billing_fixture

package main

import (
	"context"
	"time"

	"github.com/LowGok/my-chef/internal/billing"
	"github.com/LowGok/my-chef/internal/config"
	httpapi "github.com/LowGok/my-chef/internal/http"
	"github.com/LowGok/my-chef/internal/store"
)

func init() { fixtureBillingFactory = makeFixtureBilling }

func makeFixtureBilling(ctx context.Context, journal *store.LifecycleJournal, inbox config.FixtureInbox) (*httpapi.NativeBilling, error) {
	active := func() bool { return time.Now().Before(inbox.Expires) }
	if journal == nil || !active() {
		return nil, errBillingFixture
	}
	protected, err := journal.Billing(ctx)
	if err != nil {
		return nil, err
	}
	apple := billing.NewFixtureProvider(inbox.CapabilityHash, billing.Apple, inbox.Expires)
	play := billing.NewFixtureProvider(inbox.CapabilityHash, billing.Play, inbox.Expires)
	offers := []billing.Offer{apple.Offer(billing.Monthly), apple.Offer(billing.Annual), play.Offer(billing.Monthly), play.Offer(billing.Annual)}
	key := billing.FixtureKey(inbox.CapabilityHash, "journal/play")
	i, err := billing.NewPlayInbox(protected, billing.Sandbox, offers, play, key[:])
	clear(key[:])
	if err != nil {
		return nil, err
	}
	return &httpapi.NativeBilling{
		Journal: protected, Environment: billing.Sandbox, CheckoutEnabled: true, PlayCheckoutEnabled: true,
		Offers: offers, Providers: map[billing.Store]billing.Provider{billing.Apple: apple}, Play: i, Available: active,
	}, nil
}
