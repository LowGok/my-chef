//go:build mychef_billing_fixture

package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/auth"
	"github.com/LowGok/my-chef/internal/billing"
)

func TestBillingFixtureServesBothNativePurchasesAndEnforcesPremium(t *testing.T) {
	for _, example := range []struct {
		platform string
		store    billing.Store
	}{{"ios", billing.Apple}, {"android", billing.Play}} {
		t.Run(example.platform, func(t *testing.T) {
			s, journal := runtimeDatabase(t)
			root := filepath.Dir(s.Path)
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(s.Path, 0600); err != nil {
				t.Fatal(err)
			}
			const email = "fixture-billing@example.test"
			const password = "Disposable billing fixture password!"
			mgr := auth.New(s)
			user, err := mgr.Register(t.Context(), auth.Registration{Email: email, Password: password, Username: "fixture-billing",
				FirstName: "Fixture", LastName: "Billing"})
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			if err := s.DB.Exec("UPDATE users SET email_verified_at=? WHERE user_id=?", now.Unix(), user.ID).Error; err != nil {
				t.Fatal(err)
			}
			var capability [32]byte
			if _, err := rand.Read(capability[:]); err != nil {
				t.Fatal(err)
			}
			expires := now.Add(10 * time.Minute)
			settings, err := json.Marshal(map[string]any{"capability": base64.RawURLEncoding.EncodeToString(capability[:]), "expires": expires})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "fixture-inbox.json")
			if err := os.WriteFile(path, settings, 0600); err != nil {
				t.Fatal(err)
			}
			command, origin := lifecycleRuntimeCommand(t, s.Path, journal.Path)
			command.Env = append(command.Env, "MYCHEF_EMAIL_MODE=fake", "MYCHEF_FIXTURE_INBOX_FILE="+path,
				"MYCHEF_BILLING_FIXTURE=1", "MYCHEF_BILLING_FIXTURE_ROOT="+root)
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
					if processErr != nil {
						t.Error("fixture server did not shut down", processErr)
					}
				case <-time.After(5 * time.Second):
					_ = command.Process.Kill()
					<-done
					t.Error("fixture server did not stop")
				}
			}()
			client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil},
				CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			defer client.CloseIdleConnections()
			for deadline := time.Now().Add(10 * time.Second); ; {
				response, err := client.Get(origin + "/readyz")
				if err == nil {
					_ = response.Body.Close()
					if response.StatusCode == http.StatusOK {
						break
					}
				}
				select {
				case <-done:
					t.Fatal("fixture server refused to start", processErr)
				default:
					if time.Now().After(deadline) {
						t.Fatal("fixture server did not become ready")
					}
					time.Sleep(25 * time.Millisecond)
				}
			}

			request := func(method, path, data, access string, want int, output any) {
				t.Helper()
				r, err := http.NewRequestWithContext(t.Context(), method, origin+path, strings.NewReader(data))
				if err != nil {
					t.Fatal(err)
				}
				r.Header.Set("Content-Type", "application/json")
				if access != "" {
					r.Header.Set("Authorization", "Bearer "+access)
				}
				res, err := client.Do(r)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = res.Body.Close() }()
				if res.StatusCode != want {
					t.Fatalf("%s %s returned %d, want %d", method, path, res.StatusCode, want)
				}
				if output != nil && json.NewDecoder(io.LimitReader(res.Body, 32<<10)).Decode(output) != nil {
					t.Fatal("could not read synthetic fixture reply")
				}
			}
			const native = "/api/native/v1"
			var credentials struct{ Native auth.NativeTokens }
			request("GET", "/api/v1/billing/status", "", "", 404, nil)
			request("GET", native+"/billing/status", "", "", 401, nil)
			request("POST", native+"/session", `{"platform":"`+example.platform+`"}`, "", 201, &credentials)
			request("POST", native+"/login", `{"email":"`+email+`","password":"`+password+`"}`,
				credentials.Native.AccessToken, 200, &credentials)
			access := credentials.Native.AccessToken
			var permission struct{ CanWrite bool }
			request("GET", native+"/personal-recipes/permissions", "", access, 200, &permission)
			if permission.CanWrite {
				t.Fatal("fixture account began with Premium")
			}
			var checkout struct{ ID, Binding, Store string }
			request("POST", native+"/billing/checkouts", `{"interval":"monthly"}`, access, 201, &checkout)
			if checkout.ID == "" || checkout.Binding == "" || checkout.Store != string(example.store) {
				t.Fatal("server did not reserve the correct fixture store")
			}
			route := native + "/billing/checkouts/" + checkout.ID
			request("POST", route+"/launch", "", access, 204, nil)
			receipt, err := billing.NewFixtureProvider(sha256.Sum256(capability[:]), example.store, expires).Mint(billing.FixturePurchase{
				Binding: checkout.Binding, Order: "5095aae8-27e3-45de-bef1-bf9b179cedcf", Interval: billing.Monthly,
				StartsAt: now, PaidThrough: now.Add(5 * time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			request("POST", route+"/verify", `{"receipt":"`+receipt+`x"}`, access, 404, nil)
			var result struct{ CompleteStoreTransaction bool }
			request("POST", route+"/verify", `{"receipt":"`+receipt+`"}`, access, 200, &result)
			if !result.CompleteStoreTransaction {
				t.Fatal("server did not confirm the durable simulated purchase")
			}
			request("GET", native+"/personal-recipes/permissions", "", access, 200, &permission)
			if !permission.CanWrite {
				t.Fatal("the verified fixture purchase did not unlock Premium permissions")
			}
		})
	}
}
