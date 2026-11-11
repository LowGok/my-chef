package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/store"
)

func TestLifecycleRuntimeChild(t *testing.T) {
	if os.Getenv("MYCHEF_LIFECYCLE_CHILD") != "1" {
		return
	}
	os.Args = []string{"mychef"}
	main()
	os.Exit(0)
}

func lifecycleRuntimeCommand(t *testing.T, db, journal string) (*exec.Cmd, string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	command := exec.Command(os.Args[0], "-test.run=^TestLifecycleRuntimeChild$")
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "MYCHEF_") && !strings.HasPrefix(entry, "RESEND_") && !strings.HasPrefix(entry, "CLOUDINARY_") && !strings.HasPrefix(entry, "GEMINI_") {
			command.Env = append(command.Env, entry)
		}
	}
	origin := "http://" + address
	command.Env = append(command.Env, "MYCHEF_LIFECYCLE_CHILD=1", "MYCHEF_DB="+db, "MYCHEF_LIFECYCLE_DB="+journal, "MYCHEF_ADDR="+address, "MYCHEF_ORIGIN="+origin, "MYCHEF_DEV=1", "MYCHEF_E2E=1", "MYCHEF_PROVIDER=fixture")
	return command, origin
}
func runtimeDatabase(t *testing.T) (*store.Store, *store.LifecycleJournal) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "app.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err = s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	j, err := s.InitializeLifecycleJournal(t.Context(), s.Path+".lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	return s, j
}
func TestLifecycleRuntimeRefusesMissingWrongAndUncertainJournal(t *testing.T) {
	for _, kind := range []string{"missing", "wrong", "uncertain", "held"} {
		t.Run(kind, func(t *testing.T) {
			s, j := runtimeDatabase(t)
			path := j.Path
			switch kind {
			case "missing":
				path += ".absent"
			case "wrong":
				_, other := runtimeDatabase(t)
				path = other.Path
			case "uncertain":
				if err := j.PrepareDeletion(t.Context(), strings.Repeat("a", 32), strings.Repeat("b", 32)); err != nil {
					t.Fatal(err)
				}
			case "held":
				unlock, err := store.LockApplication(j.Path)
				if err != nil {
					t.Fatal(err)
				}
				defer unlock()
			}
			command, _ := lifecycleRuntimeCommand(t, s.Path, path)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			// A timeout is a failure: refusal must happen before a listener is started.
			done := make(chan error, 1)
			var output bytes.Buffer
			command.Stdout = &output
			command.Stderr = &output
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			go func() { done <- command.Wait() }()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("unsafe server accepted journal")
				}
			case <-ctx.Done():
				_ = command.Process.Kill()
				waitErr := <-done
				t.Fatal("unsafe server stayed running", waitErr, output.String())
			}
			if kind == "missing" {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("startup created missing journal")
				}
			}
		})
	}
}
func TestLifecycleRuntimeReconcilesBeforeReadyAndBlocksJournalLoss(t *testing.T) {
	s, j := runtimeDatabase(t)
	ctx := t.Context()
	u := store.User{FirstName: "Disposable", LastName: "Fixture", Username: "runtime", Email: "runtime@example.test", Password: "fixture-hash"}
	if err := s.DB.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	token := "runtime-session"
	if err := s.DB.Create(&store.Session{Hash: token, UserID: &u.ID, ExpiresUnix: time.Now().Add(time.Hour).Unix()}).Error; err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "before.db")
	if err := s.Backup(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := j.DeleteAccount(ctx, store.AccountActor{UserID: u.ID, SessionHash: token, PasswordHash: u.Password}, strings.Repeat("c", 32), "DELETE", time.Now(), store.AccountDeletionOptions{}); err != nil {
		t.Fatal(err)
	}
	// Bypass the restore command to simulate an operator placing the old copy.
	command, origin := lifecycleRuntimeCommand(t, snapshot, j.Path)
	output, err := os.Create(filepath.Join(t.TempDir(), "server.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = output.Close() }()
	command.Stdout = output
	command.Stderr = output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	defer func() {
		_ = command.Process.Signal(os.Interrupt)
		select {
		case err := <-done:
			if err != nil {
				t.Error("server shutdown failed", err)
			}
		case <-time.After(10 * time.Second):
			_ = command.Process.Kill()
			waitErr := <-done
			t.Error("server did not stop", waitErr)
		}
	}()
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(10 * time.Second)
	for {
		response, err := client.Get(origin + "/readyz")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture did not become ready")
		}
		time.Sleep(20 * time.Millisecond)
	}
	copy, err := store.OpenReadOnly(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var users int
	err = copy.SQL.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&users)
	_ = copy.Close()
	if err != nil || users != 0 {
		t.Fatal("readiness preceded deleted account purge", users, err)
	}
	if err := os.Rename(j.Path, j.Path+".lost"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/readyz", "/api/v1/session"} {
		response, err := client.Get(origin + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != 503 {
			t.Fatalf("journal loss allowed %s: %d", path, response.StatusCode)
		}
	}
}

func TestLifecycleDisabledSenderStillAllowsPermanentAccountDeletion(t *testing.T) {
	s, j := runtimeDatabase(t)
	const password = "A disposable account deletion sentence!"
	manager := auth.New(s)
	user, err := manager.Register(t.Context(), auth.Registration{Email: "disabled-mail@example.test", Username: "disabledmail", FirstName: "Disabled", LastName: "Sender", Password: password})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Model(&store.User{}).Where("user_id=?", user.ID).Update("email_verified_at", time.Now().Unix()).Error; err != nil {
		t.Fatal(err)
	}
	token, err := manager.Rotate(t.Context(), &user.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	command, origin := lifecycleRuntimeCommand(t, s.Path, j.Path)
	command.Env = append(command.Env, "MYCHEF_EMAIL_MODE=disabled", "MYCHEF_CREDENTIALS="+filepath.Join(t.TempDir(), "missing-provider-credentials.json"))
	log, err := os.Create(filepath.Join(t.TempDir(), "disabled-server.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	command.Stdout = log
	command.Stderr = log
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var processErr error
	go func() { processErr = command.Wait(); close(done) }()
	defer func() {
		_ = command.Process.Signal(os.Interrupt)
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = command.Process.Kill()
			<-done
			t.Error("disabled sender fixture did not stop")
		}
	}()
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(10 * time.Second)
	for {
		select {
		case <-done:
			t.Fatal("disabled sender prevented account service startup", processErr)
		default:
		}
		response, err := client.Get(origin + "/readyz")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("disabled sender account service did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	body := strings.NewReader(`{"currentPassword":"A disposable account deletion sentence!","confirmation":"DELETE"}`)
	request, err := http.NewRequestWithContext(t.Context(), "POST", origin+"/api/v1/account/deletion", body)
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(&http.Cookie{Name: "mychef_session", Value: token})
	request.Header.Set("Origin", origin)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", auth.CSRF(token))
	request.Header.Set("Idempotency-Key", strings.Repeat("d", 32))
	// Password verification is intentionally costly, especially under the race detector.
	deletionClient := &http.Client{Timeout: 30 * time.Second}
	response, err := deletionClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("disabled sender blocked account deletion", response.StatusCode)
	}
	var users, notices int
	if err := s.SQL.QueryRow("SELECT COUNT(*) FROM users").Scan(&users); err != nil || users != 0 {
		t.Fatal("disabled sender kept account live", err)
	}
	if err := s.SQL.QueryRow("SELECT COUNT(*) FROM email_outbox WHERE template=?", store.AccountDeletedTemplate).Scan(&notices); err != nil || notices != 0 {
		t.Fatal("disabled sender stored a recipient snapshot", err)
	}
	var notice string
	if err := s.SQL.QueryRow("SELECT notice FROM lifecycle_operations").Scan(&notice); err != nil || notice != "skipped" {
		t.Fatal("disabled notice result was not recorded", err)
	}
}
