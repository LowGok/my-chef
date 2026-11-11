package imports

import (
	"bytes"
	"io"
	"mime"
	"strings"
	"unicode"
	"unicode/utf8"

	nethtml "golang.org/x/net/html"
)

type recipeHTMLScan struct {
	scripts [][]byte
	text    string
	invalid bool
}

type recipeHTMLFrame struct {
	tag                          string
	skipped, primary, structured bool
}

type recipeText struct {
	builder  strings.Builder
	full     bool
	space    bool
	preserve bool
}

func (b *recipeText) add(data []byte) {
	if b.full {
		return
	}
	for len(data) != 0 {
		r, size := utf8.DecodeRune(data)
		data = data[size:]
		if b.preserve && (r == '\n' || r == '\r') {
			b.line()
			continue
		}
		if unicode.IsSpace(r) {
			b.space = b.builder.Len() != 0
			continue
		}
		needed := utf8.RuneLen(r)
		if b.space {
			needed++
		}
		if b.builder.Len()+needed > maxRecipeFallbackBytes {
			b.full = true
			return
		}
		if b.space {
			b.builder.WriteByte(' ')
			b.space = false
		}
		b.builder.WriteRune(r)
	}
}

func (b *recipeText) line() {
	if !b.full && b.builder.Len() != 0 {
		if b.builder.Len()+1 > maxRecipeFallbackBytes {
			b.full = true
			return
		}
		b.builder.WriteByte('\n')
		b.space = false
	}
}

func scanRecipeHTML(data []byte, structured bool) (recipeHTMLScan, error) {
	if len(data) > maxRecipeHTMLBytes || !utf8.Valid(data) {
		return recipeHTMLScan{}, ErrImportInput
	}
	z := nethtml.NewTokenizer(bytes.NewReader(data))
	z.SetMaxBuf(maxRecipeHTMLBytes)
	frames := []recipeHTMLFrame{}
	visible, primary := recipeText{preserve: !structured}, recipeText{preserve: !structured}
	result := recipeHTMLScan{scripts: [][]byte{}}
	jsonBytes, tokens := 0, 0
	for {
		tokens++
		if tokens > 100000 {
			return recipeHTMLScan{}, ErrImportInput
		}
		kind := z.Next()
		switch kind {
		case nethtml.ErrorToken:
			if z.Err() != io.EOF {
				return recipeHTMLScan{}, ErrImportInput
			}
			selected := &visible
			if primary.builder.Len() > 0 {
				selected = &primary
			}
			if !selected.full {
				result.text = strings.TrimSpace(selected.builder.String())
			}
			return result, nil
		case nethtml.TextToken:
			if len(frames) != 0 && frames[len(frames)-1].structured && structured {
				text := z.Text()
				if len(bytes.TrimSpace(text)) == 0 {
					continue
				}
				if len(text) > 256<<10 || len(result.scripts) >= 16 || jsonBytes+len(text) > 1<<20 {
					result.invalid = true
					continue
				}
				result.scripts = append(result.scripts, bytes.Clone(text))
				jsonBytes += len(text)
				continue
			}
			if len(frames) > 0 && frames[len(frames)-1].skipped {
				continue
			}
			text := z.Text()
			visible.add(text)
			if len(frames) > 0 && frames[len(frames)-1].primary {
				primary.add(text)
			}
		case nethtml.StartTagToken, nethtml.SelfClosingTagToken:
			name, more := z.TagName()
			tag := string(name)
			attributes := map[string]string{}
			for count := 0; more; count++ {
				if count >= 64 {
					return recipeHTMLScan{}, ErrImportInput
				}
				key, value, next := z.TagAttr()
				more = next
				switch string(key) {
				case "hidden", "aria-hidden", "style", "class", "type", "itemtype":
					if _, seen := attributes[string(key)]; seen {
						continue
					}
					if len(value) > 2048 {
						attributes["hidden"] = ""
					} else {
						attributes[string(key)] = string(value)
					}
				}
			}
			parentSkipped, parentPrimary := false, false
			if len(frames) > 0 {
				parentSkipped, parentPrimary = frames[len(frames)-1].skipped, frames[len(frames)-1].primary
			}
			isPrimary := parentPrimary || tag == "main" || tag == "article" || strings.Contains(attributes["itemtype"], "schema.org/Recipe")
			skipped := parentSkipped || hiddenRecipeElement(tag, attributes, isPrimary)
			media, _, _ := mime.ParseMediaType(attributes["type"])
			frame := recipeHTMLFrame{tag: tag, skipped: skipped, primary: isPrimary, structured: tag == "script" && media == "application/ld+json"}
			if !skipped && recipeBlockTag(tag) {
				visible.line()
				if isPrimary {
					primary.line()
				}
			}
			if kind != nethtml.SelfClosingTagToken && !recipeVoidTag(tag) {
				if len(frames) >= 64 {
					return recipeHTMLScan{}, ErrImportInput
				}
				frames = append(frames, frame)
			}
		case nethtml.EndTagToken:
			name, _ := z.TagName()
			for i := len(frames) - 1; i >= 0; i-- {
				if frames[i].tag != string(name) {
					continue
				}
				if !frames[i].skipped && recipeBlockTag(frames[i].tag) {
					visible.line()
					if frames[i].primary {
						primary.line()
					}
				}
				frames = frames[:i]
				break
			}
		}
	}
}

func hiddenRecipeElement(tag string, attributes map[string]string, primary bool) bool {
	if _, hidden := attributes["hidden"]; hidden {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(attributes["aria-hidden"]), "true") {
		return true
	}
	style := strings.ToLower(strings.Join(strings.Fields(attributes["style"]), ""))
	if strings.Contains(style, "display:none") || strings.Contains(style, "visibility:hidden") || strings.Contains(style, "content-visibility:hidden") {
		return true
	}
	for _, class := range strings.Fields(strings.ToLower(attributes["class"])) {
		if class == "hidden" || class == "sr-only" || class == "visually-hidden" {
			return true
		}
	}
	switch tag {
	case "script", "style", "head", "noscript", "template", "iframe", "object", "embed", "svg", "canvas", "form", "nav", "footer", "aside":
		return true
	case "header":
		return !primary
	}
	return false
}

func recipeVoidTag(tag string) bool {
	switch tag {
	case "area", "base", "br", "col", "embed", "hr", "img", "input", "link", "meta", "param", "source", "track", "wbr":
		return true
	}
	return false
}

func recipeBlockTag(tag string) bool {
	switch tag {
	case "p", "div", "br", "li", "h1", "h2", "h3", "h4", "h5", "h6", "section", "main", "article", "header", "tr":
		return true
	}
	return false
}

func plainRecipeText(value string) string {
	scan, err := scanRecipeHTML([]byte(value), false)
	if err != nil {
		return ""
	}
	return scan.text
}
