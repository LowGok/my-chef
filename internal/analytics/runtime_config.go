package analytics

import (
	"encoding/json"
	"io"
	"os"
)

// RuntimeFile is operator-controlled and never accepted from an application request.
type RuntimeFile struct {
	Root          string `json:"root"`
	PrivacyPath   string `json:"privacyPath"`
	AnchorPath    string `json:"anchorPath"`
	BackupKeyFile string `json:"backupKeyFile"`
	Collection    Config `json:"collection"`
}

func LoadRuntime(path string) (OpenOptions, error) {
	if e := privatePath(path, false); e != nil {
		return OpenOptions{}, e
	}
	f, e := os.Open(path)
	if e != nil {
		return OpenOptions{}, ErrPrivacy
	}
	defer func() { _ = f.Close() }()
	var r RuntimeFile
	d := json.NewDecoder(io.LimitReader(f, 65537))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil || d.Decode(new(any)) != io.EOF {
		return OpenOptions{}, ErrInput
	}
	if r.Collection.OutboxCap < 0 || r.Collection.OutboxCap > 1000000 {
		return OpenOptions{}, ErrInput
	}
	o := OpenOptions{Root: r.Root, PrivacyPath: r.PrivacyPath, AnchorPath: r.AnchorPath, Config: r.Collection}
	if r.BackupKeyFile != "" {
		if e = privatePath(r.BackupKeyFile, false); e != nil {
			return o, e
		}
		key, e := os.ReadFile(r.BackupKeyFile)
		if e != nil || len(key) != 32 {
			return o, ErrPrivacy
		}
		o.BackupKey = key
	}
	return o, nil
}
