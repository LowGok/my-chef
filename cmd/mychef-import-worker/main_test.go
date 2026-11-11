package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/imports"
)

func TestWorkerCommandHelper(t *testing.T) {
	if os.Getenv("MYCHEF_WORKER_COMMAND_HELPER") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Exit(run(os.Args[i+1:]))
		}
	}
	os.Exit(2)
}
func TestWorkerCommandStartsPrivateFixtureWithoutProviderCredentials(t *testing.T) {
	root, err := filepath.Abs("../../.test-artifacts")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp(root, "wc-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(directory) }()
	key := bytes.Repeat([]byte{47}, 32)
	keyPath := filepath.Join(directory, "key")
	if err := os.WriteFile(keyPath, key, 0600); err != nil {
		t.Fatal(err)
	}
	ledger := filepath.Join(directory, "admission.db")
	now := time.Now().UTC()
	id, err := imports.InitializeAdmissionLedger(ledger, key, now)
	if err != nil {
		t.Fatal(err)
	}
	binding, policy := imports.FixtureExtractionPolicy(now.Add(30 * time.Minute))
	_ = binding
	encoded, _ := json.Marshal(policy)
	policyPath := filepath.Join(directory, "policy.json")
	if err := os.WriteFile(policyPath, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(directory, "import-worker.sock")
	args := []string{"-test.run=^TestWorkerCommandHelper$", "--", "serve", "--provider", "fixture", "--fixture-until", now.Add(30 * time.Minute).Format(time.RFC3339), "--ledger", ledger, "--ledger-id", id, "--ledger-key", keyPath, "--channel-key", keyPath, "--policy", policyPath, "--socket", socket, "--app-uid", strconv.Itoa(os.Geteuid())}
	command := exec.CommandContext(t.Context(), os.Args[0], args...)
	command.Env = []string{"MYCHEF_WORKER_COMMAND_HELPER=1", "MYCHEF_E2E=1", "MYCHEF_CREDENTIALS=/must-not-read/private-provider-canary"}
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = command.Process.Signal(os.Interrupt)
		_ = command.Wait()
		if bytes.Contains(output.Bytes(), []byte("canary")) {
			t.Error("worker exposed credential diagnostics")
		}
	}()
	client, err := imports.NewWorkerClient(socket, key)
	if err != nil {
		t.Fatal(err)
	}
	ready := false
	for range 150 {
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		status, err := client.Ready(ctx)
		cancel()
		if err == nil && status.Ready && status.Fixture && status.AIAvailable {
			ready = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ready {
		t.Fatal("private fixture did not become ready")
	}
	source := imports.ImportSource{Kind: "url", URL: "https://recipes.example.test/carrot-soup", ImportedAt: time.Now().UTC()}
	result, err := client.CheckPage(t.Context(), imports.WorkerRequest{OperationID: strings.Repeat("1", 32), AccountID: strings.Repeat("a", 32), Source: source})
	if err != nil || result.Private.Inspection == nil || len(result.Private.Inspection.Candidates) != 1 {
		t.Fatal("running worker failed fixture import", err)
	}
}
func TestWorkerCommandRefusesUnknownModeAndImplicitInitialization(t *testing.T) {
	if run([]string{"serve", "--provider", "paid-fallback"}) == 0 {
		t.Fatal("unknown provider enabled")
	}
	if run([]string{"serve", "--provider", "fixture"}) == 0 {
		t.Fatal("unconfigured worker enabled")
	}
	if run([]string{"init-ledger"}) == 0 {
		t.Fatal("implicit ledger initialized")
	}
}

func TestWorkerCommandFixturePolicyIsExpiringAndSyntheticOnly(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, tc := range []struct {
		name, enabled string
		until         time.Time
		allowed       bool
	}{
		{"no opt-in", "", now.Add(20 * time.Minute), false},
		{"expired", "1", now.Add(-time.Minute), false},
		{"too long", "1", now.Add(2 * time.Hour), false},
		{"private fixture", "1", now.Add(20 * time.Minute), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command := exec.Command(executable, "-test.run=^TestWorkerCommandHelper$", "--", "fixture-policy", "--fixture-until", tc.until.Format(time.RFC3339))
			command.Env = []string{"MYCHEF_WORKER_COMMAND_HELPER=1", "MYCHEF_E2E=" + tc.enabled}
			raw, err := command.Output()
			if !tc.allowed {
				if err == nil || len(raw) != 0 {
					t.Fatal("unbounded or non-fixture policy was issued")
				}
				return
			}
			var policy imports.ExtractionPolicy
			if err != nil || json.Unmarshal(raw, &policy) != nil {
				t.Fatal("fixture policy unavailable", err)
			}
			binding, _ := imports.FixtureExtractionPolicy(tc.until)
			if policy.Check(time.Now().UTC(), binding) != nil {
				t.Fatal("invalid synthetic policy")
			}
			binding.ProjectID = "real-project"
			if policy.Check(time.Now().UTC(), binding) == nil {
				t.Fatal("fixture policy authorized a different provider project")
			}
		})
	}
}
