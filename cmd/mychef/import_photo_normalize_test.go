package main

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"testing"
)

func TestImportPhotoNormalizeRuntimeChild(t *testing.T) {
	if os.Getenv("MYCHEF_IMPORT_PHOTO_CHILD") != "1" {
		return
	}
	os.Args = []string{"mychef", "import-photo-normalize"}
	main()
	os.Exit(0)
}
func TestImportPhotoNormalizeRuntimeIgnoresApplicationSettings(t *testing.T) {
	var input bytes.Buffer
	if err := png.Encode(&input, image.NewRGBA(image.Rect(0, 0, 20, 20))); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestImportPhotoNormalizeRuntimeChild$")
	command.Env = []string{"MYCHEF_IMPORT_PHOTO_CHILD=1", "MYCHEF_ORIGIN=invalid", "MYCHEF_DB=/does-not-exist/private.db", "MYCHEF_CREDENTIALS=/does-not-exist/private.json"}
	command.Stdin = bytes.NewReader(input.Bytes())
	var output, diagnostics bytes.Buffer
	command.Stdout, command.Stderr = &output, &diagnostics
	if err := command.Run(); err != nil || diagnostics.Len() != 0 {
		t.Fatal("import child loaded app settings or failed", err)
	}
	if _, err := jpeg.Decode(bytes.NewReader(output.Bytes())); err != nil {
		t.Fatal("missing clean derivative", err)
	}
}
