package config

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/mail"
	"os"
	"strings"
)

type Email struct {
	Disabled                   bool
	Fake                       bool
	APIKey, From, ReplyTo      string
	WebhookKey, FingerprintKey []byte
}

func LoadEmail() (Email, error) {
	mode := os.Getenv("MYCHEF_EMAIL_MODE")
	if mode == "disabled" {
		return Email{Disabled: true}, nil
	}
	dev := os.Getenv("MYCHEF_DEV") == "1"
	if mode == "" && dev {
		mode = "fake"
	}
	if mode == "fake" && dev {
		return Email{Fake: true, From: "mychef@example.test"}, nil
	}
	if mode != "resend" {
		return Email{}, errors.New("set MYCHEF_EMAIL_MODE=resend, disabled or use explicit development mode")
	}
	var secrets struct {
		APIKey      string `json:"resend_api_key"`
		From        string `json:"email_from"`
		ReplyTo     string `json:"email_reply_to"`
		Webhook     string `json:"resend_webhook_secret"`
		Fingerprint string `json:"email_fingerprint_key"`
	}
	data, err := os.ReadFile(os.Getenv("MYCHEF_CREDENTIALS"))
	if err != nil || json.Unmarshal(data, &secrets) != nil {
		return Email{}, errors.New("email credential file unavailable or invalid")
	}
	webhook, hasPrefix := strings.CutPrefix(secrets.Webhook, "whsec_")
	webhookKey, webhookErr := base64.StdEncoding.DecodeString(webhook)
	fp, fpErr := base64.StdEncoding.DecodeString(secrets.Fingerprint)
	from, fromErr := mail.ParseAddress(secrets.From)
	if !strings.HasPrefix(secrets.APIKey, "re_") || len(secrets.APIKey) < 12 || strings.ContainsAny(secrets.APIKey, " \t\r\n") ||
		!hasPrefix || webhookErr != nil || len(webhookKey) < 16 || fpErr != nil || len(fp) != 32 || fromErr != nil {
		return Email{}, errors.New("invalid Resend email settings")
	}
	if noReplyAddress(from) {
		return Email{}, errors.New("email sender must not be a no-reply address")
	}
	if secrets.ReplyTo != "" {
		reply, err := mail.ParseAddress(secrets.ReplyTo)
		if err != nil {
			return Email{}, errors.New("invalid email reply-to")
		}
		if noReplyAddress(reply) {
			return Email{}, errors.New("email reply-to must not be a no-reply address")
		}
	}
	return Email{APIKey: secrets.APIKey, From: secrets.From, ReplyTo: secrets.ReplyTo, WebhookKey: webhookKey, FingerprintKey: fp}, nil
}

func noReplyAddress(address *mail.Address) bool {
	local := address.Address[:strings.LastIndex(address.Address, "@")]
	local, _, _ = strings.Cut(local, "+")
	normalized := strings.NewReplacer("-", "", "_", "", ".", "").Replace(strings.ToLower(local))
	return normalized == "noreply" || normalized == "donotreply"
}
