package auth

import "context"

type nativeIdentityKey struct{}

// WithNativeIdentity is for the server's trusted listener/route configuration,
// never a value copied from request JSON, headers or a client's platform name.
func WithNativeIdentity(ctx context.Context, lane string) context.Context {
	return context.WithValue(ctx, nativeIdentityKey{}, lane)
}

func nativeIdentity(ctx context.Context) string {
	value, ok := ctx.Value(nativeIdentityKey{}).(string)
	if !ok {
		return "preview"
	}
	return value
}

func validNativeIdentity(lane string) bool { return lane == "preview" || lane == "app" }
