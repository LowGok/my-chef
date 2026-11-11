package providers

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"io"
	"strconv"
	"strings"
)

func personalPhotoMetadata(data []byte) (string, int, error) {
	if len(data) < 12 {
		return "", 0, ErrPersonalPhotoInvalid
	}
	orientation := 0
	merge := func(value int) error {
		if value == 0 {
			return nil
		}
		if value < 1 || value > 8 || orientation != 0 && orientation != value {
			return ErrPersonalPhotoInvalid
		}
		orientation = value
		return nil
	}
	exif := func(value []byte) error {
		value = bytes.TrimPrefix(value, []byte("Exif\x00\x00"))
		result, err := photoExifOrientation(value)
		if err != nil {
			return err
		}
		return merge(result)
	}
	xmp := func(value []byte) error {
		result, err := photoXMPOrientation(value)
		if err != nil {
			return err
		}
		return merge(result)
	}
	format := ""
	switch {
	case bytes.HasPrefix(data, []byte{0xff, 0xd8}):
		format = "jpeg"
		pos, scan, finished := 2, false, false
		for pos < len(data) {
			if scan && data[pos] != 0xff {
				pos++
				continue
			}
			if data[pos] != 0xff {
				return "", 0, ErrPersonalPhotoInvalid
			}
			for pos < len(data) && data[pos] == 0xff {
				pos++
			}
			if pos == len(data) {
				return "", 0, ErrPersonalPhotoInvalid
			}
			marker := data[pos]
			pos++
			if scan && (marker == 0 || marker >= 0xd0 && marker <= 0xd7) {
				continue
			}
			if marker == 0xd9 {
				finished = pos == len(data)
				break
			}
			if marker == 0xd8 || marker == 0 || pos+2 > len(data) {
				return "", 0, ErrPersonalPhotoInvalid
			}
			size := int(binary.BigEndian.Uint16(data[pos:]))
			if size < 2 || size > len(data)-pos {
				return "", 0, ErrPersonalPhotoInvalid
			}
			payload := data[pos+2 : pos+size]
			if marker == 0xe1 {
				// Split extended-XMP cannot be safely reconstructed here; do not
				// silently discard orientation that may be in a later segment.
				if bytes.HasPrefix(payload, []byte("http://ns.adobe.com/xmp/extension/\x00")) {
					return "", 0, ErrPersonalPhotoInvalid
				}
				if bytes.HasPrefix(payload, []byte("Exif\x00\x00")) {
					if err := exif(payload); err != nil {
						return "", 0, err
					}
				}
				const xmpPrefix = "http://ns.adobe.com/xap/1.0/\x00"
				if bytes.HasPrefix(payload, []byte(xmpPrefix)) {
					if err := xmp(payload[len(xmpPrefix):]); err != nil {
						return "", 0, err
					}
				}
			}
			pos += size
			scan = marker == 0xda
		}
		if !finished {
			return "", 0, ErrPersonalPhotoInvalid
		}
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		format = "png"
		pos, finished := 8, false
		for pos+12 <= len(data) {
			size := uint64(binary.BigEndian.Uint32(data[pos:]))
			if size > uint64(len(data)-pos-12) {
				return "", 0, ErrPersonalPhotoInvalid
			}
			end := pos + 8 + int(size)
			kind, value := string(data[pos+4:pos+8]), data[pos+8:end]
			switch kind {
			case "acTL", "fcTL", "fdAT":
				return "", 0, ErrPersonalPhotoInvalid
			case "eXIf":
				if err := exif(value); err != nil {
					return "", 0, err
				}
			case "iTXt":
				if bytes.HasPrefix(value, []byte("XML:com.adobe.xmp\x00")) {
					// Compressed XMP is rejected; it must not hide orientation from normalization.
					parts := bytes.SplitN(value, []byte{0}, 6)
					if len(parts) != 6 || len(parts[1]) != 0 || len(parts[2]) != 0 {
						return "", 0, ErrPersonalPhotoInvalid
					}
					if err := xmp(parts[5]); err != nil {
						return "", 0, err
					}
				}
			case "IEND":
				finished = end+4 == len(data) && size == 0
			}
			pos = end + 4
			if kind == "IEND" {
				break
			}
		}
		if !finished {
			return "", 0, ErrPersonalPhotoInvalid
		}
	case bytes.HasPrefix(data, []byte("RIFF")) && string(data[8:12]) == "WEBP":
		format = "webp"
		if uint64(binary.LittleEndian.Uint32(data[4:8]))+8 != uint64(len(data)) {
			return "", 0, ErrPersonalPhotoInvalid
		}
		pos := 12
		for pos+8 <= len(data) {
			size := uint64(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))
			if size > uint64(len(data)-pos-8) {
				return "", 0, ErrPersonalPhotoInvalid
			}
			kind, value := string(data[pos:pos+4]), data[pos+8:pos+8+int(size)]
			switch kind {
			case "ANIM", "ANMF":
				return "", 0, ErrPersonalPhotoInvalid
			case "VP8X":
				if len(value) != 10 || value[0]&0xc3 != 0 || value[1] != 0 || value[2] != 0 || value[3] != 0 {
					return "", 0, ErrPersonalPhotoInvalid
				}
			case "EXIF":
				if err := exif(value); err != nil {
					return "", 0, err
				}
			case "XMP ":
				if err := xmp(value); err != nil {
					return "", 0, err
				}
			}
			pos += 8 + int(size) + int(size&1)
		}
		if pos != len(data) {
			return "", 0, ErrPersonalPhotoInvalid
		}
	default:
		return "", 0, ErrPersonalPhotoInvalid
	}
	if orientation == 0 {
		orientation = 1
	}
	return format, orientation, nil
}

