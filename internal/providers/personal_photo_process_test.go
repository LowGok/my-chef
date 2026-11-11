package providers

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"
)

func TestPersonalPhotoNormalizerHelper(t *testing.T) {
	if len(os.Args) != 2 || os.Args[1] != "-test.run=^TestPersonalPhotoNormalizerHelper$" {
		return
	}
	if os.Getenv("MYCHEF_CREDENTIALS") != "" || os.Getenv("RESEND_API_KEY") != "" {
		os.Exit(10)
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, PersonalPhotoInputLimit+1))
	if err != nil {
		os.Exit(2)
	}
	photo, err := NormalizePersonalPhoto(raw)
	if err != nil {
		os.Exit(2)
	}
	if _, err := os.Stdout.Write(photo.JPEG()); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestPersonalPhotoNormalizerInterruptHelper(t *testing.T) {
	if len(os.Args) != 2 || os.Args[1] != "-test.run=^TestPersonalPhotoNormalizerInterruptHelper$" {
		return
	}
	time.Sleep(time.Minute)
	os.Exit(1)
}

func TestPersonalPhotoProcessBoundsCancellationAndEnvironment(t *testing.T) {
	t.Setenv("MYCHEF_CREDENTIALS", "private-credential-path-canary")
	t.Setenv("RESEND_API_KEY", "private-provider-credential-canary")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	normalizer := NewPersonalPhotoNormalizer(executable)
	normalizer.arguments = []string{"-test.run=^TestPersonalPhotoNormalizerHelper$"}
	photo, err := normalizer.Normalize(t.Context(), photoFixture(t, 40, 20, "png"))
	if err != nil || photo.Size() == 0 {
		t.Fatal("bounded child normalization failed", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := normalizer.Normalize(ctx, photoFixture(t, 40, 20, "png")); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled normalization continued", err)
	}
	normalizer.arguments = []string{"-test.run=^TestPersonalPhotoNormalizerInterruptHelper$"}
	normalizer.budget = 100 * time.Millisecond
	start := time.Now()
	if _, err := normalizer.Normalize(t.Context(), photoFixture(t, 40, 20, "png")); err == nil || time.Since(start) > 5*time.Second {
		t.Fatal("decoder was not interrupted within its budget")
	}
	if _, err := normalizer.Normalize(t.Context(), bytes.Repeat([]byte{0}, PersonalPhotoInputLimit+1)); !errors.Is(err, ErrPersonalPhotoSize) {
		t.Fatal("oversized child input accepted", err)
	}
}

func TestPersonalPhotoProcessOutputCannotBypassByteLimit(t *testing.T) {
	output := &photoOutput{}
	_, err := io.Copy(output, io.LimitReader(bytes.NewReader(make([]byte, PersonalPhotoLimit+1024)), PersonalPhotoLimit+1024))
	if err == nil || output.Len() > PersonalPhotoLimit {
		t.Fatal("decoder stream bypassed its output byte limit")
	}
}
