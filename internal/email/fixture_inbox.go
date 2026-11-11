package email

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"html/template"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/LowGok/my-chef/internal/config"
	"github.com/LowGok/my-chef/internal/store"
)

const fixtureMessageLimit = 256
const fixtureMessageLife = 10 * time.Minute

type fixtureMessage struct {
	recipient, text string
	expires         time.Time
}

type FixtureInbox struct {
	settings config.FixtureInbox
	now      func() time.Time
	mu       sync.Mutex
	seen     map[string]struct{}
	messages []fixtureMessage
	closed   bool
}

func NewFixtureInbox(settings config.FixtureInbox, now func() time.Time) *FixtureInbox {
	if now == nil {
		now = time.Now
	}
	return &FixtureInbox{settings: settings, now: now, seen: map[string]struct{}{}}
}

func syntheticRecipient(value string) (string, bool) {
	display, _, err := store.EmailIdentity(value)
	if err != nil {
		return "", false
	}
	domain := strings.ToLower(display[strings.LastIndexByte(display, '@')+1:])
	return display, strings.HasSuffix(domain, ".test") || strings.HasSuffix(domain, ".invalid")
}

func (i *FixtureInbox) record(key string, body []byte) {
	if i == nil || len(body) > 64*1024 {
		return
	}
	var payload sendPayload
	if json.Unmarshal(body, &payload) != nil || len(payload.To) != 1 || payload.Text == "" {
		return
	}
	address, ok := syntheticRecipient(payload.To[0])
	if !ok {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	now := i.now()
	i.pruneLocked(now)
	if _, repeated := i.seen[key]; repeated || i.closed || !now.Before(i.settings.Expires) || len(i.seen) >= fixtureMessageLimit {
		return
	}
	i.seen[key] = struct{}{}
	i.messages = append(i.messages, fixtureMessage{recipient: address, text: payload.Text, expires: now.Add(fixtureMessageLife)})
}

func (i *FixtureInbox) pruneLocked(now time.Time) {
	if !now.Before(i.settings.Expires) || i.closed {
		clear(i.messages)
		i.messages = nil
		clear(i.seen)
		return
	}
	kept := i.messages[:0]
	for _, message := range i.messages {
		if now.Before(message.expires) {
			kept = append(kept, message)
		}
	}
	clear(i.messages[len(kept):])
	i.messages = kept
}

func (i *FixtureInbox) prune() {
	if i == nil {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.pruneLocked(i.now())
}

func (i *FixtureInbox) close() {
	if i == nil {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.closed = true
	i.pruneLocked(i.now())
}

func (i *FixtureInbox) take(token, recipient string) (string, bool) {
	value, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(value) != 32 {
		return "", false
	}
	hash := sha256.Sum256(value)
	if subtle.ConstantTimeCompare(hash[:], i.settings.CapabilityHash[:]) != 1 {
		return "", false
	}
	recipient, ok := syntheticRecipient(recipient)
	if !ok {
		return "", false
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	now := i.now()
	i.pruneLocked(now)
	if i.closed || !now.Before(i.settings.Expires) {
		return "", false
	}
	for j, message := range i.messages {
		if message.recipient == recipient {
			copy(i.messages[j:], i.messages[j+1:])
			i.messages[len(i.messages)-1] = fixtureMessage{}
			i.messages = i.messages[:len(i.messages)-1]
			return message.text, true
		}
	}
	return "", true
}

var fixturePage = template.Must(template.New("fixture").Parse(`<!doctype html>
<html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>MyChef test inbox</title>
<h1>MyChef test inbox</h1>
<form method="post" action="/fixture/inbox" autocomplete="off">
<p><label>Test inbox code <input name="capability" type="password" maxlength="43" required></label></p>
<p><label>Synthetic email <input name="recipient" type="text" inputmode="email" autocapitalize="none" spellcheck="false" maxlength="254" required></label></p>
<button>Read the next message</button>
</form>
{{if .}}<pre>{{.}}</pre>{{end}}</html>`))

func (i *FixtureInbox) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	page := r.URL.Path == "/fixture/inbox"
	if page {
		// The HTML form needs an exact Origin on its same-origin POST.
		// No-referrer makes browsers send a null Origin for that navigation.
		w.Header().Set("Referrer-Policy", "same-origin")
	} else {
		w.Header().Set("Referrer-Policy", "no-referrer")
	}
	if !i.now().Before(i.settings.Expires) || r.URL.RawQuery != "" {
		http.NotFound(w, r)
		return
	}
	if page && r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = fixturePage.Execute(w, "")
		return
	}
	if r.Method != http.MethodPost || r.Header.Get("Origin") != i.settings.Origin {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2048)
	var token, recipient string
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	switch {
	case page && media == "application/x-www-form-urlencoded":
		if r.ParseForm() != nil {
			http.NotFound(w, r)
			return
		}
		token, recipient = r.PostForm.Get("capability"), r.PostForm.Get("recipient")
	case !page && media == "application/json":
		token, _ = strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		var input struct {
			Recipient string `json:"recipient"`
		}
		reader := json.NewDecoder(r.Body)
		reader.DisallowUnknownFields()
		if reader.Decode(&input) != nil || reader.Decode(new(any)) != io.EOF {
			http.NotFound(w, r)
			return
		}
		recipient = input.Recipient
	default:
		http.NotFound(w, r)
		return
	}
	if len(token) != 43 || len(recipient) > 254 {
		http.NotFound(w, r)
		return
	}
	message, ok := i.take(token, recipient)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if page {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if message == "" {
			message = "No test message yet."
		}
		_ = fixturePage.Execute(w, message)
		return
	}
	if message == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"text": message})
}
