package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/LowGok/my-chef/internal/config"
	"github.com/LowGok/my-chef/internal/imports"
)

var version = "dev"

func main() { syscall.Umask(077); os.Exit(run(os.Args[1:])) }
func run(arguments []string) int {
	if len(arguments) == 1 && arguments[0] == "--version" {
		fmt.Println(version)
		return 0
	}
	if len(arguments) == 0 {
		return 2
	}
	command := arguments[0]
	flags := flag.NewFlagSet("mychef-import-worker", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("ledger", "", "")
	id := flags.String("ledger-id", "", "")
	ledgerKeyPath := flags.String("ledger-key", "", "")
	channelKeyPath := flags.String("channel-key", "", "")
	socket := flags.String("socket", "", "")
	policyPath := flags.String("policy", "", "")
	provider := flags.String("provider", "public", "")
	credentials := flags.String("credentials", "", "")
	project := flags.String("project", "", "")
	appUID := flags.Int("app-uid", -1, "")
	untilRaw := flags.String("fixture-until", "", "")
	if flags.Parse(arguments[1:]) != nil || flags.NArg() != 0 {
		return 2
	}
	if command == "fixture-policy" {
		until, err := time.Parse(time.RFC3339, *untilRaw)
		now := time.Now().UTC()
		if err != nil || os.Getenv("MYCHEF_E2E") != "1" || !until.After(now) || until.Sub(now) > time.Hour {
			return 1
		}
		_, policy := imports.FixtureExtractionPolicy(until)
		if json.NewEncoder(os.Stdout).Encode(policy) != nil {
			return 1
		}
		return 0
	}
	key, err := config.ReadImportSecret(*ledgerKeyPath)
	if err != nil {
		return 1
	}
	defer clear(key)
	if command == "init-ledger" {
		identity, err := imports.InitializeAdmissionLedger(*path, key, time.Now().UTC())
		if err != nil {
			return 1
		}
		if json.NewEncoder(os.Stdout).Encode(map[string]string{"ledgerId": identity}) != nil {
			return 1
		}
		return 0
	}
	if command != "serve" || *appUID < 0 || uint64(*appUID) > uint64(^uint32(0)) {
		return 2
	}
	fixture := *provider == "fixture"
	if !fixture && *provider != "public" && *provider != "gemini" {
		return 2
	}
	var until time.Time
	if fixture {
		until, err = time.Parse(time.RFC3339, *untilRaw)
		now := time.Now().UTC()
		if err != nil || os.Getenv("MYCHEF_E2E") != "1" || !until.After(now) || until.Sub(now) > time.Hour {
			return 1
		}
	} else if *appUID == os.Geteuid() {
		return 1
	}
	channelKey, err := config.ReadImportSecret(*channelKeyPath)
	if err != nil {
		return 1
	}
	defer clear(channelKey)
	ledger, err := imports.OpenAdmissionLedger(*path, key, *id, time.Now().UTC())
	if err != nil {
		return 1
	}
	defer func() { _ = ledger.Close() }()
	var engine imports.ExtractionEngine
	var binding imports.ExtractionBinding
	var fetcher imports.PageFetcher = imports.NewFetcher()
	policy := func() (imports.ExtractionPolicy, error) { return imports.ExtractionPolicy{}, nil }
	if fixture {
		binding, _ = imports.FixtureExtractionPolicy(until)
		engine = imports.FixtureExtractionEngine{}
		fetcher = imports.FixturePageFetcher{}
		policy = func() (imports.ExtractionPolicy, error) {
			if !time.Now().Before(until) {
				return imports.ExtractionPolicy{}, imports.ErrExtractionUnavailable
			}
			return config.LoadRecipeExtractionPolicy(*policyPath)
		}
	} else if *provider == "gemini" {
		binding, err = config.ReadImportBinding(*credentials, *project)
		if err != nil {
			return 1
		}
		engine, err = imports.NewGeminiExtractionEngine(binding)
		if err != nil {
			return 1
		}
		policy = func() (imports.ExtractionPolicy, error) { return config.LoadRecipeExtractionPolicy(*policyPath) }
	}
	handler, err := imports.NewWorkerHandler(imports.WorkerOptions{Ledger: ledger, Binding: binding, Policy: policy, Engine: engine, Fetcher: fetcher, ChannelKey: channelKey, Fixture: fixture})
	if err != nil {
		return 1
	}
	if fixture {
		next := handler
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !time.Now().Before(until) {
				http.Error(w, "private import fixture expired", http.StatusServiceUnavailable)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	listener, err := imports.ListenPrivateWorker(*socket, uint32(*appUID))
	if err != nil {
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 4096, BaseContext: func(net.Listener) context.Context { return ctx }}
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	err = server.Serve(listener)
	if err != nil && err != http.ErrServerClosed {
		return 1
	}
	stop()
	<-done
	return 0
}
