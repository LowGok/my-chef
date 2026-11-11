package config

import "testing"

func TestInsecureSettingsAreLoopbackOnly(t *testing.T) {
	t.Setenv("MYCHEF_ORIGIN", "http://127.0.0.1:18137")
	t.Setenv("MYCHEF_DEV", "1")
	t.Setenv("MYCHEF_ADDR", "127.0.0.1:18137")
	t.Setenv("MYCHEF_PROVIDER", "fixture")
	t.Setenv("MYCHEF_E2E", "1")
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MYCHEF_ADDR", "0.0.0.0:8080")
	if _, err := Load(); err == nil {
		t.Fatal("public insecure/fixture server accepted")
	}
	t.Setenv("MYCHEF_ADDR", "127.0.0.1:18137")
	t.Setenv("MYCHEF_ORIGIN", "http://example.test")
	if _, err := Load(); err == nil {
		t.Fatal("insecure public origin accepted")
	}
}

func TestTrustedEmailProxyListUsesLiteralAddresses(t *testing.T) {
	t.Setenv("MYCHEF_ORIGIN", "https://app.example.test")
	t.Setenv("MYCHEF_PROVIDER", "gemini")
	t.Setenv("MYCHEF_TRUSTED_PROXY_IPS", "127.0.0.1, ::ffff:192.0.2.2, 192.0.2.2")
	cfg, err := Load()
	if err != nil || len(cfg.TrustedProxies) != 2 || cfg.TrustedProxies[1].String() != "192.0.2.2" {
		t.Fatalf("proxy addresses not normalized and deduplicated: %v", err)
	}
	for _, value := range []string{"proxy.example.test", "192.0.2.0/24", "0.0.0.0", "127.0.0.1,"} {
		t.Setenv("MYCHEF_TRUSTED_PROXY_IPS", value)
		if _, err := Load(); err == nil {
			t.Fatal("accepted an invalid proxy address")
		}
	}
}
