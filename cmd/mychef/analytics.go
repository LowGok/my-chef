package main

import (
	"context"

	"github.com/LowGok/my-chef/internal/analytics"
	"github.com/LowGok/my-chef/internal/store"
)

// Empty configuration preserves the disabled rollout. Initialization is an
// explicit operator CLI action; startup never creates analytical storage.
func openAnalytics(ctx context.Context, app *store.Store, path string) (func(), error) {
	if path == "" {
		return func() {}, nil
	}
	options, e := analytics.LoadRuntime(path)
	if e != nil {
		return func() {}, e
	}
	service, e := analytics.Open(ctx, app.DB, options)
	if e != nil {
		return func() {}, e
	}
	if e = service.Maintain(ctx); e != nil {
		_ = service.Close()
		return func() {}, e
	}
	app.Analytics = service
	return func() { _ = service.Close() }, nil
}
