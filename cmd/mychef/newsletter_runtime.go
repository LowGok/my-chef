package main

import "github.com/LowGok/my-chef/internal/config"

func newsletterReady(settings config.Newsletter, storage, email, keys, failed bool) bool {
	for _, ready := range newsletterChecks(settings, storage, email, keys, failed) {
		if !ready {
			return false
		}
	}
	return true
}

func newsletterChecks(settings config.Newsletter, storage, email, keys, failed bool) map[string]bool {
	// Issue 14 must implement and verify app signup and email-change compatibility
	// before this capability can be enabled in a release.
	const identityCompatibility = false
	checks := settings.Readiness(storage, email, keys, identityCompatibility)
	checks["worker"] = !failed
	return checks
}
