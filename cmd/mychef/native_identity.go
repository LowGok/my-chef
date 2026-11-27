package main

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/LowGok/my-chef/internal/config"
	"github.com/LowGok/my-chef/internal/store"
)

func nativeIdentityIngress(value string) (bool, error) {
	switch value {
	case "", "0":
		return false, nil
	case "1":
		return true, nil
	default:
		return false, errors.New("MYCHEF_NATIVE_APP_INGRESS must be 0 or 1")
	}
}

// CI may activate only a fresh, explicitly disposable loopback fixture. A
// restart must never reopen an operator-withdrawn persistent policy.
func configureNativeIdentityFixture(ctx context.Context, s *store.Store, cfg config.Config, e2e string, ingress bool, value string) error {
	enabled, err := nativeIdentityIngress(value)
	if err != nil || !enabled {
		return err
	}
	host, _, err := net.SplitHostPort(cfg.Addr)
	if err != nil || cfg.Provider != "fixture" || e2e != "1" || !ingress || (host != "localhost" && !net.ParseIP(host).IsLoopback()) {
		return errors.New("native identity fixture requires explicit loopback E2E mode")
	}
	policy, err := s.NativeIdentityPolicy(ctx)
	if err != nil {
		return err
	}
	if policy.Revision != 0 {
		return nil
	}
	_, err = s.SetNativeIdentityPolicy(ctx, 0, true, 0, time.Now())
	return err
}
