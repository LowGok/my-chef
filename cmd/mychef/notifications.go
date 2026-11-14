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
	sender, err := notifications.NewPushSender(cfg.Apple, cfg.Google)
	if err != nil {
		return nil, nil, invalid
	}
	settings, err := store.NewReminderSettings(s, key, read, nil)
	if err != nil {
		return nil, nil, invalid
	}
	settings.SetProviderAvailability(sender.Supports)
	return settings, sender.Send, nil
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
