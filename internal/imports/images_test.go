package imports

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func cleanImportJPEG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, 32, 24)), nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func sourceFiles(t *testing.T, path string) int {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if e.Name() != ".lock" {
			n++
		}
	}
	return n
}
func TestImportImagesPrivateStagingAndCleanup(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sources")
	spool, err := NewImageSpool(dir, DefaultImageLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = spool.Close() }()
	now := time.Now().UTC()
	jpegData := cleanImportJPEG(t)
	calls := 0
	normalize := func(ctx context.Context, raw []byte) ([]byte, error) {
		calls++
		if string(raw) != "private-source-canary" {
			t.Fatal("wrong source")
		}
		return bytes.Clone(jpegData), nil
	}
	batch, err := spool.Stage(context.Background(), []io.Reader{strings.NewReader("private-source-canary")}, normalize, now)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || sourceFiles(t, dir) != 1 {
		t.Fatal("original retained or derivative missing")
	}
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		info, _ := entry.Info()
		if info.Mode().Perm() != 0600 {
			t.Fatal("source is not private")
		}
	}
	info, _ := os.Stat(dir)
	if info.Mode().Perm() != 0700 {
		t.Fatal("source directory is not private")
	}
	data, err := batch.Read(context.Background(), now)
	if err != nil || len(data) != 1 || !bytes.Equal(data[0], jpegData) {
		t.Fatal("invalid private derivative", err)
	}
	if strings.Contains(fmt.Sprint(batch), dir) || strings.Contains(fmt.Sprintf("%#v", batch), "jpg") {
		t.Fatal("handle discloses paths")
	}
	if err := batch.Close(); err != nil {
		t.Fatal(err)
	}
	if sourceFiles(t, dir) != 0 {
		t.Fatal("success cleanup retained files")
	}
	if _, err := batch.Read(context.Background(), now); err == nil {
		t.Fatal("discarded sources readable")
	}
}
func TestImportImagesFailureCancelExpiryAndRestart(t *testing.T) {
	for _, kind := range []string{"normalizer", "invalid-output", "cancel", "oversize", "empty", "too-many", "aggregate"} {
		t.Run(kind, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "sources")
			limits := DefaultImageLimits()
			limits.MaxInputBytes = 32
			limits.MaxAggregateBytes = 40
			spool, err := NewImageSpool(dir, limits)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = spool.Close() }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			readers := []io.Reader{strings.NewReader("private-input")}
			fn := func(context.Context, []byte) ([]byte, error) { return cleanImportJPEG(t), nil }
			switch kind {
			case "normalizer":
				fn = func(context.Context, []byte) ([]byte, error) { return nil, errors.New("secret input body") }
			case "invalid-output":
				fn = func(context.Context, []byte) ([]byte, error) { return []byte("secret output"), nil }
			case "cancel":
				cancel()
			case "oversize":
				readers = []io.Reader{strings.NewReader(strings.Repeat("x", 33))}
			case "empty":
				readers = nil
			case "too-many":
				readers = []io.Reader{strings.NewReader("a"), strings.NewReader("a"), strings.NewReader("a"), strings.NewReader("a")}
			case "aggregate":
				readers = []io.Reader{strings.NewReader(strings.Repeat("x", 30)), strings.NewReader(strings.Repeat("x", 30))}
			}
			if _, err := spool.Stage(ctx, readers, fn, time.Now()); err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatal("unsafe failure", err)
			}
			if sourceFiles(t, dir) != 0 {
				t.Fatal("failure retained sources")
			}
		})
	}
	dir := filepath.Join(t.TempDir(), "sources")
	now := time.Now().UTC()
	spool, err := NewImageSpool(dir, DefaultImageLimits())
	if err != nil {
		t.Fatal(err)
	}
	fn := func(context.Context, []byte) ([]byte, error) { return cleanImportJPEG(t), nil }
	batch, err := spool.Stage(context.Background(), []io.Reader{strings.NewReader("source")}, fn, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := batch.Read(context.Background(), now.Add(time.Hour)); err == nil {
		t.Fatal("expired source readable")
	}
	if err := spool.Sweep(now.Add(time.Hour)); err != nil || sourceFiles(t, dir) != 0 {
		t.Fatal("expiry retained source", err)
	}
	_, err = spool.Stage(context.Background(), []io.Reader{strings.NewReader("source")}, fn, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := spool.Close(); err != nil {
		t.Fatal(err)
	}
	spool, err = NewImageSpool(dir, DefaultImageLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = spool.Close() }()
	if sourceFiles(t, dir) != 0 {
		t.Fatal("startup retained abandoned source")
	}
}
func TestImportImagesSymlinkAndCapacityBoundaries(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "sources")
	spool, err := NewImageSpool(dir, DefaultImageLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewImageSpool(dir, DefaultImageLimits()); err == nil {
		t.Fatal("two owners accepted")
	}
	target := filepath.Join(parent, "must-survive")
	if err := os.WriteFile(target, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, strings.Repeat("a", 64)+".jpg")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := spool.Sweep(time.Now().Add(2 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != "outside" {
		t.Fatal("cleanup followed symlink")
	}
	if err := spool.Close(); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(parent, "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := NewImageSpool(alias, DefaultImageLimits()); err == nil {
		t.Fatal("symlink root accepted")
	}
	if _, err := NewImageSpool(dir, ImageLimits{}); err == nil {
		t.Fatal("unknown bounds accepted")
	}
}

func TestImportImagesCancellationInterruptsBlockedUpload(t *testing.T) {
	spool, err := NewImageSpool(filepath.Join(t.TempDir(), "sources"), DefaultImageLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = spool.Close() }()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reader, writer := io.Pipe()
	defer func() { _ = reader.Close() }()
	defer func() { _ = writer.Close() }()
	done := make(chan error, 1)
	go func() {
		_, err := spool.Stage(ctx, []io.Reader{reader}, func(context.Context, []byte) ([]byte, error) { return cleanImportJPEG(t), nil }, time.Now())
		done <- err
	}()
	time.AfterFunc(20*time.Millisecond, cancel)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancel accepted")
		}
	case <-time.After(500 * time.Millisecond):
		_ = reader.Close()
		t.Fatal("cancel left source upload blocked", <-done)
	}
}
func TestImportImagesRejectNormalizerMetadata(t *testing.T) {
	data := cleanImportJPEG(t)
	// APP0 can also contain arbitrary untrusted data; our child writes none.
	body := []byte("private-normalizer-metadata-canary")
	segment := []byte{0xff, 0xe0, 0, byte(len(body) + 2)}
	segment = append(segment, body...)
	bad := append(bytes.Clone(data[:2]), segment...)
	bad = append(bad, data[2:]...)
	if validCleanRecipeImage(bad) {
		t.Fatal("normalizer metadata was accepted")
	}
}
func TestImportImagesStartupRemovesAbandonedSource(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sources")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, strings.Repeat("b", 64)+".upload"), []byte("abandoned-source-canary"), 0600); err != nil {
		t.Fatal(err)
	}
	spool, err := NewImageSpool(dir, DefaultImageLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = spool.Close() }()
	if sourceFiles(t, dir) != 0 {
		t.Fatal("abandoned source survived startup")
	}
}

