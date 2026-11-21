package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"time"
	_ "time/tzdata"

	"github.com/LowGok/my-chef/internal/domain"
	"gorm.io/gorm"
)

var ErrPlanningTimezone = errors.New("planning requires a confirmed household timezone")

// PlanningPolicy joins planning to the shared server entitlement and a
// confirmed household timezone. Neither comes from an operation's payload.
type PlanningPolicy struct {
	HouseholdZone func(context.Context, *gorm.DB, int64) (string, error)
	Entitlements  PersonalEntitlementReader
	Now           func() time.Time
}

// PlanningStatus gives both app transports the same server calendar.
type PlanningStatus struct {
	Zone      string `json:"zone"`
	Today     string `json:"today,omitempty"`
	FirstWeek string `json:"firstWeek,omitempty"`
	LastWeek  string `json:"lastWeek,omitempty"`
	Premium   bool   `json:"premium"`
}

var planningZoneShape = regexp.MustCompile(`^(?:UTC|[A-Za-z0-9_+-]+(?:/[A-Za-z0-9_+-]+)+)$`)

func planningLocation(zone string) (*time.Location, error) {
	if len(zone) > 100 || !planningZoneShape.MatchString(zone) {
		return nil, ErrPlanningTimezone
	}
	location, err := time.LoadLocation(zone)
	if err != nil {
		return nil, ErrPlanningTimezone
	}
	return location, nil
}

type savedPlanningPreferences struct {
	domain.Preferences
	HouseholdZone string `json:"householdZone,omitempty"`
}

func ConfirmedPlanningZone(_ context.Context, tx *gorm.DB, user int64) (string, error) {
	var owner User
	if err := tx.Select("preferences_json").First(&owner, user).Error; err != nil {
		return "", err
	}
	var saved savedPlanningPreferences
	if owner.PreferencesJSON != nil {
		if err := json.Unmarshal([]byte(*owner.PreferencesJSON), &saved); err != nil {
			return "", nil
		}
	}
	return saved.HouseholdZone, nil
}

func planningStatus(ctx context.Context, tx *gorm.DB, user int64, policy *PlanningPolicy) (PlanningStatus, error) {
	if policy == nil || policy.HouseholdZone == nil {
		return PlanningStatus{}, ErrPlanningTimezone
	}
	zone, err := policy.HouseholdZone(ctx, tx, user)
	if err != nil {
		return PlanningStatus{}, err
	}
	if zone == "" {
		return PlanningStatus{}, nil
	}
	w, err := policy.windowForZone(ctx, tx, user, zone)
	if errors.Is(err, ErrPlanningTimezone) {
		// A previously accepted name can disappear from the platform data.
		// Let the owner confirm a replacement rather than guessing.
		return PlanningStatus{}, nil
	}
	if err != nil {
		return PlanningStatus{}, err
	}
	return PlanningStatus{Zone: zone, Today: w.Today, Premium: w.Premium,
		FirstWeek: w.First.Format(time.DateOnly), LastWeek: w.End.AddDate(0, 0, -7).Format(time.DateOnly)}, nil
}

func (s *Store) PlanningStatus(ctx context.Context, user int64, policy *PlanningPolicy) (PlanningStatus, error) {
	var status PlanningStatus
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		status, err = planningStatus(ctx, tx, user, policy)
		return err
	}, &sql.TxOptions{ReadOnly: true})
	return status, err
}

func (s *Store) ConfirmPlanningZone(ctx context.Context, user int64, sessionHash, zone string, policy *PlanningPolicy) (PlanningStatus, error) {
	if _, err := planningLocation(zone); err != nil {
		return PlanningStatus{}, err
	}
	var status PlanningStatus
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var owner User
		if err := tx.Where("EXISTS (SELECT 1 FROM sessions s WHERE s.user_id=users.user_id AND s.hash=? AND s.expires_unix>unixepoch())", sessionHash).
			First(&owner, user).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrAccountAccess
			}
			return err
		}
		initial, err := json.Marshal(owner.Preferences())
		if err != nil {
			return err
		}
		if err := tx.Model(&owner).Update("preferences_json", gorm.Expr(
			`json_set(CASE WHEN json_type(CASE WHEN json_valid(preferences_json) THEN preferences_json END)='object' THEN preferences_json ELSE ? END,'$.householdZone',?)`, string(initial), zone)).Error; err != nil {
			return err
		}
		status, err = planningStatus(ctx, tx, user, policy)
		return err
	})
	return status, err
}

type PlanningWindow struct {
	First, End time.Time
	Premium    bool
	Today      string
}

