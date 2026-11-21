package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/config"
	"github.com/LowGok/my-chef/internal/store"
)

func TestNativeIdentityIngressRequiresExplicitConfiguration(t *testing.T) {
	for _, value := range []string{"", "0"} {
		enabled, err := nativeIdentityIngress(value)
		if err != nil || enabled {
			t.Fatal("candidate ingress implicitly enabled")
		}
	}
	enabled, err := nativeIdentityIngress("1")
	if err != nil || !enabled {
		t.Fatal("explicit candidate ingress rejected")
	}
	for _, value := range []string{"true", "app", " 1", "2"} {
		if _, err := nativeIdentityIngress(value); err == nil {
			t.Fatal("ambiguous candidate ingress accepted")
		}
	}
}

func TestNativeIdentityFixtureRequiresExplicitLoopbackAndNeverReopensPause(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "fixture.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err = s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Provider: "fixture", Addr: "127.0.0.1:8977"}
	for _, change := range []string{"off", "provider", "e2e", "ingress", "listener"} {
		testCfg := cfg
		e2e, ingress, enabled := "1", true, "1"
		switch change {
		case "off":
			enabled = "0"
		case "provider":
			testCfg.Provider = "gemini"
		case "e2e":
			e2e = "0"
		case "ingress":
			ingress = false
		case "listener":
			testCfg.Addr = "0.0.0.0:8977"
		}
		err := configureNativeIdentityFixture(context.Background(), s, testCfg, e2e, ingress, enabled)
		if change != "off" && err == nil {
			t.Fatal("unsafe fixture startup accepted", change)
		}
		policy, _ := s.NativeIdentityPolicy(context.Background())
		if policy.Active {
			t.Fatal("unsafe startup changed policy")
		}
	}
	if err := configureNativeIdentityFixture(context.Background(), s, cfg, "1", true, "1"); err != nil {
		t.Fatal(err)
	}
	policy, _ := s.NativeIdentityPolicy(context.Background())
	if !policy.Active || policy.Revision != 1 {
		t.Fatal("explicit disposable fixture not activated")
	}
	if _, err := s.SetNativeIdentityPolicy(context.Background(), policy.Revision, false, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := configureNativeIdentityFixture(context.Background(), s, cfg, "1", true, "1"); err != nil {
		t.Fatal(err)
	}
	policy, _ = s.NativeIdentityPolicy(context.Background())
	if policy.Active || policy.Revision != 2 {
		t.Fatal("fixture restart reopened persisted pause")
	}
}
