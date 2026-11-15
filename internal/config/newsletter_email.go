package config

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
)

// LoadNewsletterEmail loads the sending-only role without requiring a webhook
// that its restricted HTTP surface does not expose. Full-app loading is unchanged.
func LoadNewsletterEmail() (Email, error) {
	invalid := errors.New("invalid newsletter email configuration")
	if os.Getenv("MYCHEF_EMAIL_MODE") == "fake" {
		cfg, err := LoadEmail()
		if err != nil || !cfg.Fake || cfg.NewsletterFrom == "" || cfg.NewsletterReplyTo == "" {
			return Email{}, invalid
		}
		return cfg, nil
	}
	if os.Getenv("MYCHEF_EMAIL_MODE") != "resend" {
		return Email{}, invalid
	}
	file, err := os.Open(os.Getenv("MYCHEF_CREDENTIALS"))
	if err != nil {
		return Email{}, invalid
	}
	defer func() { _ = file.Close() }()
	body, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil || len(body) > 65536 {
		return Email{}, invalid
	}
	var profile struct {
		APIKey      string `json:"resend_api_key"`
		From        string `json:"newsletter_email_from"`
		ReplyTo     string `json:"newsletter_email_reply_to"`
		Fingerprint string `json:"email_fingerprint_key"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if decoder.Decode(&profile) != nil || decoder.Decode(new(any)) != io.EOF {
		return Email{}, invalid
	}
	fingerprint, err := base64.StdEncoding.DecodeString(profile.Fingerprint)
	if err != nil || len(fingerprint) != 32 || !validResendAPIKey(profile.APIKey) {
		return Email{}, invalid
	}
	cfg := Email{APIKey: profile.APIKey, FingerprintKey: fingerprint}
	if setNewsletterSender(&cfg, profile.From, profile.ReplyTo) != nil || cfg.NewsletterFrom == "" || cfg.NewsletterReplyTo == "" {
		return Email{}, invalid
	}
	cfg.From, cfg.ReplyTo = cfg.NewsletterFrom, cfg.NewsletterReplyTo
	return cfg, nil
}
