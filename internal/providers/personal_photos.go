package providers

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	_ "image/png"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const (
	PersonalPhotoInputLimit = 12 << 20
	PersonalPhotoLimit      = 2 << 20
)

var (
	ErrPersonalPhotoInvalid = errors.New("choose a supported JPEG, PNG or static WebP photo")
	ErrPersonalPhotoSize    = errors.New("choose a photo within the size and resolution limits")
	ErrPersonalPhotoProcess = errors.New("the photo could not be processed; try a smaller file")
)

type NormalizedPhoto struct {
	data          []byte
	width, height int
	digest        string
}

func (p NormalizedPhoto) JPEG() []byte           { return bytes.Clone(p.data) }
func (p NormalizedPhoto) Size() int              { return len(p.data) }
func (p NormalizedPhoto) Dimensions() (int, int) { return p.width, p.height }
func (p NormalizedPhoto) Digest() string         { return p.digest }
func (NormalizedPhoto) String() string           { return "normalized private photo" }
func (NormalizedPhoto) GoString() string         { return "normalized private photo" }

// NormalizePersonalPhoto runs inside the bounded decoding child in the runtime.
// It never retains filenames or copies metadata into the resulting JPEG.
func NormalizePersonalPhoto(data []byte) (out NormalizedPhoto, err error) {
	defer func() {
		if recover() != nil {
			out, err = NormalizedPhoto{}, ErrPersonalPhotoInvalid
		}
	}()
	if len(data) > PersonalPhotoInputLimit {
		return out, ErrPersonalPhotoSize
	}
	format, orientation, err := personalPhotoMetadata(data)
	if err != nil {
		return out, err
	}
	config, detected, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || detected != format {
		return out, ErrPersonalPhotoInvalid
	}
	if config.Width < 1 || config.Height < 1 || config.Width > 8192 || config.Height > 8192 || int64(config.Width)*int64(config.Height) > 20_000_000 {
		return out, ErrPersonalPhotoSize
	}
	decoded, detected, err := image.Decode(bytes.NewReader(data))
	if err != nil || detected != format || decoded.Bounds().Dx() != config.Width || decoded.Bounds().Dy() != config.Height {
		return out, ErrPersonalPhotoInvalid
	}
	return normalizePersonalPixels(decoded, orientation)
}

func normalizePersonalPixels(decoded image.Image, orientation int) (NormalizedPhoto, error) {
	out := NormalizedPhoto{}
	width, height := decoded.Bounds().Dx(), decoded.Bounds().Dy()
	longest := max(width, height)
	if longest > 2048 {
		width, height = max(1, width*2048/longest), max(1, height*2048/longest)
	}
	scaled := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(scaled, scaled.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	xdraw.ApproxBiLinear.Scale(scaled, scaled.Bounds(), decoded, decoded.Bounds(), draw.Over, nil)
	if orientation != 1 {
		w, h := width, height
		if orientation >= 5 {
			w, h = height, width
		}
		rotated := image.NewRGBA(image.Rect(0, 0, w, h))
		for y := range height {
			for x := range width {
				dx, dy := x, y
				switch orientation {
				case 2:
					dx = width - 1 - x
				case 3:
					dx, dy = width-1-x, height-1-y
				case 4:
					dy = height - 1 - y
				case 5:
					dx, dy = y, x
				case 6:
					dx, dy = height-1-y, x
				case 7:
					dx, dy = height-1-y, width-1-x
				case 8:
					dx, dy = y, width-1-x
				}
				rotated.SetRGBA(dx, dy, scaled.RGBAAt(x, y))
			}
		}
		scaled, width, height = rotated, w, h
	}
	for _, quality := range []int{85, 75, 65, 55, 45} {
		var output bytes.Buffer
		if jpeg.Encode(&output, scaled, &jpeg.Options{Quality: quality}) != nil {
			return out, ErrPersonalPhotoProcess
		}
		if output.Len() > PersonalPhotoLimit {
			continue
		}
		encoded := output.Bytes()
		hash := sha256.Sum256(encoded)
		return NormalizedPhoto{data: encoded, width: width, height: height, digest: hex.EncodeToString(hash[:])}, nil
	}
	return out, ErrPersonalPhotoSize
}
