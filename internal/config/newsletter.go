package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/mail"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type Newsletter struct {
	Mode              string
	ServiceToken      string
	Origin            string
	ConsentVersion    string
	Contact           string
	PendingDays       int
	WithdrawnDays     int
	OperationsReady   bool
	CollectionEnabled bool
}

func LoadNewsletter(app Config, email Email) (Newsletter, error) {
	mode := os.Getenv("MYCHEF_NEWSLETTER_MODE")
	if mode == "" || mode == "disabled" {
		return Newsletter{Mode: "disabled"}, nil
	}
	invalid := errors.New("invalid newsletter configuration")
	if mode != "enabled" && mode != "fixture" {
		return Newsletter{}, invalid
	}
	if mode == "fixture" {
		host, _, err := net.SplitHostPort(app.Addr)
		if err != nil || os.Getenv("MYCHEF_DEV") != "1" || app.Provider != "fixture" || !email.Fake || !net.ParseIP(host).IsLoopback() {
			return Newsletter{}, invalid
		}
	} else if email.Fake {
		return Newsletter{}, invalid
	}
	c := Newsletter{Mode: mode, Origin: os.Getenv("MYCHEF_NEWSLETTER_ORIGIN"), ConsentVersion: os.Getenv("MYCHEF_NEWSLETTER_CONSENT_VERSION"), Contact: os.Getenv("MYCHEF_NEWSLETTER_CONTACT"), OperationsReady: os.Getenv("MYCHEF_NEWSLETTER_OPERATIONS_READY") == "1", CollectionEnabled: os.Getenv("MYCHEF_NEWSLETTER_COLLECT") != "0"}
	if c.Origin == "" {
		c.Origin = "https://mychef.dev"
	}
	if c.ConsentVersion == "" {
		c.ConsentVersion = "newsletter-v1"
	}
	origin, err := url.Parse(c.Origin)
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" || len(c.ConsentVersion) > 80 {
		return Newsletter{}, invalid
	}
	if c.Contact != "" {
		address, err := mail.ParseAddress(c.Contact)
		if err != nil || address.Address != c.Contact {
			return Newsletter{}, invalid
		}
	}
	for _, setting := range []struct {
		name   string
		target *int
	}{{"MYCHEF_NEWSLETTER_PENDING_DAYS", &c.PendingDays}, {"MYCHEF_NEWSLETTER_WITHDRAWN_DAYS", &c.WithdrawnDays}} {
		if value := os.Getenv(setting.name); value != "" {
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 || n > 3650 {
				return Newsletter{}, invalid
			}
			*setting.target = n
		}
	}
	if path := os.Getenv("MYCHEF_CREDENTIALS"); path != "" {
		file, err := os.Open(path)
		if err != nil {
			return Newsletter{}, invalid
		}
		defer func() { _ = file.Close() }()
		var credentials struct {
			Token string `json:"newsletter_service_token"`
		}
		raw, err := io.ReadAll(io.LimitReader(file, 65537))
		if err != nil || len(raw) > 65536 {
			return Newsletter{}, invalid
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		if decoder.Decode(&credentials) != nil || decoder.Decode(new(any)) != io.EOF {
			return Newsletter{}, invalid
		}
		c.ServiceToken = credentials.Token
		if c.ServiceToken != "" && (len(c.ServiceToken) < 32 || len(c.ServiceToken) > 256 || strings.IndexFunc(c.ServiceToken, func(r rune) bool { return r < 33 || r > 126 }) >= 0) {
			return Newsletter{}, invalid
		}
	}
	return c, nil
}

// Readiness contains only operator-safe booleans, with no addresses or secrets.
func (c Newsletter) Readiness(storage, email, keys, identityCompatible bool) map[string]bool {
	return map[string]bool{
		"mode":                  c.Mode == "enabled" || c.Mode == "fixture",
		"collectionEnabled":     c.CollectionEnabled,
		"serviceCredential":     len(c.ServiceToken) >= 32,
		"publicOrigin":          c.Origin != "",
		"consentText":           c.ConsentVersion != "",
		"controllerContact":     c.Contact != "",
		"retentionPolicy":       c.PendingDays > 0 && c.WithdrawnDays > 0,
		"contactOperations":     c.OperationsReady,
		"storage":               storage,
		"emailDelivery":         email,
		"proofKeys":             keys,
		"identityCompatibility": identityCompatible || c.Mode == "fixture",
	}
}