func TestImportImagesRejectMetadataAfterScan(t *testing.T) {
	data := cleanImportJPEG(t)
	payload := []byte("Exif\x00\x00private-late-metadata-canary")
	segment := []byte{0xff, 0xe1, 0, byte(len(payload) + 2)}
	segment = append(segment, payload...)
	bad := append(bytes.Clone(data[:len(data)-2]), segment...)
	bad = append(bad, data[len(data)-2:]...)
	if validCleanRecipeImage(bad) {
		t.Fatal("metadata after image scan accepted for worker dispatch")
	}
}

func TestImportImagesStreamingPartsShareAggregateLimit(t *testing.T) {
	limits := DefaultImageLimits()
	limits.MaxInputBytes = 32
	limits.MaxAggregateBytes = 40
	spool, err := NewImageSpool(filepath.Join(t.TempDir(), "sources"), limits)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = spool.Close() }()
	index := 0
	next := func() (io.Reader, error) {
		if index == 2 {
			return nil, io.EOF
		}
		index++
		return strings.NewReader(strings.Repeat("x", 30)), nil
	}
	if _, err := spool.StageStream(t.Context(), next, func(context.Context, []byte) ([]byte, error) { return cleanImportJPEG(t), nil }, time.Now()); err == nil {
		t.Fatal("multipart aggregate cap reset between parts")
	}
}
