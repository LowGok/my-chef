package config

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/LowGok/my-chef/internal/imports"
)

// LoadRecipeExtractionPolicy is called by the private worker before dispatch.
// It reads operator-owned configuration; no web request can choose this path,
// mutate its limits, enable billing or replace missing evidence with defaults.
func LoadRecipeExtractionPolicy(path string) (imports.ExtractionPolicy, error) {
	invalid := imports.ErrExtractionUnavailable
	if !filepath.IsAbs(path) {
		return imports.ExtractionPolicy{}, invalid
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || (info.Mode().Perm() != 0600 && info.Mode().Perm() != 0400) {
		return imports.ExtractionPolicy{}, invalid
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return imports.ExtractionPolicy{}, invalid
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return imports.ExtractionPolicy{}, invalid
	}
	data, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil || len(data) > 65536 {
		return imports.ExtractionPolicy{}, invalid
	}
	defer clear(data)
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var policy imports.ExtractionPolicy
	if decoder.Decode(&policy) != nil {
		return imports.ExtractionPolicy{}, invalid
	}
	if _, err := decoder.Token(); err != io.EOF {
		return imports.ExtractionPolicy{}, invalid
	}
	return policy, nil
}

// ReadImportSecret reads a raw 32-byte local service key, never a cloud key.
func ReadImportSecret(path string) ([]byte, error) {
	data, err := readImportPrivateFile(path, 32)
	if err != nil || len(data) != 32 {
		clear(data)
		return nil, imports.ErrExtractionUnavailable
	}
	if _, err := imports.NewDraftVault(data); err != nil {
		clear(data)
		return nil, imports.ErrExtractionUnavailable
	}
	return data, nil
}
func readImportPrivateFile(path string, maximum int64) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, imports.ErrExtractionUnavailable
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || (info.Mode().Perm() != 0600 && info.Mode().Perm() != 0400) {
		return nil, imports.ErrExtractionUnavailable
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, imports.ErrExtractionUnavailable
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, imports.ErrExtractionUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(data)) > maximum {
		clear(data)
		return nil, imports.ErrExtractionUnavailable
	}
	return data, nil
}
func ReadImportBinding(path, project string) (imports.ExtractionBinding, error) {
	data, err := readImportPrivateFile(path, 65536)
	if err != nil {
		return imports.ExtractionBinding{}, err
	}
	defer clear(data)
	var credential struct {
		APIKey string `json:"gemini_api_key"`
		Model  string `json:"gemini_recipe_model"`
	}
	if json.Unmarshal(data, &credential) != nil || credential.APIKey == "" || credential.Model == "" {
		return imports.ExtractionBinding{}, imports.ErrExtractionUnavailable
	}
	return imports.ExtractionBinding{ProjectID: project, Model: credential.Model, APIKey: credential.APIKey}, nil
}

type RecipeImportClient struct {
	Socket     string
	TempDir    string
	ChannelKey []byte
}

func (*RecipeImportClient) String() string   { return "private import client settings" }
func (*RecipeImportClient) GoString() string { return "private import client settings" }
func LoadRecipeImportClient() (*RecipeImportClient, error) {
	socket, keyFile, directory := os.Getenv("MYCHEF_IMPORT_WORKER_SOCKET"), os.Getenv("MYCHEF_IMPORT_CHANNEL_KEY_FILE"), os.Getenv("MYCHEF_IMPORT_TEMP_DIR")
	if socket == "" && keyFile == "" && directory == "" {
		return nil, nil
	}
	if !filepath.IsAbs(socket) || filepath.Base(socket) != "import-worker.sock" || !filepath.IsAbs(directory) || directory == "/" {
		return nil, imports.ErrExtractionUnavailable
	}
	key, err := ReadImportSecret(keyFile)
	if err != nil {
		return nil, err
	}
	return &RecipeImportClient{Socket: socket, TempDir: directory, ChannelKey: key}, nil
}
