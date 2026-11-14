package analytics_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/LowGok/my-chef/internal/analytics"
)

func TestInheritedReportOutput(t *testing.T) {
	mode := os.Getenv("MYCHEF_FICTIONAL_OUTPUT_PROBE")
	if mode == "capture" || mode == "blocked" {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		data := []byte("fictional report\n")
		if mode == "blocked" {
			data = bytes.Repeat([]byte("x"), 1<<20)
		}
		descriptor := os.Stdout.Fd()
		before, _, flagErr := syscall.Syscall(syscall.SYS_FCNTL, descriptor, syscall.F_GETFL, 0)
		if flagErr != 0 {
			os.Exit(44)
		}
		n, err := (analytics.OutputWriter{Context: ctx, Output: os.Stdout}).Write(data)
		after, _, flagErr := syscall.Syscall(syscall.SYS_FCNTL, descriptor, syscall.F_GETFL, 0)
		if flagErr != 0 || before != after {
			os.Exit(45)
		}
		if mode == "capture" && (err != nil || n != len(data)) {
			os.Exit(42)
		}
		if mode == "blocked" && (err == nil || n == 0 || n == len(data)) {
			os.Exit(43)
		}
		os.Exit(0)
	}
	for _, mode := range []string{"capture", "blocked"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestInheritedReportOutput$")
			command.Env = append(os.Environ(), "MYCHEF_FICTIONAL_OUTPUT_PROBE="+mode)
			if mode == "capture" {
				data, err := command.Output()
				if err != nil || string(data) != "fictional report\n" {
					t.Fatalf("inherited stdout: %q %v", data, err)
				}
				return
			}
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = reader.Close(); _ = writer.Close() }()
			command.Stdout = writer
			if err = command.Run(); err != nil {
				t.Fatal("blocked inherited pipe did not stop its partial write", err)
			}
		})
	}
}
