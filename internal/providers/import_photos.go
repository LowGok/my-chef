package providers

import (
	"bytes"
	"encoding/binary"
	"errors"

	"github.com/gen2brain/h265/heic"
)

const ImportPhotoInputLimit = 10 << 20

var ErrImportPhotoInvalid = errors.New("choose a supported JPEG, PNG or HEIC recipe-page photo")

// NormalizeImportPhoto is called only in the bounded decoder child. Source
// metadata, names and original encodings are never returned to the web app.
func NormalizeImportPhoto(data []byte) (out NormalizedPhoto, err error) {
	defer func() {
		if recover() != nil {
			out, err = NormalizedPhoto{}, ErrImportPhotoInvalid
		}
	}()
	if len(data) == 0 || len(data) > ImportPhotoInputLimit {
		return out, ErrPersonalPhotoSize
	}
	if bytes.HasPrefix(data, []byte{0xff, 0xd8}) || bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
		return NormalizePersonalPhoto(data)
	}
	transform, e := importHEICContainer(data)
	if e != nil {
		return out, e
	}
	config, e := heic.DecodeConfig(bytes.NewReader(data))
	if e != nil || !importPhotoDimensions(config.Width, config.Height) {
		return out, ErrImportPhotoInvalid
	}
	orientation := 0
	merge := func(value int) bool {
		if value == 0 {
			return true
		}
		if orientation != 0 && orientation != value {
			return false
		}
		orientation = value
		return true
	}
	if raw, e := heic.RawExif(bytes.NewReader(data)); e == nil {
		value, e := photoExifOrientation(raw)
		if e != nil || !merge(value) {
			return out, ErrImportPhotoInvalid
		}
	} else if !errors.Is(e, heic.ErrNoExif) {
		return out, ErrImportPhotoInvalid
	}
	if raw, e := heic.RawXMP(bytes.NewReader(data)); e == nil {
		value, e := photoXMPOrientation(raw)
		if e != nil || !merge(value) {
			return out, ErrImportPhotoInvalid
		}
	} else if !errors.Is(e, heic.ErrNoXMP) {
		return out, ErrImportPhotoInvalid
	}
	decoded, e := heic.Decode(bytes.NewReader(data), heic.Options{AutoRotate: true, FrameSizeLimit: 20_000_000, MaxFrames: 1, Threads: 1})
	if e != nil || !importPhotoDimensions(decoded.Bounds().Dx(), decoded.Bounds().Dy()) {
		return out, ErrImportPhotoInvalid
	}
	// HEIF item transforms take precedence over the EXIF tag. Applying both
	// would rotate common camera photos twice. Otherwise use EXIF/XMP as JPEG does.
	if transform || orientation == 0 {
		orientation = 1
	}
	return normalizePersonalPixels(decoded, orientation)
}
func importPhotoDimensions(w, h int) bool {
	return w > 0 && h > 0 && w <= 8192 && h <= 8192 && int64(w)*int64(h) <= 20_000_000
}

// Accept a single HEVC still container, not AVIF, an image sequence, appended
// document, zero-sized/unbounded box or movie. Unknown top-level boxes fail closed.
func importHEICContainer(data []byte) (bool, error) {
	if len(data) < 24 || string(data[4:8]) != "ftyp" {
		return false, ErrImportPhotoInvalid
	}
	ftyp, meta, mdat := 0, 0, 0
	transform := false
	boxes := 0
	var visit func([]byte, string, int) error
	visit = func(raw []byte, parent string, depth int) error {
		if depth > 4 {
			return ErrImportPhotoInvalid
		}
		for len(raw) > 0 {
			boxes++
			if boxes > 2048 || len(raw) < 8 {
				return ErrImportPhotoInvalid
			}
			size := uint64(binary.BigEndian.Uint32(raw))
			header := 8
			kind := string(raw[4:8])
			if size == 1 {
				if len(raw) < 16 {
					return ErrImportPhotoInvalid
				}
				size = binary.BigEndian.Uint64(raw[8:16])
				header = 16
			}
			if size < uint64(header) || size > uint64(len(raw)) {
				return ErrImportPhotoInvalid
			}
			body := raw[header:int(size)]
			if parent == "" {
				switch kind {
				case "ftyp":
					ftyp++
					if ftyp != 1 || len(body) < 12 || len(body)%4 != 0 {
						return ErrImportPhotoInvalid
					}
					accepted := false
					for i := 0; i < len(body); i += 4 {
						if i == 4 {
							continue
						}
						brand := string(body[i : i+4])
						switch brand {
						case "heic", "heix":
							accepted = true
						case "mif1", "miaf":
						default:
							return ErrImportPhotoInvalid
						}
					}
					if !accepted {
						return ErrImportPhotoInvalid
					}
				case "meta":
					meta++
					if meta != 1 || len(body) < 4 || !bytes.Equal(body[:4], []byte{0, 0, 0, 0}) {
						return ErrImportPhotoInvalid
					}
					if err := visit(body[4:], "meta", depth+1); err != nil {
						return err
					}
				case "mdat":
					mdat++
					if len(body) == 0 {
						return ErrImportPhotoInvalid
					}
				case "free", "skip":
				default:
					return ErrImportPhotoInvalid
				}
			} else if parent == "meta" && kind == "iprp" || parent == "iprp" && kind == "ipco" {
				if err := visit(body, kind, depth+1); err != nil {
					return err
				}
			} else if parent == "ipco" && (kind == "irot" || kind == "imir") {
				if len(body) != 1 {
					return ErrImportPhotoInvalid
				}
				transform = true
			}
			raw = raw[int(size):]
		}
		return nil
	}
	if err := visit(data, "", 0); err != nil || ftyp != 1 || meta != 1 || mdat < 1 {
		return false, ErrImportPhotoInvalid
	}
	return transform, nil
}
