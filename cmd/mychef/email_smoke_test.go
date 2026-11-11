package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmailStartupProcess(t *testing.T) {
	if os.Getenv("MYCHEF_STARTUP_TEST_CHILD") == "1" {
		os.Args = []string{"mychef"}
		main()
		os.Exit(0)
	}
	for _, mode := range []string{"missing", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "test-credentials")
			if mode == "malformed" {
				if err := os.WriteFile(path, []byte(`{"resend_api_key":"credential-canary","email_from":"recipient-canary@example.test"}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("MYCHEF_STARTUP_TEST_CHILD", "1")
			t.Setenv("MYCHEF_DEV", "0")
			t.Setenv("MYCHEF_EMAIL_MODE", "resend")
			t.Setenv("MYCHEF_ORIGIN", "https://example.test")
			t.Setenv("MYCHEF_ADDR", "127.0.0.1:0")
			t.Setenv("MYCHEF_PROVIDER", "gemini")
			t.Setenv("MYCHEF_CREDENTIALS", path)
			t.Setenv("MYCHEF_TRACE_RETENTION_HOURS", "168")
			out, err := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestEmailStartupProcess$").CombinedOutput()
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != 1 || !strings.Contains(string(out), "invalid email configuration") {
				t.Fatalf("expected startup refusal, got %v", err)
			}
			if strings.Contains(string(out), "credential-canary") || strings.Contains(string(out), "recipient-canary") {
				t.Fatal("startup exposed private settings")
			}
		})
	}
}

func TestEmailSmokeRequiresExplicitCheck(t *testing.T) {
	t.Setenv("MYCHEF_DEV", "1")
	t.Setenv("MYCHEF_EMAIL_MODE", "fake")
	t.Setenv("MYCHEF_CREDENTIALS", "")
	if code := runEmailSmoke(nil); code != 2 {
		t.Fatalf("missing acknowledgement: %d", code)
	}
	if code := runEmailSmoke([]string{"--tracking-off-verified", "recipient-canary@example.test"}); code != 2 {
		t.Fatalf("arbitrary recipient: %d", code)
	}
	if code := runEmailSmoke([]string{"--tracking-off-verified"}); code != 0 {
		t.Fatalf("fake smoke: %d", code)
	}
}