func photoExifOrientation(data []byte) (int, error) {
	if len(data) < 8 {
		return 0, ErrPersonalPhotoInvalid
	}
	var order binary.ByteOrder
	switch string(data[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 0, ErrPersonalPhotoInvalid
	}
	if order.Uint16(data[2:4]) != 42 {
		return 0, ErrPersonalPhotoInvalid
	}
	start := uint64(order.Uint32(data[4:8]))
	if start < 8 || start+2 > uint64(len(data)) {
		return 0, ErrPersonalPhotoInvalid
	}
	count := uint64(order.Uint16(data[start : start+2]))
	if count > 256 || start+2+count*12+4 > uint64(len(data)) {
		return 0, ErrPersonalPhotoInvalid
	}
	result := 0
	for n := uint64(0); n < count; n++ {
		entry := data[start+2+n*12 : start+2+(n+1)*12]
		if order.Uint16(entry[:2]) != 0x112 {
			continue
		}
		if order.Uint16(entry[2:4]) != 3 || order.Uint32(entry[4:8]) != 1 {
			return 0, ErrPersonalPhotoInvalid
		}
		value := int(order.Uint16(entry[8:10]))
		if value < 1 || value > 8 || result != 0 && result != value {
			return 0, ErrPersonalPhotoInvalid
		}
		result = value
	}
	return result, nil
}

func photoXMPOrientation(data []byte) (int, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	result := 0
	accept := func(text string) error {
		value, err := strconv.Atoi(strings.TrimSpace(text))
		if err != nil || value < 1 || value > 8 || result != 0 && result != value {
			return ErrPersonalPhotoInvalid
		}
		result = value
		return nil
	}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, ErrPersonalPhotoInvalid
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		for _, attribute := range start.Attr {
			if attribute.Name.Local == "Orientation" {
				if err := accept(attribute.Value); err != nil {
					return 0, err
				}
			}
		}
		if start.Name.Local == "Orientation" {
			var value string
			if decoder.DecodeElement(&value, &start) != nil || len(value) > 32 {
				return 0, ErrPersonalPhotoInvalid
			}
			if err := accept(value); err != nil {
				return 0, err
			}
		}
	}
	return result, nil
}
