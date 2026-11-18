package main

import "testing"

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