func (p *PlanningPolicy) Window(ctx context.Context, tx *gorm.DB, user int64) (PlanningWindow, error) {
	if p == nil {
		return PlanningWindow{}, nil
	}
	if p.HouseholdZone == nil {
		return PlanningWindow{}, ErrPlanningTimezone
	}
	value, err := p.HouseholdZone(ctx, tx, user)
	if err != nil {
		return PlanningWindow{}, err
	}
	return p.windowForZone(ctx, tx, user, value)
}

func (p *PlanningPolicy) windowForZone(ctx context.Context, tx *gorm.DB, user int64, value string) (PlanningWindow, error) {
	zone, err := planningLocation(value)
	if err != nil {
		return PlanningWindow{}, err
	}
	clock := p.Now
	if clock == nil {
		clock = time.Now
	}
	now := clock().In(zone)
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, zone)
	first := day.AddDate(0, 0, -(int(day.Weekday())+6)%7)
	window := PlanningWindow{First: first, End: first.AddDate(0, 0, 7), Today: now.Format(time.DateOnly)}
	if p.Entitlements != nil {
		var incarnation string
		if err := tx.Raw(`SELECT incarnation FROM account_incarnations WHERE user_id=?`, user).Row().Scan(&incarnation); err != nil {
			return PlanningWindow{}, err
		}
		ent, err := p.Entitlements(ctx, tx, user, incarnation)
		if err != nil {
			return PlanningWindow{}, err
		}
		window.Premium = ent.Premium
		if ent.Premium {
			window.End = first.AddDate(0, 0, 35)
		}
	}
	return window, nil
}

func (w PlanningWindow) Enabled() bool { return !w.First.IsZero() }

func (w PlanningWindow) Includes(day string) bool {
	return !w.Enabled() || day >= w.First.Format(time.DateOnly) && day < w.End.Format(time.DateOnly)
}

func (w PlanningWindow) meals(tx *gorm.DB) *gorm.DB {
	if !w.Enabled() {
		return tx
	}
	return tx.Where("creationdate>=? AND creationdate<?", w.First.Format(time.DateOnly), w.End.Format(time.DateOnly))
}

func checkShoppingPlan(ctx context.Context, tx *gorm.DB, user int64, week string, p *PlanningPolicy) error {
	w, err := p.Window(ctx, tx, user)
	if err != nil {
		return err
	}
	if !w.Includes(week) {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// New favorite assignments in the queue prepare a future plan. A retained
// queue can still be read and cleaned up as its week becomes current.
func checkFutureAssignment(ctx context.Context, tx *gorm.DB, user int64, week string, policy *PlanningPolicy) error {
	window, err := policy.Window(ctx, tx, user)
	if err != nil || !window.Enabled() {
		return err
	}
	if !window.Premium || week <= window.First.Format(time.DateOnly) {
		return ErrPlanningTier
	}
	if !window.Includes(week) {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func firstPlanningTier(tiers []*PlanningPolicy) *PlanningPolicy {
	if len(tiers) > 0 {
		return tiers[0]
	}
	return nil
}

func planningRead[T any](ctx context.Context, s *Store, user int64, policy []*PlanningPolicy, read func(*gorm.DB, PlanningWindow) (T, error)) (T, error) {
	if len(policy) == 0 || policy[0] == nil {
		return read(s.DB.WithContext(ctx), PlanningWindow{})
	}
	var result T
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		window, err := policy[0].Window(ctx, tx, user)
		if err != nil {
			return err
		}
		result, err = read(tx, window)
		return err
	}, &sql.TxOptions{ReadOnly: true})
	return result, err
}

func (s *Store) PlanningOperation(ctx context.Context, user, id int64, policy *PlanningPolicy) (domain.Operation, error) {
	return planningRead(ctx, s, user, []*PlanningPolicy{policy}, func(tx *gorm.DB, w PlanningWindow) (domain.Operation, error) {
		var row Generation
		if err := tx.Where("id=? AND user_id=?", id, user).First(&row).Error; err != nil {
			return domain.Operation{}, err
		}
		if w.Enabled() {
			var saved domain.GenerationRequest
			if json.Unmarshal([]byte(row.RequestJSON), &saved) != nil || len(saved.Dates) == 0 {
				return domain.Operation{}, gorm.ErrRecordNotFound
			}
			for _, date := range saved.Dates {
				if !w.Includes(date) {
					return domain.Operation{}, gorm.ErrRecordNotFound
				}
			}
		}
		return row.Operation(), nil
	})
}
