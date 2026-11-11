package providers

import (
	"context"
	"encoding/hex"
	"strings"
)

type uploadIntentKey struct{}
type traceGuardKey struct{}
type uploadIntent struct {
	target string
	begin  func(context.Context) error
}
type TraceGuard func(string, func() error) error

func WithUploadIntent(ctx context.Context, target string, begin func(context.Context) error) context.Context {
	return context.WithValue(ctx, uploadIntentKey{}, uploadIntent{target: target, begin: begin})
}
func UploadTarget(ctx context.Context) string {
	intent, _ := ctx.Value(uploadIntentKey{}).(uploadIntent)
	return intent.target
}
func StartReservedUpload(ctx context.Context) error {
	intent, ok := ctx.Value(uploadIntentKey{}).(uploadIntent)
	if !ok {
		return nil
	}
	suffix := strings.TrimPrefix(intent.target, "mychef/")
	decoded, err := hex.DecodeString(suffix)
	if err != nil || len(decoded) != 16 || intent.target != "mychef/"+hex.EncodeToString(decoded) || intent.begin == nil {
		return Failure("provider_invalid")
	}
	return intent.begin(ctx)
}
func WithTraceGuard(ctx context.Context, guard TraceGuard) context.Context {
	return context.WithValue(ctx, traceGuardKey{}, guard)
}
func guardedTraceWrite(ctx context.Context, path string, write func() error) error {
	guard, _ := ctx.Value(traceGuardKey{}).(TraceGuard)
	if guard != nil {
		return guard(path, write)
	}
	return write()
}
