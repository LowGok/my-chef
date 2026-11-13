package httpapi

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/LowGok/my-chef/internal/newsletter"
	"github.com/LowGok/my-chef/internal/store"
)

func (a *API) newsletterRoute(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Robots-Tag", "noindex")
		service := a.Config.Newsletter
		if service == nil {
			a.fail(w, r, 503, "newsletter_unavailable", "Newsletter registration is temporarily unavailable.")
			return
		}
		settings := a.Config.NewsletterSettings
		provided, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || len(settings.ServiceToken) < 32 || subtle.ConstantTimeCompare([]byte(provided), []byte(settings.ServiceToken)) != 1 {
			a.fail(w, r, 401, "unauthenticated", "Service authentication required.")
			return
		}
		if r.Header.Get("Origin") != settings.Origin {
			a.fail(w, r, 403, "origin_invalid", "The request origin is invalid.")
			return
		}
		source, err := netip.ParseAddr(r.Header.Get("X-MyChef-Newsletter-Source"))
		if err != nil || source.Zone() != "" || source.IsUnspecified() || source.IsMulticast() {
			a.fail(w, r, 400, "invalid_input", "A valid request source is required.")
			return
		}
		if !a.throttle(w, r, "newsletter/source/"+source.String(), 60) {
			return
		}
		if action == "subscribe" {
			if a.Config.NewsletterReady == nil || !a.Config.NewsletterReady(r.Context()) {
				a.fail(w, r, 503, "newsletter_unavailable", "Newsletter registration is temporarily unavailable.")
				return
			}
			var input newsletter.Input
			if !a.decodeNewsletter(w, r, &input) {
				return
			}
			if settings.Mode == "fixture" {
				address, _, err := store.EmailIdentity(input.Email)
				if err != nil || !syntheticNewsletterAddress(address) {
					a.fail(w, r, 400, "synthetic_email_required", "Use an address ending in .test or .invalid in this private preview.")
					return
				}
			}
			wait, err := service.Request(r.Context(), source.String(), input)
			if err != nil {
				a.newsletterError(w, r, err)
				return
			}
			if wait > 0 {
				w.Header().Set("Retry-After", strconv.Itoa(wait))
				a.fail(w, r, 429, "rate_limited", "Wait before trying again.")
				return
			}
			a.write(w, 202, map[string]string{"status": "pending-confirmation"})
			return
		}
		var input struct {
			Proof string `json:"proof"`
		}
		if !a.decodeNewsletter(w, r, &input) {
			return
		}
		bucket, err := store.EmailRateBucket(service.RateKey, "newsletter/proof", input.Proof)
		if err != nil {
			a.newsletterError(w, r, newsletter.ErrInput)
			return
		}
		rate, err := a.Store.ReserveEmailLimits(r.Context(), nil, time.Now(), store.EmailLimit{Bucket: bucket, Maximum: 20, Window: time.Hour})
		if err != nil {
			a.newsletterError(w, r, err)
			return
		}
		if rate.RetryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(rate.RetryAfter))
			a.fail(w, r, 429, "rate_limited", "Wait before trying again.")
			return
		}
		status := "confirmed"
		if action == "unsubscribe" {
			err = service.Unsubscribe(r.Context(), input.Proof)
			status = "unsubscribed"
		} else {
			err = service.Confirm(r.Context(), input.Proof)
		}
		if err != nil {
			a.newsletterError(w, r, err)
			return
		}
		a.write(w, 200, map[string]string{"status": status})
	}
}

func syntheticNewsletterAddress(address string) bool {
	_, domain, ok := strings.Cut(address, "@")
	domain = strings.ToLower(domain)
	return ok && (strings.HasSuffix(domain, ".test") || strings.HasSuffix(domain, ".invalid"))
}

func (a *API) decodeNewsletter(w http.ResponseWriter, r *http.Request, value any) bool {
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "application/json" {
		a.fail(w, r, 400, "invalid_input", "Send a JSON request.")
		return false
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4097))
	if err != nil || len(raw) > 4096 {
		a.fail(w, r, 413, "invalid_input", "The request is too large.")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil || decoder.Decode(new(any)) != io.EOF {
		a.fail(w, r, 400, "invalid_input", "Check the request fields.")
		return false
	}
	return true
}

func (a *API) newsletterError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, newsletter.ErrInput) || errors.Is(err, newsletter.ErrProof) {
		a.fail(w, r, 400, "invalid_input", "Check the email or request a new confirmation link.")
		return
	}
	a.fail(w, r, 503, "newsletter_unavailable", "Newsletter registration is temporarily unavailable.")
}
