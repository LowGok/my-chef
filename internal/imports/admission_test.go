package imports

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func admissionRequest(n int) AdmissionRequest {
	return AdmissionRequest{OperationID: fmt.Sprintf("%032x", n), AccountID: strings.Repeat("a", 32), SourceGroup: "recipes.example.com", RequestHash: strings.Repeat("b", 64)}
}
func admissionFixture(t *testing.T) (*AdmissionLedger, string, []byte, string, ExtractionPolicy, ExtractionBinding, time.Time) {
	t.Helper()
	p, b, now := policyFixture()
	directory := filepath.Join(t.TempDir(), "worker")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "admission.db")
	key := bytes.Repeat([]byte{91}, 32)
	id, err := InitializeAdmissionLedger(path, key, now)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := OpenAdmissionLedger(path, key, id, now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	return ledger, path, key, id, p, b, now
}
func TestAdmissionLedgerAtomicBurstReplayAndPrivacy(t *testing.T) {
	ledger, path, _, _, policy, binding, now := admissionFixture(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := []AdmissionReservation{}
	for n := 1; n <= 12; n++ {
		wg.Go(func() {
			reservation, err := ledger.Admit(t.Context(), policy, binding, admissionRequest(n), now)
			if err == nil && reservation.Dispatch {
				mu.Lock()
				accepted = append(accepted, reservation)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if len(accepted) != 2 {
		t.Fatal("fleet token/concurrency/call caps not atomic", len(accepted))
	}
	for _, reservation := range accepted {
		if err := ledger.Finish(t.Context(), reservation, "completed", now); err != nil {
			t.Fatal(err)
		}
		again, err := ledger.Admit(t.Context(), policy, binding, reservation.request, now)
		if err != nil || again.Dispatch {
			t.Fatal("same operation dispatched again", err)
		}
		changed := reservation.request
		changed.RequestHash = strings.Repeat("c", 64)
		if _, err := ledger.Admit(t.Context(), policy, binding, changed, now); err == nil {
			t.Fatal("same operation accepted different input")
		}
	}
	for _, file := range []string{path, path + "-wal"} {
		data, err := os.ReadFile(file)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		for _, private := range []string{"recipes.example.com", strings.Repeat("a", 32), strings.Repeat("b", 64)} {
			if bytes.Contains(data, []byte(private)) {
				t.Fatal("ledger retained recoverable request data")
			}
		}
	}
}
func TestAdmissionLedgerBudgetsSurviveFinishRestartAndAppRestore(t *testing.T) {
	ledger, path, key, id, policy, binding, now := admissionFixture(t)
	policy.Limits.AccountCallsPerDay = 1
	policy.Limits.SourceCallsPerDay = 1
	policy.Limits.FleetCallsPerDay = 3
	first := admissionRequest(1)
	reservation, err := ledger.Admit(t.Context(), policy, binding, first, now)
	if err != nil || !reservation.Dispatch {
		t.Fatal(err)
	}
	if err := ledger.Finish(t.Context(), reservation, "ambiguous", now); err != nil {
		t.Fatal(err)
	}
	sameSource := admissionRequest(2)
	sameSource.AccountID = strings.Repeat("d", 32)
	if _, err := ledger.Admit(t.Context(), policy, binding, sameSource, now); err == nil {
		t.Fatal("source daily cap bypassed")
	}
	sameAccount := admissionRequest(3)
	sameAccount.SourceGroup = "another.example.com"
	if _, err := ledger.Admit(t.Context(), policy, binding, sameAccount, now); err == nil {
		t.Fatal("account daily cap bypassed")
	}
	second := sameSource
	second.SourceGroup = "another.example.com"
	if _, err := ledger.Admit(t.Context(), policy, binding, second, now); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}
	ledger, err = OpenAdmissionLedger(path, key, id, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ledger.Close() }()
	// An old app snapshot can submit the same stable operation, but owns no
	// path or management API capable of resetting this independent database.
	replay, err := ledger.Admit(t.Context(), policy, binding, first, now.Add(time.Minute))
	if err != nil || replay.Dispatch {
		t.Fatal("app restoration replayed inference", err)
	}
	third := admissionRequest(4)
	third.AccountID = strings.Repeat("e", 32)
	third.SourceGroup = "third.example.com"
	last, err := ledger.Admit(t.Context(), policy, binding, third, now.Add(61*time.Second))
	if err != nil || !last.Dispatch {
		t.Fatal("restart lost usable capacity", err)
	}
	if err := ledger.Finish(t.Context(), last, "failed", now.Add(61*time.Second)); err != nil {
		t.Fatal(err)
	}
	fourth := admissionRequest(5)
	fourth.AccountID = strings.Repeat("f", 32)
	fourth.SourceGroup = "fourth.example.com"
	if _, err := ledger.Admit(t.Context(), policy, binding, fourth, now.Add(62*time.Second)); err == nil {
		t.Fatal("fleet daily budget reset/refunded")
	}
}
func TestAdmissionLedgerFailsClosedAndReservesMaximumTokens(t *testing.T) {
	ledger, path, key, id, policy, binding, now := admissionFixture(t)
	policy.Limits.TokensPerMinute = policy.Limits.MaxInputTokens + policy.Limits.MaxOutputTokens + policy.Limits.MaxThinkingTokens
	one, err := ledger.Admit(t.Context(), policy, binding, admissionRequest(1), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Finish(t.Context(), one, "failed", now); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Admit(t.Context(), policy, binding, admissionRequest(2), now); err == nil {
		t.Fatal("failed call refunded maximum token reservation")
	}
	disabled := policy
	disabled.Enabled = false
	if _, err := ledger.Admit(t.Context(), disabled, binding, admissionRequest(3), now.Add(time.Minute)); err == nil {
		t.Fatal("kill control bypassed")
	}
	if _, err := ledger.Admit(t.Context(), policy, binding, admissionRequest(4), now.Add(-time.Minute)); err == nil {
		t.Fatal("clock rollback reset windows")
	}
	if _, err := OpenAdmissionLedger(path, key, id, now); err == nil {
		t.Fatal("second worker took an active ledger")
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenAdmissionLedger(path, bytes.Repeat([]byte{92}, 32), id, now); err == nil {
		t.Fatal("unknown ledger key accepted")
	}
	if _, err := OpenAdmissionLedger(path, key, strings.Repeat("0", 32), now); err == nil {
		t.Fatal("unknown ledger identity accepted")
	}
	if _, err := InitializeAdmissionLedger(path, key, now); err == nil {
		t.Fatal("existing ledger reinitialized")
	}
	if _, err := OpenAdmissionLedger(filepath.Join(t.TempDir(), "missing.db"), key, id, now); err == nil {
		t.Fatal("missing ledger auto-created")
	}
}
func TestAdmissionLedgerPrivateFetchHasDurableSeparateIntent(t *testing.T) {
	ledger, _, _, _, _, _, now := admissionFixture(t)
	request := admissionRequest(1)
	first, err := ledger.AdmitFetch(t.Context(), request, now)
	if err != nil || !first.Dispatch {
		t.Fatal(err)
	}
	if err := ledger.Finish(t.Context(), first, "completed", now); err != nil {
		t.Fatal(err)
	}
	again, err := ledger.AdmitFetch(t.Context(), request, now)
	if err != nil || again.Dispatch {
		t.Fatal("fetch automatically replayed", err)
	}
}
func TestAdmissionLedgerProcessHelper(t *testing.T) {
	if os.Getenv("MYCHEF_ADMISSION_HELPER") != "1" {
		return
	}
	var input struct {
		Path, ID string
		Key      []byte
		Now      time.Time
	}
	if json.NewDecoder(os.Stdin).Decode(&input) != nil {
		os.Exit(2)
	}
	ledger, err := OpenAdmissionLedger(input.Path, input.Key, input.ID, input.Now)
	if err != nil {
		fmt.Print("locked")
		os.Exit(0)
	}
	policy, binding, _ := policyFixture()
	replay, err := ledger.Admit(t.Context(), policy, binding, admissionRequest(1), input.Now)
	if err != nil || replay.Dispatch {
		os.Exit(3)
	}
	if err := ledger.Close(); err != nil {
		os.Exit(4)
	}
	fmt.Print("opened")
	os.Exit(0)
}
func TestAdmissionLedgerConcurrentProcessCannotResetReservations(t *testing.T) {
	ledger, path, key, id, policy, binding, now := admissionFixture(t)
	reservation, err := ledger.Admit(t.Context(), policy, binding, admissionRequest(1), now)
	if err != nil || !reservation.Dispatch {
		t.Fatal(err)
	}
	input, _ := json.Marshal(struct {
		Path, ID string
		Key      []byte
		Now      time.Time
	}{path, id, key, now})
	command := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestAdmissionLedgerProcessHelper$")
	command.Env = []string{"MYCHEF_ADMISSION_HELPER=1"}
	command.Stdin = bytes.NewReader(input)
	output, err := command.CombinedOutput()
	if err != nil || string(output) != "locked" {
		t.Fatal("another process reset or shared worker ownership", err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}
	command = exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestAdmissionLedgerProcessHelper$")
	command.Env = []string{"MYCHEF_ADMISSION_HELPER=1"}
	command.Stdin = bytes.NewReader(input)
	output, err = command.CombinedOutput()
	if err != nil || string(output) != "opened" {
		t.Fatal("separate worker could not recover without redispatching", err)
	}
}
