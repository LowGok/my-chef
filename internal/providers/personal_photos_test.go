package providers

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func photoFixture(t *testing.T, width, height int, format string) []byte {
	t.Helper()
	im := image.NewNRGBA(image.Rect(0, 0, width, height))
	colors := []color.NRGBA{{255, 0, 0, 255}, {0, 0, 255, 255}, {0, 255, 0, 255}, {255, 255, 255, 255}}
	for y := range height {
		for x := range width {
			i := 0
			if x >= width/2 {
				i++
			}
			if y >= height/2 {
				i += 2
			}
			im.SetNRGBA(x, y, colors[i])
		}
	}
	var out bytes.Buffer
	var err error
	if format == "png" {
		err = png.Encode(&out, im)
	} else {
		err = jpeg.Encode(&out, im, &jpeg.Options{Quality: 100})
	}
	if err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func photoExif(orientation uint16) []byte {
	// Synthetic TIFF metadata contains orientation plus a private-description canary.
	value := []byte("personal-photo-metadata-canary\x00")
	data := make([]byte, 38+len(value))
	copy(data, "II")
	binary.LittleEndian.PutUint16(data[2:], 42)
	binary.LittleEndian.PutUint32(data[4:], 8)
	binary.LittleEndian.PutUint16(data[8:], 2)
	binary.LittleEndian.PutUint16(data[10:], 0x112)
	binary.LittleEndian.PutUint16(data[12:], 3)
	binary.LittleEndian.PutUint32(data[14:], 1)
	binary.LittleEndian.PutUint16(data[18:], orientation)
	binary.LittleEndian.PutUint16(data[22:], 0x10e)
	binary.LittleEndian.PutUint16(data[24:], 2)
	binary.LittleEndian.PutUint32(data[26:], uint32(len(value)))
	binary.LittleEndian.PutUint32(data[30:], 38)
	copy(data[38:], value)
	return data
}

func jpegPhotoExif(data, exif []byte) []byte {
	payload := append([]byte("Exif\x00\x00"), exif...)
	segment := []byte{0xff, 0xe1, 0, 0}
	binary.BigEndian.PutUint16(segment[2:], uint16(len(payload)+2))
	out := append([]byte{}, data[:2]...)
	out = append(out, segment...)
	out = append(out, payload...)
	return append(out, data[2:]...)
}

func photoPNGChunk(kind string, payload []byte) []byte {
	out := make([]byte, 12+len(payload))
	binary.BigEndian.PutUint32(out, uint32(len(payload)))
	copy(out[4:], kind)
	copy(out[8:], payload)
	binary.BigEndian.PutUint32(out[8+len(payload):], crc32.ChecksumIEEE(out[4:8+len(payload)]))
	return out
}

func pngPhotoBeforeEnd(data []byte, kind string, payload []byte) []byte {
	out := append([]byte{}, data[:len(data)-12]...)
	out = append(out, photoPNGChunk(kind, payload)...)
	return append(out, data[len(data)-12:]...)
}

func photoColor(im image.Image, x, y int) byte {
	r, g, b, _ := im.At(x, y).RGBA()
	switch {
	case r > 45000 && g > 45000 && b > 45000:
		return 'W'
	case r > g*2 && r > b*2:
		return 'R'
	case g > r*2 && g > b*2:
		return 'G'
	case b > r*2 && b > g*2:
		return 'B'
	default:
		return '?'
	}
}

func TestPersonalPhotoReencodesOrientationAndRemovesMetadata(t *testing.T) {
	for i, corners := range []string{"RBGW", "BRWG", "WGBR", "GWRB", "RGBW", "GRWB", "WBGR", "BWRG"} {
		raw := jpegPhotoExif(photoFixture(t, 40, 20, "jpeg"), photoExif(uint16(i+1)))
		photo, err := NormalizePersonalPhoto(raw)
		if err != nil {
			t.Fatal("valid oriented JPEG rejected", i+1, err)
		}
		encoded := photo.JPEG()
		if bytes.Contains(encoded, []byte("personal-photo-metadata-canary")) || bytes.Contains(encoded, []byte("Exif")) {
			t.Fatal("source metadata survived normalization")
		}
		decoded, err := jpeg.Decode(bytes.NewReader(encoded))
		if err != nil {
			t.Fatal(err)
		}
		width, height := 40, 20
		if i+1 >= 5 {
			width, height = 20, 40
		}
		if decoded.Bounds().Dx() != width || decoded.Bounds().Dy() != height {
			t.Fatal("orientation dimensions wrong", i+1)
		}
		got := []byte{photoColor(decoded, width/4, height/4), photoColor(decoded, 3*width/4, height/4), photoColor(decoded, width/4, 3*height/4), photoColor(decoded, 3*width/4, 3*height/4)}
		if string(got) != corners {
			t.Fatal("orientation pixels wrong", i+1, string(got), corners)
		}
		hash := sha256.Sum256(encoded)
		if photo.Digest() != hex.EncodeToString(hash[:]) || photo.Size() != len(encoded) {
			t.Fatal("normalized identity does not match bytes")
		}
		encoded[0] = 0
		if photo.JPEG()[0] == 0 {
			t.Fatal("caller mutated normalized private bytes")
		}
	}
	input := pngPhotoBeforeEnd(photoFixture(t, 40, 20, "png"), "eXIf", photoExif(6))
	photo, err := NormalizePersonalPhoto(input)
	if err != nil {
		t.Fatal("PNG metadata/orientation rejected", err)
	}
	width, height := photo.Dimensions()
	if width != 20 || height != 40 || bytes.Contains(photo.JPEG(), []byte("personal-photo-metadata-canary")) {
		t.Fatal("PNG metadata/orientation mishandled")
	}
}

func TestPersonalPhotoFormatsResizeAndMalformedBounds(t *testing.T) {
	// A 2x2 color grid encoded locally with libwebp's lossless encoder.
	webp, err := base64.StdEncoding.DecodeString("UklGRiwAAABXRUJQVlA4TB8AAAAvAUAAAB8gEEjeHzqN+RcQFPwf3fxHZA/gAg0R/Q8BAA==")
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{photoFixture(t, 40, 20, "jpeg"), photoFixture(t, 40, 20, "png"), webp} {
		photo, err := NormalizePersonalPhoto(raw)
		if err != nil {
			t.Fatal("supported format rejected", err)
		}
		if _, err := jpeg.Decode(bytes.NewReader(photo.JPEG())); err != nil {
			t.Fatal("output is not JPEG", err)
		}
	}
	photo, err := NormalizePersonalPhoto(photoFixture(t, 2400, 1200, "jpeg"))
	if err != nil {
		t.Fatal(err)
	}
	w, h := photo.Dimensions()
	if w != 2048 || h != 1024 || photo.Size() > 2<<20 {
		t.Fatal("normalized size limit not applied")
	}
	validPNG := photoFixture(t, 2, 2, "png")
	bomb := append([]byte{}, validPNG...)
	binary.BigEndian.PutUint32(bomb[16:], 5000)
	binary.BigEndian.PutUint32(bomb[20:], 5000)
	binary.BigEndian.PutUint32(bomb[29:], crc32.ChecksumIEEE(bomb[12:29]))
	animatedPNG := pngPhotoBeforeEnd(validPNG, "acTL", make([]byte, 8))
	animatedWebP := append([]byte{}, webp...)
	animatedWebP = append(animatedWebP, []byte("ANIM\x00\x00\x00\x00")...)
	binary.LittleEndian.PutUint32(animatedWebP[4:], uint32(len(animatedWebP)-8))
	badExif := photoExif(9)
	for _, raw := range [][]byte{nil, []byte("<svg onload='private-canary'/>"), []byte("GIF89a"), []byte("\x00\x00\x00\x18ftypheic"), bytes.Repeat([]byte{0}, (12<<20)+1), validPNG[:len(validPNG)-5], bomb, animatedPNG, animatedWebP, jpegPhotoExif(photoFixture(t, 40, 20, "jpeg"), badExif)} {
		if _, err := NormalizePersonalPhoto(raw); err == nil {
			t.Fatal("malformed, unsupported or oversized photo accepted")
		}
	}
	for n := 0; n < 64; n++ {
		if _, err := NormalizePersonalPhoto(validPNG[:min(n, len(validPNG)-1)]); err == nil {
			t.Fatal("truncated photo accepted")
		}
	}
}

func TestPersonalPhotoRejectsUnresolvedAndConflictingOrientation(t *testing.T) {
	jpegInput := photoFixture(t, 40, 20, "jpeg")
	appendAPP1 := func(input, payload []byte) []byte {
		segment := []byte{0xff, 0xe1, 0, 0}
		binary.BigEndian.PutUint16(segment[2:], uint16(len(payload)+2))
		out := append([]byte{}, input[:2]...)
		out = append(out, segment...)
		out = append(out, payload...)
		return append(out, input[2:]...)
	}
	xmp := []byte("http://ns.adobe.com/xap/1.0/\x00<root xmlns:tiff='http://ns.adobe.com/tiff/1.0/' tiff:Orientation='6'/>")
	photo, err := NormalizePersonalPhoto(appendAPP1(jpegInput, xmp))
	if err != nil {
		t.Fatal("supported XMP orientation rejected", err)
	}
	w, h := photo.Dimensions()
	if w != 20 || h != 40 {
		t.Fatal("XMP rotation was ignored")
	}
	for _, input := range [][]byte{
		appendAPP1(jpegInput, []byte("http://ns.adobe.com/xmp/extension/\x00unresolved-orientation")),
		appendAPP1(jpegPhotoExif(jpegInput, photoExif(3)), xmp),
		appendAPP1(jpegInput, []byte("http://ns.adobe.com/xap/1.0/\x00<root Orientation='0'/>")),
	} {
		if _, err := NormalizePersonalPhoto(input); err == nil {
			t.Fatal("unresolved or conflicting orientation accepted")
		}
	}
	// The same orientation must also work for big-endian EXIF.
	exif := make([]byte, 26)
	copy(exif, "MM")
	binary.BigEndian.PutUint16(exif[2:], 42)
	binary.BigEndian.PutUint32(exif[4:], 8)
	binary.BigEndian.PutUint16(exif[8:], 1)
	binary.BigEndian.PutUint16(exif[10:], 0x112)
	binary.BigEndian.PutUint16(exif[12:], 3)
	binary.BigEndian.PutUint32(exif[14:], 1)
	binary.BigEndian.PutUint16(exif[18:], 6)
	photo, err = NormalizePersonalPhoto(jpegPhotoExif(jpegInput, exif))
	w, h = photo.Dimensions()
	if err != nil || w != 20 || h != 40 {
		t.Fatal("big-endian EXIF not applied", err)
	}
	// A single oversize edge is disallowed even below the total-pixel cap.
	pngInput := photoFixture(t, 2, 2, "png")
	binary.BigEndian.PutUint32(pngInput[16:], 8193)
	binary.BigEndian.PutUint32(pngInput[29:], crc32.ChecksumIEEE(pngInput[12:29]))
	if _, err := NormalizePersonalPhoto(pngInput); err != ErrPersonalPhotoSize {
		t.Fatal("edge limit bypassed", err)
	}
}
