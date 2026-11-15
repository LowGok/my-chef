package billing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const playAPI = "https://androidpublisher.googleapis.com/androidpublisher/v3/applications/"

var (
	playPackage  = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*(\.[a-zA-Z][a-zA-Z0-9_]*)+$`)
	playProduct  = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,127}$`)
	playBasePlan = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	playToken    = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	playOrderID  = regexp.MustCompile(`^GPA\.[0-9-]{4,100}(\.\.[0-9]{1,12})?$`)
	playRegion   = regexp.MustCompile(`^[A-Z]{2}$`)
)

type PlayConfig struct {
	Package, Product, MonthlyBasePlan, AnnualBasePlan string
	Environment                                       Environment
	// The deployment supplies an Android Publisher OAuth access token.
	// Never accept URLs, package overrides or credentials from a proof request.
	AccessToken func(context.Context) (string, error)
}

type PlayProvider struct {
	cfg  PlayConfig
	http *http.Client
	now  func() time.Time
}

func NewPlayProvider(cfg PlayConfig, client *http.Client, now func() time.Time) (*PlayProvider, error) {
	if len(cfg.Package) > 150 || !playPackage.MatchString(cfg.Package) ||
		!playProduct.MatchString(cfg.Product) || !playBasePlan.MatchString(cfg.MonthlyBasePlan) ||
		!playBasePlan.MatchString(cfg.AnnualBasePlan) || cfg.MonthlyBasePlan == cfg.AnnualBasePlan ||
		!validEnvironment(cfg.Environment) || cfg.AccessToken == nil {
		return nil, ErrPurchaseBinding
	}
	if now == nil {
		now = time.Now
	}
	transport := http.DefaultTransport
	if client != nil && client.Transport != nil {
		transport = client.Transport
	}
	return &PlayProvider{cfg: cfg, now: now, http: &http.Client{Transport: transport, Timeout: 8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

type playSubscription struct {
	Acknowledgement string    `json:"acknowledgementState"`
	Kind            string    `json:"kind"`
	Region          string    `json:"regionCode"`
	State           string    `json:"subscriptionState"`
	Linked          string    `json:"linkedPurchaseToken"`
	Test            *struct{} `json:"testPurchase"`
	OutOfApp        *struct{} `json:"outOfAppPurchaseContext"`
	Account         struct {
		Binding string `json:"obfuscatedExternalAccountId"`
	} `json:"externalAccountIdentifiers"`
	Start        string `json:"startTime"`
	Cancellation *struct {
		Replacement *struct{} `json:"replacementCancellation"`
	} `json:"canceledStateContext"`

	Lines []struct {
		Product string `json:"productId"`
		Expiry  string `json:"expiryTime"`
		Order   string `json:"latestSuccessfulOrderId"`
		Auto    *struct {
			Enabled      *bool     `json:"autoRenewEnabled"`
			Installments *struct{} `json:"installmentDetails"`
		} `json:"autoRenewingPlan"`
		Prepaid     *struct{} `json:"prepaidPlan"`
		Deferred    *struct{} `json:"deferredItemReplacement"`
		Removed     *struct{} `json:"deferredItemRemoval"`
		Replacement *struct {
			Product  string `json:"productId"`
			BasePlan string `json:"basePlanId"`
			Offer    string `json:"offerId"`
			Mode     string `json:"replacementMode"`
		} `json:"itemReplacement"`
		Promotion *struct{} `json:"signupPromotion"`
		Offer     struct {
			BasePlan string `json:"basePlanId"`
			OfferID  string `json:"offerId"`
		} `json:"offerDetails"`
		Phase map[string]map[string]any `json:"offerPhase"`
	} `json:"lineItems"`
}

type playMoney struct {
	Currency string `json:"currencyCode"`
	Units    string `json:"units"`
	Nanos    int64  `json:"nanos"`
}

func (m playMoney) exactEUR(cents int64) bool {
	units, err := strconv.ParseInt(m.Units, 10, 64)
	return err == nil && m.Currency == "EUR" && units == cents/100 && m.Nanos == (cents%100)*10_000_000
}

type playOrder struct {
	ID    string    `json:"orderId"`
	Token string    `json:"purchaseToken"`
	State string    `json:"state"`
	Total playMoney `json:"total"`
	Items []struct {
		Product      string    `json:"productId"`
		Total        playMoney `json:"total"`
		Subscription *struct {
			Plan    string                    `json:"basePlanId"`
			OfferID string                    `json:"offerId"`
			Start   string                    `json:"servicePeriodStartTime"`
			End     string                    `json:"servicePeriodEndTime"`
			Phase   map[string]map[string]any `json:"offerPhaseDetails"`
		} `json:"subscriptionDetails"`
	} `json:"lineItems"`
}

func solePlayPhase(value map[string]map[string]any, want string) bool {
	phase, ok := value[want]
	return ok && len(value) == 1 && phase != nil && len(phase) == 0
}

func (p *PlayProvider) Verify(ctx context.Context, token string) (ProviderProof, error) {
	return p.verify(ctx, token, true, "")
}

func (p *PlayProvider) VerifyExisting(ctx context.Context, token string) (ProviderProof, error) {
	return p.verify(ctx, token, false, "")
}

func validPlayToken(token string) bool {
	return len(token) <= 2048 && playToken.MatchString(token) && token != "." && token != ".."
}

func playTokenDigest(token string) string {
	hash := sha256.Sum256([]byte("mychef/play/token/v1\x00" + token))
	return hex.EncodeToString(hash[:])
}

func validPlayBearer(bearer string) bool {
	return bearer != "" && len(bearer) <= 8192 && !strings.ContainsAny(bearer, "\r\n\t ")
}

func (p *PlayProvider) verify(ctx context.Context, token string, initial bool, predecessor string) (ProviderProof, error) {
	if !validPlayToken(token) {
		return ProviderProof{}, ErrPurchaseBinding
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	bearer, err := p.cfg.AccessToken(ctx)
	if err != nil || !validPlayBearer(bearer) {
		return ProviderProof{}, ErrStoreUnavailable
	}
	var sub playSubscription
	if err := p.get(ctx, "purchases/subscriptionsv2/tokens/"+token, bearer, &sub); err != nil {
		return ProviderProof{}, err
	}
	if sub.Kind != "androidpublisher#subscriptionPurchaseV2" || (sub.Test != nil) != (p.cfg.Environment == Sandbox) ||
		!playRegion.MatchString(sub.Region) || (initial && sub.Region != "BE") ||
		!accountUUID.MatchString(sub.Account.Binding) || sub.OutOfApp != nil {
		return ProviderProof{}, ErrPurchaseBinding
	}
	if predecessor == "" && sub.Linked != "" {
		return ProviderProof{}, ErrUnresolved
	}
	if predecessor != "" && !matchesPlayPredecessor(sub.Linked, token, predecessor) {
		return ProviderProof{}, ErrPurchaseBinding
	}
	if sub.Cancellation != nil && sub.Cancellation.Replacement != nil {
		// Play may expire an old token when it transfers its remaining paid
		// time to a replacement. Only the linked reconciliation can resolve
		// that transition. The old state alone does not prove revocation.
		return ProviderProof{}, ErrUnresolved
	}
	switch sub.State {
	case "SUBSCRIPTION_STATE_PENDING", "SUBSCRIPTION_STATE_PENDING_PURCHASE_CANCELED":
		return ProviderProof{}, p.signupState(sub, token)
	case "SUBSCRIPTION_STATE_ACTIVE", "SUBSCRIPTION_STATE_CANCELED":
	case "SUBSCRIPTION_STATE_IN_GRACE_PERIOD", "SUBSCRIPTION_STATE_EXPIRED":
		if initial {
			return ProviderProof{}, ErrUnresolved
		}
	default:
		return ProviderProof{}, ErrUnresolved
	}
	if len(sub.Lines) != 1 {
		return ProviderProof{}, ErrPurchaseBinding
	}
	line := sub.Lines[0]
	if line.Product != p.cfg.Product || !playOrderID.MatchString(line.Order) ||
		line.Auto == nil || line.Auto.Installments != nil || line.Prepaid != nil ||
		line.Deferred != nil || line.Removed != nil || !p.validPeriodReplacement(sub, predecessor) ||
		line.Promotion != nil || line.Offer.OfferID != "" || !solePlayPhase(line.Phase, "basePrice") {
		return ProviderProof{}, ErrPurchaseBinding
	}
	interval, price := Monthly, int64(699)
	switch line.Offer.BasePlan {
	case p.cfg.MonthlyBasePlan:
	case p.cfg.AnnualBasePlan:
		interval, price = Annual, 6990
	default:
		return ProviderProof{}, ErrPurchaseBinding
	}
	expiry, err := time.Parse(time.RFC3339Nano, line.Expiry)
	if err != nil {
		return ProviderProof{}, ErrPurchaseBinding
	}
	var order playOrder
	if err := p.get(ctx, "orders/"+line.Order, bearer, &order); err != nil {
		return ProviderProof{}, err
	}
	if order.ID != line.Order || order.Token != token || len(order.Items) != 1 {
		return ProviderProof{}, ErrPurchaseBinding
	}
	switch order.State {
	case "PROCESSED":
	case "PENDING_REFUND", "PARTIALLY_REFUNDED", "REFUNDED":
		if initial {
			return ProviderProof{}, ErrUnresolved
		}
	default:
		return ProviderProof{}, ErrUnresolved
	}
	item := order.Items[0]
	if item.Product != p.cfg.Product || item.Subscription == nil || item.Subscription.Plan != line.Offer.BasePlan ||
		item.Subscription.OfferID != "" || !solePlayPhase(item.Subscription.Phase, "baseDetails") ||
		(initial && (!order.Total.exactEUR(price) || !item.Total.exactEUR(price))) {
		return ProviderProof{}, ErrPurchaseBinding
	}
	from, fromErr := time.Parse(time.RFC3339Nano, item.Subscription.Start)
	through, throughErr := time.Parse(time.RFC3339Nano, item.Subscription.End)
	at := p.now().UTC()
	if fromErr != nil || throughErr != nil || !through.After(from) || from.After(at.Add(time.Minute)) ||
		from.Before(at.AddDate(-2, 0, 0)) || through.After(at.AddDate(2, 0, 0)) {
		return ProviderProof{}, ErrPurchaseBinding
	}
	// Order dates cap the funded period; Play's live subscription can
	// continue reporting active during the automatic payment retry window.
	// Keep immutable order dates for replay; explicit early expiry revokes.
	revoked := sub.State == "SUBSCRIPTION_STATE_EXPIRED" && expiry.Before(through)
	if expiry.Before(through) && !revoked || initial && !through.After(at) {
		return ProviderProof{}, ErrUnresolved
	}
	var renewal *RenewalObservation
	// A retry can extend Play's reported expiry without paying another
	// period. Show the status only when it describes this funded period.
	if line.Auto.Enabled != nil && expiry.Equal(through) && !from.After(at) && through.After(at) {
		switch {
		case sub.State == "SUBSCRIPTION_STATE_ACTIVE" && *line.Auto.Enabled:
			renewal = &RenewalObservation{State: RenewalRenews, ObservedAt: at}
		case sub.State == "SUBSCRIPTION_STATE_CANCELED" && !*line.Auto.Enabled:
			renewal = &RenewalObservation{State: RenewalEnds, ObservedAt: at}
		}
	}
	return ProviderProof{App: p.cfg.Package, Product: p.cfg.Product, BasePlan: line.Offer.BasePlan, Region: sub.Region,
		Period: VerifiedPeriod{Store: Play, Environment: p.cfg.Environment, Binding: strings.ToLower(sub.Account.Binding),
			Subscription: playTokenDigest(token), Transaction: line.Order, Interval: interval,
			StartsAt: from, PaidThrough: through, ObservedAt: at, Revoked: revoked, Renewal: renewal}}, nil
}

func (p *PlayProvider) get(ctx context.Context, path, bearer string, dest any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, playAPI+p.cfg.Package+"/"+path, nil)
	if err != nil {
		return ErrStoreUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/json")
	res, err := p.http.Do(req)
	if err != nil {
		return ErrStoreUnavailable
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return ErrStoreUnavailable
	}
	const limit = 128 << 10
	raw, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil || len(raw) > limit || json.Unmarshal(raw, dest) != nil {
		return ErrStoreUnavailable
	}
	return nil
}
