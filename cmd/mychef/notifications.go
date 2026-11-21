package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/LowGok/my-chef/internal/notifications"
	"github.com/LowGok/my-chef/internal/store"
)

type pushConfig struct {
	StorageKey string
	Apple      *notifications.ApplePush
	Google     *notifications.GooglePush
	App        *pushIdentityConfig
}

type pushIdentityConfig struct {
	Apple  *notifications.ApplePush
	Google *notifications.GooglePush
}

func selectPushSender(lane string, preview, app *notifications.PushSender) *notifications.PushSender {
	switch lane {
	case "", "preview":
		return preview
	case "app":
		return app
	default:
		return nil
	}
}

func configurePush(s *store.Store, read store.PersonalEntitlementReader, fixture bool) (*store.ReminderSettings, notifications.Send, error) {
	path := os.Getenv("MYCHEF_PUSH_CONFIG_FILE")
	if path == "" {
		if fixture && os.Getenv("MYCHEF_PUSH_FIXTURE") == "1" {
			key := make([]byte, 32)
			if _, err := rand.Read(key); err != nil {
				return nil, nil, err
			}
			defer clear(key)
			settings, err := store.NewReminderSettings(s, key, read, nil)
			return settings, func(context.Context, notifications.Delivery) notifications.Result {
				return notifications.Result{State: notifications.Accepted}
			}, err
		}
		return nil, nil, nil
	}
	if fixture {
		return nil, nil, errors.New("live push is disabled in fixture mode")
	}
	invalid := errors.New("invalid private push configuration")
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, invalid
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 64<<10 {
		return nil, nil, invalid
	}
	var cfg pushConfig
	d := json.NewDecoder(io.LimitReader(f, 64<<10))
	d.DisallowUnknownFields()
	if d.Decode(&cfg) != nil || d.Decode(new(any)) != io.EOF {
		return nil, nil, invalid
	}
	key, err := base64.StdEncoding.Strict().DecodeString(cfg.StorageKey)
	if err != nil {
		return nil, nil, invalid
	}
	defer clear(key)
	sender, err := notifications.NewIdentityPushSender("preview", cfg.Apple, cfg.Google)
	if err != nil {
		return nil, nil, invalid
	}
	var appSender *notifications.PushSender
	if cfg.App != nil {
		appSender, err = notifications.NewIdentityPushSender("app", cfg.App.Apple, cfg.App.Google)
		if err != nil {
			return nil, nil, invalid
		}
	}
	settings, err := store.NewReminderSettings(s, key, read, nil)
	if err != nil {
		return nil, nil, invalid
	}
	if err := settings.SetIdentityProviderRoute("preview", sender.Environment(), sender.Supports); err != nil {
		return nil, nil, invalid
	}
	if appSender != nil {
		if err := settings.SetIdentityProviderRoute("app", appSender.Environment(), appSender.Supports); err != nil {
			return nil, nil, invalid
		}
	}
	return settings, func(ctx context.Context, d notifications.Delivery) notifications.Result {
		selected := selectPushSender(d.IdentityLane, sender, appSender)
		if selected == nil {
			return notifications.Result{State: notifications.Permanent}
		}
		return selected.Send(ctx, d)
	}, nil
}

func runPushWorker(ctx context.Context, settings *store.ReminderSettings, send notifications.Send, logger *slog.Logger) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	nextHealth := time.Now()
	for {
		for range 16 {
			if ctx.Err() != nil {
				return
			}
			sent, err := settings.RunReminderStep(ctx, send)
			if err != nil {
				if ctx.Err() == nil {
					logger.Warn("planning reminder worker step failed")
				}
				break
			}
			if !sent {
				break
			}
		}
		if now := time.Now(); !now.Before(nextHealth) && ctx.Err() == nil {
			nextHealth = now.Add(5 * time.Minute)
			health, err := settings.ReminderHealth(ctx)
			if err != nil {
				logger.Warn("planning reminder queue health unavailable")
			} else {
				logger.Info("planning reminder queue", "queued", health.Queued, "retrying", health.Retrying,
					"provider_accepted", health.Accepted, "uncertain", health.Uncertain,
					"terminal_failure", health.Rejected, "expired", health.Expired,
					"unavailable_zone_evaluations", health.ZoneFailures, "oldest_due_seconds", health.OldestDueSeconds)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
