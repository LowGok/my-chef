package config

import (
	"errors"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	LifecycleDatabase string
	Addr              string
	Database          string
	Origin            string
	Secure            bool
	Provider          string
	TraceRetention    time.Duration
	TrustedProxies    []netip.Addr
}

func Load() (Config, error) {
	c := Config{Addr: os.Getenv("MYCHEF_ADDR"), Database: os.Getenv("MYCHEF_DB"), Origin: os.Getenv("MYCHEF_ORIGIN"), Provider: os.Getenv("MYCHEF_PROVIDER")}
	c.LifecycleDatabase = os.Getenv("MYCHEF_LIFECYCLE_DB")
	hours := 168
	if value := os.Getenv("MYCHEF_TRACE_RETENTION_HOURS"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 168 {
			return c, errors.New("MYCHEF_TRACE_RETENTION_HOURS must be between 1 and 168")
		}
		hours = n
	}
	c.TraceRetention = time.Duration(hours) * time.Hour
	if c.Addr == "" {
		c.Addr = "127.0.0.1:8080"
	}
	if c.Database == "" {
		c.Database = "data/mychef.db"
	}
	if c.LifecycleDatabase == "" {
		c.LifecycleDatabase = c.Database + ".lifecycle"
	}
	appPath, appErr := filepath.Abs(c.Database)
	journalPath, journalErr := filepath.Abs(c.LifecycleDatabase)
	if appErr != nil || journalErr != nil || appPath == journalPath {
		return c, errors.New("account lifecycle journal must be separate from the app database")
	}
	if c.Provider == "" {
		c.Provider = "gemini"
	}
	host, _, err := net.SplitHostPort(c.Addr)
	if err != nil {
		return c, errors.New("MYCHEF_ADDR must contain a host and port")
	}
	origin, err := url.Parse(c.Origin)
	if err != nil || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return c, errors.New("MYCHEF_ORIGIN must be an exact origin without a path")
	}
	loopback := func(host string) bool {
		ip := net.ParseIP(host)
		return host == "localhost" || ip != nil && ip.IsLoopback()
	}
	if origin.Scheme == "https" {
		c.Secure = true
	} else if origin.Scheme != "http" || os.Getenv("MYCHEF_DEV") != "1" || !loopback(host) || !loopback(origin.Hostname()) {
		return c, errors.New("HTTPS is required outside explicit loopback development")
	}
	if c.Provider == "fixture" && (os.Getenv("MYCHEF_E2E") != "1" || !loopback(host)) {
		return c, errors.New("fixture providers require explicit loopback E2E mode")
	}
	if c.Provider != "fixture" && c.Provider != "gemini" {
		return c, errors.New("unsupported provider")
	}
	if value := os.Getenv("MYCHEF_TRUSTED_PROXY_IPS"); value != "" {
		entries := strings.Split(value, ",")
		if len(entries) > 16 {
			return c, errors.New("MYCHEF_TRUSTED_PROXY_IPS accepts up to sixteen literal addresses")
		}
		for _, entry := range entries {
			addr, err := netip.ParseAddr(strings.TrimSpace(entry))
			addr = addr.Unmap()
			if err != nil || addr.Zone() != "" || addr.IsUnspecified() || addr.IsMulticast() {
				return c, errors.New("MYCHEF_TRUSTED_PROXY_IPS requires literal proxy addresses")
			}
			if !slices.Contains(c.TrustedProxies, addr) {
				c.TrustedProxies = append(c.TrustedProxies, addr)
			}
		}
	}
	return c, nil
}
