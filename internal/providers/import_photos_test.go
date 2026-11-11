package providers

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image/jpeg"
	"io"
	"os"
	"testing"
	"time"

	"github.com/gen2brain/h265/heic"
)

func TestImportPhotoAllowedFormatsAndHostileContainers(t *testing.T) {
	for _, format := range []string{"jpeg", "png"} {
		t.Run(format, func(t *testing.T) {
			raw := photoFixture(t, 40, 20, format)
			if format == "jpeg" {
				raw = jpegPhotoExif(raw, photoExif(6))
			} else {
				raw = pngPhotoBeforeEnd(raw, "eXIf", photoExif(6))
			}
			clean, err := NormalizeImportPhoto(raw)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(clean.JPEG(), []byte("canary")) {
				t.Fatal("metadata survived")
			}
			w, h := clean.Dimensions()
			if w != 20 || h != 40 {
				t.Fatal("orientation lost")
			}
			if _, err := jpeg.Decode(bytes.NewReader(clean.JPEG())); err != nil {
				t.Fatal(err)
			}
			for _, bad := range [][]byte{append(bytes.Clone(raw), []byte("<script>polyglot</script>")...), raw[:len(raw)-5]} {
				if _, err := NormalizeImportPhoto(bad); err == nil {
					t.Fatal("truncated/polyglot source accepted")
				}
			}
		})
	}
	png := photoFixture(t, 40, 20, "png")
	for _, raw := range [][]byte{[]byte("GIF89a"), []byte("<svg></svg>"), pngPhotoBeforeEnd(png, "acTL", make([]byte, 8)), bytes.Repeat([]byte("x"), ImportPhotoInputLimit+1)} {
		if _, err := NormalizeImportPhoto(raw); err == nil {
			t.Fatal("invalid source accepted")
		}
	}
	// A HEIC-like header must not be treated as an image without a complete container.
	ftyp := make([]byte, 24)
	binary.BigEndian.PutUint32(ftyp, 24)
	copy(ftyp[4:], "ftypheic")
	copy(ftyp[16:], "mif1heic")
	if _, err := NormalizeImportPhoto(ftyp); err == nil {
		t.Fatal("header-only HEIC accepted")
	}
}

func TestImportPhotoHEICStillMetadataAndOrientation(t *testing.T) {
	for _, orientation := range []uint16{1, 6} {
		var raw bytes.Buffer
		img, err := jpeg.Decode(bytes.NewReader(photoFixture(t, 40, 20, "jpeg")))
		if err != nil {
			t.Fatal(err)
		}
		if err := heic.Encode(&raw, img, heic.EncodeOptions{Quality: 90, Exif: photoExif(orientation), XMP: []byte("<xmp>private-import-metadata-canary</xmp>")}); err != nil {
			t.Fatal(err)
		}
		clean, err := NormalizeImportPhoto(raw.Bytes())
		if err != nil {
			t.Fatal("valid HEIC rejected", err)
		}
		w, h := clean.Dimensions()
		if orientation == 6 && (w != 20 || h != 40) {
			t.Fatal("HEIC orientation lost")
		}
		if bytes.Contains(clean.JPEG(), []byte("canary")) || bytes.Contains(clean.JPEG(), []byte("Exif")) {
			t.Fatal("HEIC metadata survived")
		}
		for _, kind := range []string{"moov", "junk"} {
			box := make([]byte, 8)
			binary.BigEndian.PutUint32(box, 8)
			copy(box[4:], kind)
			if _, err := NormalizeImportPhoto(append(bytes.Clone(raw.Bytes()), box...)); err == nil {
				t.Fatal("movie/polyglot accepted")
			}
		}
		if _, err := NormalizeImportPhoto(raw.Bytes()[:raw.Len()-1]); err == nil {
			t.Fatal("truncated HEIC accepted")
		}
	}
}

func TestImportPhotoProcessHasSeparateInputLimit(t *testing.T) {
	t.Setenv("MYCHEF_CREDENTIALS", "private-import-credential-canary")
	t.Setenv("RESEND_API_KEY", "private-import-email-key-canary")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	normalizer := NewImportPhotoNormalizer(executable)
	normalizer.arguments = []string{"-test.run=^TestImportPhotoNormalizeHelper$"}
	photo, err := normalizer.Normalize(t.Context(), photoFixture(t, 40, 20, "png"))
	if err != nil || photo.Size() == 0 {
		t.Fatal("import child failed", err)
	}
	if _, err := normalizer.Normalize(t.Context(), bytes.Repeat([]byte("x"), ImportPhotoInputLimit+1)); !errors.Is(err, ErrPersonalPhotoSize) {
		t.Fatal("import input limit was not enforced")
	}
	normalizer.arguments = []string{"-test.run=^TestPersonalPhotoNormalizerInterruptHelper$"}
	normalizer.budget = 50 * time.Millisecond
	if _, err := normalizer.Normalize(t.Context(), photoFixture(t, 40, 20, "png")); err == nil {
		t.Fatal("timeout accepted")
	}
}
func TestImportPhotoNormalizeHelper(t *testing.T) {
	if len(os.Args) != 2 || os.Args[1] != "-test.run=^TestImportPhotoNormalizeHelper$" {
		return
	}
	if os.Getenv("MYCHEF_CREDENTIALS") != "" || os.Getenv("RESEND_API_KEY") != "" {
		os.Exit(10)
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, ImportPhotoInputLimit+1))
	if err != nil {
		os.Exit(2)
	}
	photo, err := NormalizeImportPhoto(raw)
	if err != nil {
		os.Exit(2)
	}
	if _, err := os.Stdout.Write(photo.JPEG()); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}
