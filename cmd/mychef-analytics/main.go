package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/LowGok/my-chef/internal/analyticsdataset"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	run := analyticsdataset.Run
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "live" {
		run = liveCommand
		args = args[1:]
	}
	if err := run(ctx, args, os.Stdout, os.Stderr); err != nil {
		os.Exit(1)
	}
}
