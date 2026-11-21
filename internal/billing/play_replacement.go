package billing

import (
	"context"
	"strings"
	"time"
)

// PlayReplacement describes a verified token switch, not a funded period.
// StoreExpiry must be reconciled with the existing owner's committed period;
// it is never sufficient to extend access.
type PlayReplacement struct {
	Environment                        Environment
	StoreApp, Product, Region          string
	Binding, Previous, Current         string
	PreviousInterval, CurrentInterval  Interval
	StoreExpiry, StartedAt, ObservedAt time.Time
}

// VerifyReplacement obtains the authenticated next-charge-date transition for
// two base plans of this subscription. It cannot bind a token to a MyChef
// account or confirm an order has been paid.
func (p *PlayProvider) VerifyReplacement(parent context.Context, token string) (PlayReplacement, error) {
	if !validPlayToken(token) {
		return PlayReplacement{}, ErrPurchaseBinding
	}
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	bearer, err := p.cfg.AccessToken(ctx)
	if err != nil || !validPlayBearer(bearer) {
		return PlayReplacement{}, ErrStoreUnavailable
	}
	var sub playSubscription
	if err := p.get(ctx, "purchases/subscriptionsv2/tokens/"+token, bearer, &sub); err != nil {
		return PlayReplacement{}, err
	}
	if sub.Kind != "androidpublisher#subscriptionPurchaseV2" || (sub.Test != nil) != (p.cfg.Environment == Sandbox) ||
		!playRegion.MatchString(sub.Region) || !accountUUID.MatchString(sub.Account.Binding) ||
		sub.OutOfApp != nil || !validPlayToken(sub.Linked) || sub.Linked == token || len(sub.Lines) != 1 {
		return PlayReplacement{}, ErrPurchaseBinding
	}
	if sub.State != "SUBSCRIPTION_STATE_ACTIVE" && sub.State != "SUBSCRIPTION_STATE_CANCELED" ||
		sub.Cancellation != nil && sub.Cancellation.Replacement != nil {
		return PlayReplacement{}, ErrUnresolved
	}
	line := sub.Lines[0]
	replacement := line.Replacement
	if line.Product != p.cfg.Product || line.Auto == nil || line.Auto.Installments != nil || line.Prepaid != nil ||
		line.Deferred != nil || line.Removed != nil || line.Promotion != nil || line.Offer.OfferID != "" ||
		replacement == nil || replacement.Product != p.cfg.Product ||
		replacement.Mode != "WITHOUT_PRORATION" || replacement.Offer != "" ||
		!playReplacementPhase(line.Phase) {
		return PlayReplacement{}, ErrPurchaseBinding
	}
	previous, current := p.intervalForPlayPlan(replacement.BasePlan), p.intervalForPlayPlan(line.Offer.BasePlan)
	if previous == "" || current == "" || previous == current {
		return PlayReplacement{}, ErrPurchaseBinding
	}
	observed := p.now().UTC()
	started, startErr := time.Parse(time.RFC3339Nano, sub.Start)
	expiry, expiryErr := time.Parse(time.RFC3339Nano, line.Expiry)
	if startErr != nil || expiryErr != nil || started.After(observed.Add(time.Minute)) ||
		started.Before(observed.AddDate(-2, 0, 0)) || !expiry.After(started) || expiry.After(observed.AddDate(2, 0, 0)) {
		return PlayReplacement{}, ErrPurchaseBinding
	}
	return PlayReplacement{
		Environment: p.cfg.Environment, StoreApp: p.cfg.Package, Product: p.cfg.Product, Region: sub.Region,
		Binding: strings.ToLower(sub.Account.Binding), Previous: playTokenDigest(sub.Linked), Current: playTokenDigest(token),
		PreviousInterval: previous, CurrentInterval: current, StoreExpiry: expiry, StartedAt: started, ObservedAt: observed,
	}, nil
}

// VerifyLinkedPeriod still requires a funded order for this token. Its caller
// must supply a predecessor that was verified and stored before the switch.
func (p *PlayProvider) VerifyLinkedPeriod(ctx context.Context, token, predecessor string) (ProviderProof, error) {
	if len(predecessor) != 64 {
		return ProviderProof{}, ErrPurchaseBinding
	}
	return p.verify(ctx, token, false, predecessor)
}

func matchesPlayPredecessor(linked, token, digest string) bool {
	return validPlayToken(linked) && linked != token && playTokenDigest(linked) == digest
}

func (p *PlayProvider) validPeriodReplacement(sub playSubscription, predecessor string) bool {
	line := sub.Lines[0]
	if line.Replacement == nil {
		return true // Play may omit item-level detail after the verified transition.
	}
	r := line.Replacement
	before, after := p.intervalForPlayPlan(r.BasePlan), p.intervalForPlayPlan(line.Offer.BasePlan)
	return predecessor != "" && r.Product == p.cfg.Product && r.Mode == "WITHOUT_PRORATION" &&
		r.Offer == "" && before != "" && after != "" && before != after
}

func playReplacementPhase(phase map[string]map[string]any) bool {
	if solePlayPhase(phase, "basePrice") {
		return true
	}
	proration := phase["prorationPeriod"]
	return len(phase) == 1 && len(proration) == 1 && proration["originalOfferPhaseType"] == "BASE"
}

func (p *PlayProvider) intervalForPlayPlan(plan string) Interval {
	switch plan {
	case p.cfg.MonthlyBasePlan:
		return Monthly
	case p.cfg.AnnualBasePlan:
		return Annual
	default:
		return ""
	}
}
