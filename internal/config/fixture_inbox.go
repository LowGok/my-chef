package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"time"
)

type FixtureInbox struct {
	Origin             string
	CapabilityHash     [32]byte
	Expires            time.Time
	AllowLoopbackLinks bool
}

func LoadFixtureInbox(app Config, delivery Email, now time.Time) (*FixtureInbox, error) {
	path := os.Getenv("MYCHEF_FIXTURE_INBOX_FILE")
	if path == "" {
		return nil, nil
	}
	invalid := errors.New("invalid fixture inbox configuration")
	host, _, err := net.SplitHostPort(app.Addr)
	ip, ipErr := netip.ParseAddr(host)
	if err != nil || ipErr != nil || !ip.IsLoopback() || !delivery.Fake ||
		app.Provider != "fixture" || os.Getenv("MYCHEF_DEV") != "1" || os.Getenv("MYCHEF_E2E") != "1" {
		return nil, invalid
	}
	origin, err := url.Parse(app.Origin)
	if err != nil || origin.Hostname() == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return nil, invalid
	}
	linkHost, err := netip.ParseAddr(origin.Hostname())
	loopbackLinks := origin.Scheme == "http" && err == nil && linkHost.IsLoopback()
	if origin.Scheme != "https" && !loopbackLinks {
		return nil, invalid
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, invalid
	}
	defer func() { _ = file.Close() }()
	var document struct {
		Capability string    `json:"capability"`
		Expires    time.Time `json:"expires"`
	}
	data, err := io.ReadAll(io.LimitReader(file, 1025))
	if err != nil || len(data) > 1024 {
		return nil, invalid
	}
	reader := json.NewDecoder(bytes.NewReader(data))
	reader.DisallowUnknownFields()
	if err := reader.Decode(&document); err != nil || reader.Decode(new(any)) != io.EOF ||
		!document.Expires.After(now) || document.Expires.After(now.Add(30*time.Minute)) {
		return nil, invalid
	}
	value, err := base64.RawURLEncoding.Strict().DecodeString(document.Capability)
	if err != nil || len(value) != 32 {
		return nil, invalid
	}
	return &FixtureInbox{Origin: app.Origin, CapabilityHash: sha256.Sum256(value), Expires: document.Expires,
		AllowLoopbackLinks: loopbackLinks}, nil
}
