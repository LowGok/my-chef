package billing

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// Acknowledge completes a previously committed Play purchase. Its caller
// must check the protected journal and live owner before asking Play to
// finish. Re-read Play's state so a lost acknowledgement response can retry.
func (p *PlayProvider) Acknowledge(parent context.Context, token, binding string) error {
	return p.acknowledge(parent, token, binding, "")
}

// AcknowledgeReplacement applies only to the successor of a staged switch.
func (p *PlayProvider) AcknowledgeReplacement(ctx context.Context, token, binding, predecessor string) error {
	if len(predecessor) != 64 {
		return ErrPurchaseBinding
	}
	return p.acknowledge(ctx, token, binding, predecessor)
}

func (p *PlayProvider) acknowledge(parent context.Context, token, binding, predecessor string) error {
	if !validPlayToken(token) || !accountUUID.MatchString(binding) {
		return ErrPurchaseBinding
	}
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	bearer, err := p.cfg.AccessToken(ctx)
	if err != nil || !validPlayBearer(bearer) {
		return ErrStoreUnavailable
	}
	var current playSubscription
	err = p.get(ctx, "purchases/subscriptionsv2/tokens/"+token, bearer, &current)
	if err != nil {
		return err
	}
	if current.Kind != "androidpublisher#subscriptionPurchaseV2" ||
		(current.Test != nil) != (p.cfg.Environment == Sandbox) || !strings.EqualFold(current.Account.Binding, binding) ||
		current.OutOfApp != nil || len(current.Lines) != 1 ||
		predecessor == "" && current.Linked != "" || predecessor != "" && !matchesPlayPredecessor(current.Linked, token, predecessor) {
		return ErrPurchaseBinding
	}
	offer := current.Lines[0]
	if offer.Product != p.cfg.Product || (offer.Offer.BasePlan != p.cfg.MonthlyBasePlan && offer.Offer.BasePlan != p.cfg.AnnualBasePlan) ||
		offer.Auto == nil || offer.Auto.Installments != nil || offer.Prepaid != nil || offer.Offer.OfferID != "" ||
		offer.Deferred != nil || offer.Removed != nil || !p.validPeriodReplacement(current, predecessor) {
		return ErrPurchaseBinding
	}
	switch current.Acknowledgement {
	case "ACKNOWLEDGEMENT_STATE_ACKNOWLEDGED":
		return nil
	case "ACKNOWLEDGEMENT_STATE_PENDING":
		if current.State != "SUBSCRIPTION_STATE_ACTIVE" && current.State != "SUBSCRIPTION_STATE_CANCELED" {
			return ErrUnresolved
		}
	default:
		return ErrUnresolved
	}
	endpoint := playAPI + p.cfg.Package + "/purchases/subscriptions/" + p.cfg.Product + "/tokens/" + token + ":acknowledge"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader("{}"))
	if err != nil {
		return ErrStoreUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	res, err := p.http.Do(req)
	if err != nil {
		return ErrStoreUnavailable
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusNoContent {
		return ErrStoreUnavailable
	}
	return nil
}
