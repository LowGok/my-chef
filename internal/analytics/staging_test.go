package analytics_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/analytics"
	"github.com/LowGok/my-chef/internal/store"
)

func TestAppStagingCrashIsReclaimedBeforeAccess(t *testing.T) {
	f := setup(t)
	root := filepath.Dir(f.opts.Root)
	keyPath := filepath.Join(root, "key.bin")
	if e := os.WriteFile(keyPath, f.opts.BackupKey, 0600); e != nil {
		t.Fatal(e)
	}
	configPath := filepath.Join(root, "runtime.json")
	b, _ := json.Marshal(analytics.RuntimeFile{Root: f.opts.Root, PrivacyPath: f.opts.PrivacyPath, AnchorPath: f.opts.AnchorPath, BackupKeyFile: keyPath, Collection: f.opts.Config})
	if e := os.WriteFile(configPath, b, 0600); e != nil {
		t.Fatal(e)
	}
	binary, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	command := exec.Command(binary, "-test.run=^TestAnalyticsStagingCrashHelper$")
	command.Env = append(os.Environ(), "MYCHEF_ANALYTICS_STAGE_HELPER=1", "MYCHEF_ANALYTICS_STAGE_CONFIG="+configPath, "MYCHEF_ANALYTICS_STAGE_APP="+f.app.Path, "MYCHEF_ANALYTICS_STAGE_NOW="+strconv.FormatInt(f.now.Unix(), 10))
	if e = command.Run(); e == nil {
		t.Fatal("crash helper returned successfully")
	}
	files, e := filepath.Glob(filepath.Join(f.opts.Root, "stg_*.sqlite"))
	if e != nil || len(files) != 1 {
		t.Fatal("raw stage was not retained by the abrupt exit", e)
	}
	b, e = os.ReadFile(files[0])
	if e != nil || !bytes.Contains(b, []byte("fictional-raw-staging-canary")) {
		t.Fatal("staging fault not exercised", e)
	}
	if e = f.s.Maintain(context.Background()); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(files[0]); !os.IsNotExist(e) {
		t.Fatal("interrupted raw staging survived maintenance")
	}
}
func TestAnalyticsStagingCrashHelper(t *testing.T) {
	if os.Getenv("MYCHEF_ANALYTICS_STAGE_HELPER") != "1" {
		return
	}
	options, e := analytics.LoadRuntime(os.Getenv("MYCHEF_ANALYTICS_STAGE_CONFIG"))
	if e != nil {
		os.Exit(10)
	}
	at, e := strconv.ParseInt(os.Getenv("MYCHEF_ANALYTICS_STAGE_NOW"), 10, 64)
	if e != nil {
		os.Exit(11)
	}
	options.Now = func() time.Time { return time.Unix(at, 0) }
	app, e := store.Open(os.Getenv("MYCHEF_ANALYTICS_STAGE_APP"), false)
	if e != nil {
		os.Exit(12)
	}
	service, e := analytics.Open(context.Background(), app.DB, options)
	if e != nil {
		os.Exit(13)
	}
	_ = service.WithAppStaging(context.Background(), func(path string) error {
		if e := os.WriteFile(path, []byte("fictional-raw-staging-canary"), 0600); e != nil {
			os.Exit(14)
		}
		os.Exit(42)
		return nil
	})
	os.Exit(15)
}
