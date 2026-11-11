package providers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"io"
	"os/exec"
	"path/filepath"
	"time"
)

type PersonalPhotoNormalizer struct {
	executable string
	arguments  []string
	budget     time.Duration
	inputLimit int
}

func NewPersonalPhotoNormalizer(executable string) *PersonalPhotoNormalizer {
	return &PersonalPhotoNormalizer{executable: executable, arguments: []string{"photo-normalize"}, budget: 10 * time.Second, inputLimit: PersonalPhotoInputLimit}
}

func NewImportPhotoNormalizer(executable string) *PersonalPhotoNormalizer {
	return &PersonalPhotoNormalizer{executable: executable, arguments: []string{"import-photo-normalize"}, budget: 10 * time.Second, inputLimit: ImportPhotoInputLimit}
}

type photoOutput struct{ buffer bytes.Buffer }

func (b *photoOutput) Len() int      { return b.buffer.Len() }
func (b *photoOutput) Bytes() []byte { return b.buffer.Bytes() }

func (b *photoOutput) Write(value []byte) (int, error) {
	if len(value) > PersonalPhotoLimit-b.Len() {
		return 0, ErrPersonalPhotoSize
	}
	return b.buffer.Write(value)
}

func normalizedPersonalJPEG(data []byte) (NormalizedPhoto, error) {
	if len(data) == 0 || len(data) > PersonalPhotoLimit {
		return NormalizedPhoto{}, ErrPersonalPhotoSize
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || format != "jpeg" || config.Width < 1 || config.Height < 1 || config.Width > 2048 || config.Height > 2048 {
		return NormalizedPhoto{}, ErrPersonalPhotoInvalid
	}
	hash := sha256.Sum256(data)
	return NormalizedPhoto{data: bytes.Clone(data), width: config.Width, height: config.Height, digest: hex.EncodeToString(hash[:])}, nil
}

func (n *PersonalPhotoNormalizer) Normalize(ctx context.Context, raw []byte) (NormalizedPhoto, error) {
	if err := ctx.Err(); err != nil {
		return NormalizedPhoto{}, err
	}
	if len(raw) > n.inputLimit {
		return NormalizedPhoto{}, ErrPersonalPhotoSize
	}
	if !filepath.IsAbs(n.executable) || n.budget <= 0 {
		return NormalizedPhoto{}, ErrPersonalPhotoProcess
	}
	call, cancel := context.WithTimeout(ctx, n.budget)
	defer cancel()
	command := exec.CommandContext(call, n.executable, n.arguments...)
	// The decoder receives no database, capability, credential or provider settings.
	command.Env = []string{"GOMAXPROCS=1", "GOMEMLIMIT=256MiB", "GOGC=100", "GOTRACEBACK=none"}
	command.Stdin = bytes.NewReader(raw)
	output := &photoOutput{}
	command.Stdout, command.Stderr = output, io.Discard
	command.WaitDelay = time.Second
	err := command.Run()
	if ctx.Err() != nil {
		return NormalizedPhoto{}, ctx.Err()
	}
	if call.Err() != nil {
		return NormalizedPhoto{}, ErrPersonalPhotoProcess
	}
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			switch exit.ExitCode() {
			case 2:
				return NormalizedPhoto{}, ErrPersonalPhotoInvalid
			case 3:
				return NormalizedPhoto{}, ErrPersonalPhotoSize
			}
		}
		return NormalizedPhoto{}, ErrPersonalPhotoProcess
	}
	return normalizedPersonalJPEG(output.Bytes())
}
