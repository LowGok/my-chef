package auth

import (
	"context"
	"time"
)

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

type nativeIdentityPolicyKey struct{}
type nativeIdentityPolicy struct{ revision, until int64 }

// WithNativeIdentityPolicy accepts only the server's persisted policy snapshot.
// A nonzero deadline restricts authority to existing signed-in sync families.
func WithNativeIdentityPolicy(ctx context.Context, revision, until int64) context.Context {
	return context.WithValue(ctx, nativeIdentityPolicyKey{}, nativeIdentityPolicy{revision, until})
}

func identityPolicy(ctx context.Context) (nativeIdentityPolicy, bool) {
	p, ok := ctx.Value(nativeIdentityPolicyKey{}).(nativeIdentityPolicy)
	return p, ok
}

func identityPermits(ctx context.Context, revision int64, userID *int64) bool {
	p, checked := identityPolicy(ctx)
	return !checked || p.revision == revision && (p.until == 0 || p.until > time.Now().Unix() && userID != nil)
}
