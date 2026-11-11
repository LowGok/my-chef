package main

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func TestPhotoNormalizeRuntimeChild(t *testing.T) {
	if os.Getenv("MYCHEF_PHOTO_MODE_CHILD") != "1" {
		return
	}
	os.Args = []string{"mychef", "photo-normalize"}
	main()
	os.Exit(0)
}

func TestPhotoNormalizeLimitsChild(t *testing.T) {
	if os.Getenv("MYCHEF_PHOTO_MODE_CHILD") != "1" {
		return
	}
	code := runPhotoNormalize(nil)
	var cpu, core syscall.Rlimit
	if syscall.Getrlimit(syscall.RLIMIT_CPU, &cpu) != nil || syscall.Getrlimit(syscall.RLIMIT_CORE, &core) != nil || cpu.Cur > 4 || cpu.Max > 4 || core.Cur != 0 || core.Max != 0 {
		os.Exit(10)
	}
	os.Exit(code)
}

func TestPhotoNormalizeRuntimeModeDoesNotLoadApplicationSettings(t *testing.T) {
	directory := t.TempDir()
	database := filepath.Join(directory, "must-not-be-created.db")
	var input bytes.Buffer
	if err := png.Encode(&input, image.NewNRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"TestPhotoNormalizeRuntimeChild", "TestPhotoNormalizeLimitsChild"} {
		command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^"+name+"$")
		command.Env = []string{"MYCHEF_PHOTO_MODE_CHILD=1", "MYCHEF_DB=" + database, "MYCHEF_CREDENTIALS=" + filepath.Join(directory, "missing-credentials.json"), "MYCHEF_ORIGIN=invalid"}
		command.Stdin = bytes.NewReader(input.Bytes())
		var output, diagnostics bytes.Buffer
		command.Stdout, command.Stderr = &output, &diagnostics
		if err := command.Run(); err != nil || diagnostics.Len() != 0 {
			t.Fatal("decoder loaded application settings or produced diagnostics", err)
		}
		if _, err := jpeg.Decode(bytes.NewReader(output.Bytes())); err != nil {
			t.Fatal("internal decoder did not return JPEG", err)
		}
	}
	if _, err := os.Stat(database); !os.IsNotExist(err) {
		t.Fatal("decoder touched the application database")
	}
	command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestPhotoNormalizeRuntimeChild$")
	command.Env = []string{"MYCHEF_PHOTO_MODE_CHILD=1"}
	command.Stdin = bytes.NewReader([]byte("malformed-private-photo-canary"))
	var output, diagnostics bytes.Buffer
	command.Stdout, command.Stderr = &output, &diagnostics
	if err := command.Run(); err == nil || output.Len() != 0 || diagnostics.Len() != 0 {
		t.Fatal("decoder exposed invalid input or accepted it")
	}
}
