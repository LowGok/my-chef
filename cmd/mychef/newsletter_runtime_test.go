package main

import (
	"testing"

	"github.com/LowGok/my-chef/internal/config"
)

func TestNewsletterExecutableReadiness(t *testing.T) {
	settings := config.Newsletter{Mode: "fixture", ServiceToken: "01234567890123456789012345678901", Origin: "https://mychef.dev", ConsentVersion: "newsletter-v1", Contact: "preview@example.test", PendingDays: config.NewsletterRetention{Days: 7}, WithdrawnDays: config.NewsletterRetention{Days: 30}, OperationsReady: true, CollectionEnabled: true}
	if !newsletterReady(settings, true, true, true, false) {
		t.Fatal("fixture did not become ready")
	}
	for _, input := range []struct{ storage, email, keys, failed bool }{{false, true, true, false}, {true, false, true, false}, {true, true, false, false}, {true, true, true, true}} {
		if newsletterReady(settings, input.storage, input.email, input.keys, input.failed) {
			t.Fatal("unavailable prerequisite accepted")
		}
	}
	settings.Mode = "enabled"
	if newsletterReady(settings, true, true, true, false) {
		t.Fatal("public capture bypassed issue 14 compatibility")
	}
}
