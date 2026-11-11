package main

import (
	"errors"
	"io"
	"os"
	"syscall"

	"github.com/LowGok/my-chef/internal/providers"
)

// This internal mode runs before configuration loading and reads only stdin.
func runPhotoNormalize(arguments []string) int {
	return runImageNormalize(arguments, false)
}
func runImportPhotoNormalize(arguments []string) int {
	return runImageNormalize(arguments, true)
}
func runImageNormalize(arguments []string, importing bool) int {
	if len(arguments) != 0 {
		return 2
	}
	if syscall.Setrlimit(syscall.RLIMIT_CORE, &syscall.Rlimit{Cur: 0, Max: 0}) != nil || syscall.Setrlimit(syscall.RLIMIT_CPU, &syscall.Rlimit{Cur: 4, Max: 4}) != nil {
		return 1
	}
	limit := providers.PersonalPhotoInputLimit
	normalize := providers.NormalizePersonalPhoto
	if importing {
		limit = providers.ImportPhotoInputLimit
		normalize = providers.NormalizeImportPhoto
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, int64(limit+1)))
	if err != nil {
		return 2
	}
	defer clear(data)
	photo, err := normalize(data)
	if errors.Is(err, providers.ErrPersonalPhotoSize) {
		return 3
	}
	if err != nil {
		return 2
	}
	if _, err := os.Stdout.Write(photo.JPEG()); err != nil {
		return 1
	}
	return 0
}
