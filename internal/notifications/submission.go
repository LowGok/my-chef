package notifications

import "context"

type submissionGateKey struct{}
type submissionGate func() (func(), bool)

// WithSubmissionGate carries the server-owned final send fence. Providers call
// it once, immediately before the non-replayable request, and release it once
// that request has been written or has failed before it could be written.
func WithSubmissionGate(ctx context.Context, gate func() (func(), bool)) context.Context {
	return context.WithValue(ctx, submissionGateKey{}, submissionGate(gate))
}

func BeginSubmission(ctx context.Context) (func(), bool) {
	if ctx.Err() != nil {
		return func() {}, false
	}
	if gate, ok := ctx.Value(submissionGateKey{}).(submissionGate); ok {
		return gate()
	}
	return func() {}, true
}
