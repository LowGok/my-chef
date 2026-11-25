package billing

import (
	"context"
	"time"
)

type playCatalog struct {
	Package  string            `json:"packageName"`
	Product  string            `json:"productId"`
	Archived bool              `json:"archived"`
	Plans    []playCatalogPlan `json:"basePlans"`
}

type playCatalogPlan struct {
	ID      string `json:"basePlanId"`
	State   string `json:"state"`
	Regions []struct {
		Code      string    `json:"regionCode"`
		Available bool      `json:"newSubscriberAvailability"`
		Price     playMoney `json:"price"`
	} `json:"regionalConfigs"`
	Other *struct {
		Available bool `json:"newSubscriberAvailability"`
	} `json:"otherRegionsConfig"`
	Auto *struct {
		Period      string `json:"billingPeriodDuration"`
		Grace       string `json:"gracePeriodDuration"`
		Proration   string `json:"prorationMode"`
		LegacyOffer string `json:"legacyCompatibleSubscriptionOfferId"`
	} `json:"autoRenewingBasePlanType"`
	Prepaid      *struct{} `json:"prepaidBasePlanType"`
	Installments *struct{} `json:"installmentsBasePlanType"`
}

type playCatalogOffers struct {
	Offers []struct {
		Package string `json:"packageName"`
		Product string `json:"productId"`
		Plan    string `json:"basePlanId"`
		ID      string `json:"offerId"`
		State   string `json:"state"`
	} `json:"subscriptionOffers"`
	Next string `json:"nextPageToken"`
}

func (plan playCatalogPlan) matchesBelgianTerm(duration string, cents int64) bool {
	if plan.Auto == nil || plan.Prepaid != nil || plan.Installments != nil ||
		plan.Auto.Period != duration || plan.Auto.Grace != "P0D" || plan.Auto.LegacyOffer != "" ||
		(plan.Other != nil && plan.Other.Available) {
		return false
	}
	// An unset mode inherits Play's documented next-billing-date default.
	switch plan.Auto.Proration {
	case "", "SUBSCRIPTION_PRORATION_MODE_UNSPECIFIED", "SUBSCRIPTION_PRORATION_MODE_CHARGE_ON_NEXT_BILLING_DATE":
	default:
		return false
	}
	regions := make(map[string]bool)
	for _, region := range plan.Regions {
		if !playRegion.MatchString(region.Code) || regions[region.Code] {
			return false
		}
		regions[region.Code] = true
		if region.Available != (region.Code == "BE") || (region.Code == "BE" && !region.Price.exactEUR(cents)) {
			return false
		}
	}
	return regions["BE"]
}

// CheckSandboxCheckoutCatalog is a bounded read of the exact configured
// Preview product. It is an additional gate for a licensed Sandbox run, not
// evidence of buyer tax, a paid-through period or production eligibility.
func (p *PlayProvider) CheckSandboxCheckoutCatalog(ctx context.Context) error {
	if p == nil || p.cfg.Environment != Sandbox {
		return ErrPurchaseBinding
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	bearer, err := p.cfg.AccessToken(ctx)
	if err != nil || !validPlayBearer(bearer) {
		return ErrStoreUnavailable
	}
	productPath := "subscriptions/" + p.cfg.Product
	var catalog playCatalog
	if err := p.get(ctx, productPath, bearer, &catalog); err != nil {
		return err
	}
	if catalog.Package != p.cfg.Package || catalog.Product != p.cfg.Product || catalog.Archived {
		return ErrPurchaseBinding
	}
	plans := make(map[string]bool)
	active := 0
	for _, plan := range catalog.Plans {
		if !playBasePlan.MatchString(plan.ID) || plans[plan.ID] {
			return ErrPurchaseBinding
		}
		plans[plan.ID] = true
		switch plan.State {
		case "INACTIVE", "DRAFT":
			continue
		case "ACTIVE":
		default:
			return ErrPurchaseBinding
		}
		if plan.ID == p.cfg.MonthlyBasePlan {
			if !plan.matchesBelgianTerm("P1M", playMonthlyEUR) {
				return ErrPurchaseBinding
			}
		} else if plan.ID != p.cfg.AnnualBasePlan || !plan.matchesBelgianTerm("P1Y", playAnnualEUR) {
			return ErrPurchaseBinding
		}
		active++
	}
	if active != 2 {
		return ErrPurchaseBinding
	}
	// Google permits "-" to list the specified product's offers across all
	// base plans. Fail closed on partial results rather than infer no trial.
	var offers *playCatalogOffers
	if err := p.get(ctx, productPath+"/basePlans/-/offers?pageSize=100", bearer, &offers); err != nil {
		return err
	}
	if offers == nil || offers.Next != "" {
		return ErrStoreUnavailable
	}
	for _, offer := range offers.Offers {
		if offer.Package != p.cfg.Package || offer.Product != p.cfg.Product || !plans[offer.Plan] ||
			!playBasePlan.MatchString(offer.ID) || (offer.State != "DRAFT" && offer.State != "INACTIVE") {
			return ErrPurchaseBinding
		}
	}
	return nil
}
