package billing

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
)

const appleCallbackBodyLimit = 64 << 10

// NotificationHandler builds the signed notification receiver. The application
// does not register it automatically. Hosting it requires an approved Apple
// callback and the protected journal for the configured environment.
func (p *AppleProvider) NotificationHandler(j *Journal) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		media, _, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
		if err != nil || media != "application/json" {
			w.WriteHeader(http.StatusUnsupportedMediaType)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, req.Body, appleCallbackBodyLimit))
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		var envelope struct {
			SignedPayload string `json:"signedPayload"`
		}
		if err != nil || json.Unmarshal(body, &envelope) != nil || envelope.SignedPayload == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		now := p.now().UTC()
		notice, err := p.verifiedNotice(envelope.SignedPayload, now)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if notice != nil {
			if j == nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			switch err := j.retainAppleNotice(req.Context(), *notice, now); {
			case errors.Is(err, ErrPurchaseBinding):
				w.WriteHeader(http.StatusBadRequest)
				return
			case err != nil && !errors.Is(err, errUnownedAppleNotice):
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
