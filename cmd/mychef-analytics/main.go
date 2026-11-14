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
	if err := analyticsdataset.Run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		os.Exit(1)
	}
}
