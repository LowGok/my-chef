package config

import (
	"net/mail"
	"os"
)

// LoadFeedbackRecipient leaves written feedback unavailable when the operator
// mailbox is not configured. A header or a list of addresses is not a mailbox.
func LoadFeedbackRecipient() string {
	raw := os.Getenv("MYCHEF_FEEDBACK_TO")
	if len(raw) > 254 {
		return ""
	}
	parsed, err := mail.ParseAddress(raw)
	if err != nil || parsed.Name != "" || parsed.Address != raw {
		return ""
	}
	return raw
}
