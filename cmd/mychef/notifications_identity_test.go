package main

import (
	"testing"

	"github.com/LowGok/my-chef/internal/notifications"
)

func TestPushRouteSelectionNeverFallsBackToSibling(t *testing.T) {
	preview, app := new(notifications.PushSender), new(notifications.PushSender)
	for _, lane := range []string{"", "preview"} {
		if selectPushSender(lane, preview, app) != preview {
			t.Fatal("legacy sender changed")
		}
	}
	if selectPushSender("app", preview, app) != app {
		t.Fatal("candidate did not select its sender")
	}
	if selectPushSender("app", preview, nil) != nil || selectPushSender("unknown", preview, app) != nil {
		t.Fatal("missing route fell back to Preview")
	}
}
