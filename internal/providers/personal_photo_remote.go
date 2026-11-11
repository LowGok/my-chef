package providers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/LowGok/my-chef/internal/store"
)

// PrivatePhotoObject contains server-only identity, never a delivery URL.
type PrivatePhotoObject struct {
	Target       string `json:"-"`
	Binding      string `json:"-"`
	DeliveryType string `json:"-"`
	Digest       string `json:"-"`
}

func (PrivatePhotoObject) String() string   { return "private photo object" }
func (PrivatePhotoObject) GoString() string { return "private photo object" }

func validPersonalPhotoTarget(value string) bool {
	if !strings.HasPrefix(value, "mychef/") {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, "mychef/"))
	return err == nil && len(decoded) == 16 && value == "mychef/"+hex.EncodeToString(decoded)
}

func validPersonalPhotoDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && value == hex.EncodeToString(decoded)
}

func (p *Remote) privatePhotoConfigured() bool {
	return p.credentials.CloudName != "" && p.credentials.CloudKey != "" && p.credentials.CloudSecret != ""
}

func (p *Remote) UploadPersonalPhoto(ctx context.Context, photo NormalizedPhoto) (PrivatePhotoObject, error) {
	intent, ok := ctx.Value(uploadIntentKey{}).(uploadIntent)
	if !ok || !validPersonalPhotoTarget(intent.target) || intent.begin == nil || photo.Size() == 0 || photo.Size() > PersonalPhotoLimit || !validPersonalPhotoDigest(photo.Digest()) {
		return PrivatePhotoObject{}, Failure("provider_invalid")
	}
	if !p.privatePhotoConfigured() {
		return PrivatePhotoObject{}, Failure("provider_unavailable")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("file", "recipe.jpg")
	if err != nil {
		return PrivatePhotoObject{}, Failure("provider_unavailable")
	}
	if _, err = file.Write(photo.data); err != nil {
		return PrivatePhotoObject{}, Failure("provider_unavailable")
	}
	for _, field := range [][2]string{{"public_id", intent.target}, {"type", "authenticated"}, {"overwrite", "false"}} {
		if err := writer.WriteField(field[0], field[1]); err != nil {
			return PrivatePhotoObject{}, Failure("provider_unavailable")
		}
	}
	if writer.Close() != nil {
		return PrivatePhotoObject{}, Failure("provider_unavailable")
	}
	call, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(call, http.MethodPost, p.options.CloudinaryEndpoint, &body)
	if err != nil {
		return PrivatePhotoObject{}, Failure("provider_unavailable")
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.SetBasicAuth(p.credentials.CloudKey, p.credentials.CloudSecret)
	if StartReservedUpload(call) != nil {
		return PrivatePhotoObject{}, Failure("provider_unavailable")
	}
	response, err := p.options.Client.Do(request)
	if err != nil {
		return PrivatePhotoObject{}, Failure("provider_unavailable")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return PrivatePhotoObject{}, Failure("provider_unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return PrivatePhotoObject{}, Failure("provider_invalid")
	}
	var result struct {
		Target   string `json:"public_id"`
		Type     string `json:"type"`
		Resource string `json:"resource_type"`
		Format   string `json:"format"`
	}
	if json.Unmarshal(data, &result) != nil || result.Target != intent.target || result.Type != "authenticated" || result.Resource != "image" || result.Format != "jpg" {
		return PrivatePhotoObject{}, Failure("provider_invalid")
	}
	return PrivatePhotoObject{Target: intent.target, Binding: p.AssetBinding(), DeliveryType: "authenticated", Digest: photo.Digest()}, nil
}

func (p *Remote) ReadPersonalPhoto(ctx context.Context, target, digest string) ([]byte, error) {
	if !validPersonalPhotoTarget(target) || !validPersonalPhotoDigest(digest) {
		return nil, Failure("provider_invalid")
	}
	if !p.privatePhotoConfigured() {
		return nil, Failure("provider_unavailable")
	}
	now := time.Now().Unix()
	values := url.Values{"public_id": {target}, "type": {"authenticated"}, "format": {"jpg"}, "timestamp": {strconv.FormatInt(now, 10)}, "expires_at": {strconv.FormatInt(now+60, 10)}}
	// These allowlisted values contain no characters requiring canonical escaping.
	canonical := "expires_at=" + values.Get("expires_at") + "&format=jpg&public_id=" + target + "&timestamp=" + values.Get("timestamp") + "&type=authenticated" + p.credentials.CloudSecret
	signature := sha256.Sum256([]byte(canonical))
	values.Set("api_key", p.credentials.CloudKey)
	values.Set("signature", hex.EncodeToString(signature[:]))
	endpoint, err := url.Parse(p.options.CloudinaryDownloadEndpoint)
	if err != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.User != nil {
		return nil, Failure("provider_unavailable")
	}
	endpoint.RawQuery = values.Encode()
	call, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(call, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, Failure("provider_unavailable")
	}
	response, err := p.options.Client.Do(request)
	// Never return transport errors: their URL may contain the internal signature.
	if err != nil {
		return nil, Failure("provider_unavailable")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, Failure("provider_unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, PersonalPhotoLimit+1))
	if err != nil || len(data) > PersonalPhotoLimit {
		return nil, Failure("provider_invalid")
	}
	photo, err := normalizedPersonalJPEG(data)
	if err != nil || photo.Digest() != digest {
		return nil, Failure("provider_invalid")
	}
	return data, nil
}

func (p *Remote) DeletePersonalPhoto(ctx context.Context, target string) (store.AssetDeletionResult, error) {
	if !validPersonalPhotoTarget(target) {
		return store.AssetDeletionResult{Permanent: true}, Failure("provider_invalid")
	}
	return p.deleteImage(ctx, target, "authenticated")
}
