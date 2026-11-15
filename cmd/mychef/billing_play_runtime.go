package main

import (
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"net/http"
	"os"
	"regexp"
	"time"

	"github.com/LowGok/my-chef/internal/billing"
	"github.com/LowGok/my-chef/internal/config"
	httpapi "github.com/LowGok/my-chef/internal/http"
	"github.com/LowGok/my-chef/internal/store"
)

const playPreviewPackage = "com.gkldevelopment.mychef.preview"

var errPlaySandbox = errors.New("invalid private Play billing sandbox configuration")
var scopedSandboxToken = regexp.MustCompile(`^[A-Za-z0-9._~+/=-]+$`)

type playSandboxToken struct {
	Value   string `json:"access_token"`
	Expires int64  `json:"expires_unix"`
}

func (token playSandboxToken) expiry(now time.Time) (time.Time, bool) {
	expires := time.Unix(token.Expires, 0)
	return expires, len(token.Value) >= 24 && len(token.Value) <= 4096 && scopedSandboxToken.MatchString(token.Value) &&
		expires.After(now) && !expires.After(now.Add(time.Hour))
}

func (token playSandboxToken) accessor(deadline time.Time) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			return "", billing.ErrStoreUnavailable
		}
		return token.Value, nil
	}
}

type playSandboxCredential struct {
	Product      string           `json:"product"`
	Monthly      string           `json:"monthly_base_plan"`
	Annual       string           `json:"annual_base_plan"`
	Project      string           `json:"project"`
	Subscription string           `json:"subscription"`
	Publisher    playSandboxToken `json:"publisher"`
	PubSub       playSandboxToken `json:"pubsub"`
	InboxKey     string           `json:"inbox_key"`
}

type playBillingSandbox struct {
	provider billing.PlayConfig
	pull     billing.PlayPullConfig
	inboxKey [32]byte
	expires  time.Time
	checkout bool
}

func loadPlayBillingSandbox(app config.Config, inbox *config.FixtureInbox) (*playBillingSandbox, error) {
	path := os.Getenv("MYCHEF_PLAY_BILLING_SANDBOX_FILE")
	var checkout bool
	switch os.Getenv("MYCHEF_PLAY_BILLING_SANDBOX_CHECKOUT") {
	case "", "0":
	case "1":
		checkout = true
	default:
		return nil, errPlaySandbox
	}
	if path == "" {
		if checkout {
			return nil, errPlaySandbox
		}
		return nil, nil
	}
	now := time.Now()
	if !privateBillingSandbox(app, inbox, now) {
		return nil, errPlaySandbox
	}
	raw, err := readPrivateBillingSandboxCredential(path)
	if err != nil {
		return nil, errPlaySandbox
	}
	defer clear(raw)
	var saved playSandboxCredential
	if err := json.Unmarshal(raw, &saved, json.RejectUnknownMembers(true)); err != nil {
		return nil, errPlaySandbox
	}
	publisherUntil, publisherOK := saved.Publisher.expiry(now)
	pubsubUntil, pubsubOK := saved.PubSub.expiry(now)
	key, err := base64.StdEncoding.Strict().DecodeString(saved.InboxKey)
	defer clear(key)
	if !publisherOK || !pubsubOK || saved.Publisher.Value == saved.PubSub.Value ||
		err != nil || len(key) != 32 || [32]byte(key) == ([32]byte{}) ||
		base64.StdEncoding.EncodeToString(key) != saved.InboxKey {
		return nil, errPlaySandbox
	}
	deadline := inbox.Expires
	for _, at := range []time.Time{publisherUntil, pubsubUntil} {
		if at.Before(deadline) {
			deadline = at
		}
	}
	mode := &playBillingSandbox{
		expires: deadline, checkout: checkout, inboxKey: [32]byte(key),
		provider: billing.PlayConfig{Package: playPreviewPackage, Product: saved.Product, MonthlyBasePlan: saved.Monthly,
			AnnualBasePlan: saved.Annual, Environment: billing.Sandbox, AccessToken: saved.Publisher.accessor(deadline)},
		pull: billing.PlayPullConfig{Package: playPreviewPackage, Project: saved.Project, Subscription: saved.Subscription,
			AccessToken: saved.PubSub.accessor(deadline)},
	}
	if _, err := billing.NewPlayProvider(mode.provider, nil, nil); err != nil {
		return nil, errPlaySandbox
	}
	if _, err := billing.NewPlayPull(mode.pull, nil); err != nil {
		return nil, errPlaySandbox
	}
	return mode, nil
}

func (mode *playBillingSandbox) start(ctx context.Context, journal *store.LifecycleJournal, client *http.Client,
	report func(billing.PlayWorkerHealth)) (*httpapi.NativeBilling, *billing.PlayService, error) {
	available := func() bool { return time.Now().Before(mode.expires) }
	if !available() || journal == nil || mode.provider.Package != playPreviewPackage ||
		mode.pull.Package != playPreviewPackage || mode.provider.Environment != billing.Sandbox {
		return nil, nil, errPlaySandbox
	}
	provider, err := billing.NewPlayProvider(mode.provider, client, nil)
	if err != nil {
		return nil, nil, errPlaySandbox
	}
	pull, err := billing.NewPlayPull(mode.pull, client)
	if err != nil {
		return nil, nil, errPlaySandbox
	}
	protected, err := journal.Billing(ctx)
	if err != nil {
		return nil, nil, err
	}
	offers := []billing.Offer{
		{App: playPreviewPackage, Product: mode.provider.Product, Store: billing.Play,
			BasePlan: mode.provider.MonthlyBasePlan, Interval: billing.Monthly, Environment: billing.Sandbox},
		{App: playPreviewPackage, Product: mode.provider.Product, Store: billing.Play,
			BasePlan: mode.provider.AnnualBasePlan, Interval: billing.Annual, Environment: billing.Sandbox},
	}
	inbox, err := billing.NewPlayInbox(protected, billing.Sandbox, offers, provider, mode.inboxKey[:])
	if err != nil {
		return nil, nil, errPlaySandbox
	}
	service, err := billing.NewPlayService(pull, inbox, journal.WithLiveSandboxBillingOwner, report)
	if err != nil {
		return nil, nil, errPlaySandbox
	}
	return &httpapi.NativeBilling{Journal: protected, Environment: billing.Sandbox, Offers: offers,
		Available: available, SyntheticAccountsOnly: true, CheckoutEnabled: mode.checkout, PlayCheckoutEnabled: mode.checkout,
		Play: inbox}, service, nil
}
