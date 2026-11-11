package config

import (
	"errors"
	"os"
	"path/filepath"
)

func LoadFixturePhotoDirectory(app Config, inbox *FixtureInbox) (string, error) {
	directory := os.Getenv("MYCHEF_FIXTURE_PHOTOS_DIR")
	if directory == "" {
		return "", nil
	}
	if inbox == nil || app.Provider != "fixture" || os.Getenv("MYCHEF_DEV") != "1" || os.Getenv("MYCHEF_E2E") != "1" || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || directory == string(filepath.Separator) {
		return "", errors.New("invalid fixture photo configuration")
	}
	return directory, nil
}
