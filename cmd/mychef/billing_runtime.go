package main

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/LowGok/my-chef/internal/config"
	httpapi "github.com/LowGok/my-chef/internal/http"
	"github.com/LowGok/my-chef/internal/store"
)

var errBillingFixture = errors.New("billing fixtures require the fixture build, loopback, and a disposable database")
var fixtureBillingFactory func(context.Context, *store.LifecycleJournal, config.FixtureInbox) (*httpapi.NativeBilling, error)

type billingFixtureMode struct {
	inbox config.FixtureInbox
}

func loadBillingFixtureMode(cfg config.Config, inbox *config.FixtureInbox) (*billingFixtureMode, error) {
	switch os.Getenv("MYCHEF_BILLING_FIXTURE") {
	case "", "0":
		return nil, nil
	case "1":
	default:
		return nil, errBillingFixture
	}
	host, _, addrErr := net.SplitHostPort(cfg.Addr)
	ip, ipErr := netip.ParseAddr(host)
	// The installed emulator and simulator reach the Go service only through
	// the disposable harness's local TLS relay. Do not trust proxy headers.
	installedRelay := cfg.Addr == "127.0.0.1:18137" && cfg.Origin == "https://localhost:8977"
	if fixtureBillingFactory == nil || inbox == nil || addrErr != nil || ipErr != nil || !ip.IsLoopback() ||
		(!installedRelay && cfg.Origin != "http://"+cfg.Addr) || inbox.Origin != cfg.Origin || len(cfg.TrustedProxies) != 0 ||
		cfg.Provider != "fixture" || os.Getenv("MYCHEF_DEV") != "1" || os.Getenv("MYCHEF_E2E") != "1" ||
		inbox.CapabilityHash == ([32]byte{}) || !time.Now().Before(inbox.Expires) ||
		!disposableBillingFiles(os.Getenv("MYCHEF_BILLING_FIXTURE_ROOT"), cfg.Database, cfg.LifecycleDatabase) {
		return nil, errBillingFixture
	}
	return &billingFixtureMode{inbox: *inbox}, nil
}

func disposableBillingFiles(root, db, journal string) bool {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || db != filepath.Join(root, "app.db") ||
		journal != filepath.Join(root, "app.db.lifecycle") {
		return false
	}
	actual, err := filepath.EvalSymlinks(root)
	if err != nil || actual != root {
		return false
	}
	dir, err := os.Stat(root)
	if err != nil || !dir.IsDir() || dir.Mode().Perm() != 0700 {
		return false
	}
	dirOwner, ok := dir.Sys().(*syscall.Stat_t)
	if !ok || int(dirOwner.Uid) != os.Geteuid() {
		return false
	}
	for _, path := range []string{db, journal} {
		file, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue // Startup still requires an explicitly initialized database.
		}
		if err != nil || !file.Mode().IsRegular() || file.Mode().Perm()&0077 != 0 {
			return false
		}
		identity, ok := file.Sys().(*syscall.Stat_t)
		if !ok || identity.Nlink != 1 || int(identity.Uid) != os.Geteuid() {
			return false
		}
	}
	return true
}

func (mode *billingFixtureMode) start(ctx context.Context, journal *store.LifecycleJournal) (*httpapi.NativeBilling, error) {
	if mode == nil {
		return nil, nil
	}
	return fixtureBillingFactory(ctx, journal, mode.inbox)
}

func billingEntitlements(native *httpapi.NativeBilling, fallback store.PersonalEntitlementReader) store.PersonalEntitlementReader {
	if native == nil {
		return fallback
	}
	return native.Entitlements(fallback)
}
