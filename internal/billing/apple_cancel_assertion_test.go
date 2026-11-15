package billing

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
)

const fixtureAppleApp = "TEAM000000.example.mychef"

type fixtureAppleKeys struct {
	mu      sync.Mutex
	owner   string
	family  string
	key     *ecdsa.PublicKey
	counter uint32
}

func (s *fixtureAppleKeys) CancellationCredential(_ context.Context, c NativeCancellation) (AppleCancellationCredential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.Owner != s.owner || c.Family != s.family {
		return AppleCancellationCredential{}, ErrPurchaseBinding
	}
	return AppleCancellationCredential{PublicKey: s.key, Count: s.counter}, nil
}

func (s *fixtureAppleKeys) AdvanceCancellationCounter(_ context.Context, c NativeCancellation, k AppleCancellationCredential, next uint32) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.Owner != s.owner || c.Family != s.family || s.key != k.PublicKey || s.counter != k.Count {
		return false, nil
	}
	s.counter = next
	return true, nil
}

func fixtureAppleAssertion(t *testing.T, key *ecdsa.PrivateKey, app string, c NativeCancellation, counter uint32, build ...AppleAttestBuildPolicy) []byte {
	t.Helper()
	var auth [37]byte
	appHash := sha256.Sum256([]byte(app))
	copy(auth[:32], appHash[:])
	binary.BigEndian.PutUint32(auth[33:], counter)
	authData := auth[:]
	if len(build) != 0 {
		// Run the full signed cancellation fixture with the extension layout
		// from Apple's published attestation guide.
		category := make([]byte, 4)
		binary.LittleEndian.PutUint32(category, build[0].Category)
		extensions, err := cbor.Marshal(map[string]any{
			"apple_validation_category_01": category,
			"apple_bundle_version_01":      build[0].Version,
		})
		if err != nil {
			t.Fatal(err)
		}
		authData = append(authData, extensions...)
	}

	// This is the byte transcript the native side must build, independent
	// of the server implementation of the verifier.
	msg := []byte("mychef.storekit.cancel.v1\x00" + string(c.Attempt.Environment) + "\x00" +
		c.Attempt.ID + "\x00" + c.Attempt.Binding + "\x00" + string(c.Attempt.Interval) + "\x00")
	msg = append(msg, c.Challenge[:]...)
	client := sha256.Sum256(msg)
	nonce := sha256.Sum256(append(append([]byte(nil), authData...), client[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, key, nonce[:])
	if err != nil {
		t.Fatal(err)
	}

	wire := []byte{0xa2, 0x69}
	wire = append(wire, "signature"...)
	wire = append(wire, 0x58, byte(len(sig)))
	wire = append(wire, sig...)
	wire = append(wire, 0x71)
	wire = append(wire, "authenticatorData"...)
	wire = append(wire, 0x58, byte(len(authData)))
	return append(wire, authData...)
}

func TestAppleCancellationAssertionRejectsUntrustedOrReplayedProof(t *testing.T) {
	_, j, now := fixture(t)
	const family = "99998888777766665555444433332222"
	ctx := t.Context()
	r, err := j.ReserveNative(ctx, owner, family, Sandbox, Apple, Annual, now)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	point, err := key.PublicKey.ECDH()
	if err != nil {
		t.Fatal(err)
	}
	c, err := j.LaunchForNativeCancellation(ctx, r, owner, family, sha256.Sum256(point.Bytes()), now)
	if err != nil {
		t.Fatal(err)
	}
	valid := fixtureAppleAssertion(t, key, fixtureAppleApp, c, 6)
	keys := &fixtureAppleKeys{owner: owner, family: family, key: &key.PublicKey, counter: 5}
	v := AppleCancellationVerifier{AppID: fixtureAppleApp, Keys: keys, Build: AppleAttestBuildPolicy{Category: 2, Version: "8", AllowLegacy: true}}
	modified := c
	modified.Challenge[0] ^= 1
	wrongInterval := c
	wrongInterval.Attempt.Interval = Monthly
	wrongEnvironment := c
	wrongEnvironment.Attempt.Environment = Production
	otherFamily := c
	otherFamily.Family = owner
	for _, tc := range []struct {
		name  string
		claim NativeCancellation
		proof []byte
	}{
		{"different registered installation", otherFamily, valid},
		{"different challenge", modified, valid},
		{"different plan", wrongInterval, valid},
		{"different environment", wrongEnvironment, valid},
		{"wrong app", c, fixtureAppleAssertion(t, key, "TEAM000000.example.other", c, 6)},
		{"wrong signing key", c, fixtureAppleAssertion(t, otherKey, fixtureAppleApp, c, 6)},
		{"same counter", c, fixtureAppleAssertion(t, key, fixtureAppleApp, c, 5)},
		{"previous counter", c, fixtureAppleAssertion(t, key, fixtureAppleApp, c, 4)},
		{"trailing bytes", c, append(append([]byte(nil), valid...), 0x00)},
		{"truncated object", c, valid[:len(valid)-1]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := v.VerifyCancellation(ctx, tc.claim, tc.proof); !errors.Is(err, ErrPurchaseBinding) {
				t.Fatalf("untrusted restatement accepted (%v)", err)
			}
		})
	}
	keys.key = &otherKey.PublicKey
	replacement := fixtureAppleAssertion(t, otherKey, fixtureAppleApp, c, 6)
	if err := v.VerifyCancellation(ctx, c, replacement); !errors.Is(err, ErrPurchaseBinding) {
		t.Fatal("a replacement key signed for a launch it did not authorize", err)
	}
	keys.key = &key.PublicKey
	v.Build.AllowLegacy = false
	if err := v.VerifyCancellation(ctx, c, valid); !errors.Is(err, ErrPurchaseBinding) {
		t.Fatal("a legacy assertion passed a signed build requirement", err)
	}
	wrongBuild := fixtureAppleAssertion(t, key, fixtureAppleApp, c, 6, AppleAttestBuildPolicy{Category: 2, Version: "7"})
	if err := v.VerifyCancellation(ctx, c, wrongBuild); !errors.Is(err, ErrPurchaseBinding) {
		t.Fatal("an assertion from another build was accepted", err)
	}
	valid = fixtureAppleAssertion(t, key, fixtureAppleApp, c, 6, v.Build)
	clock := func() time.Time { return now.Add(time.Minute) }
	if err := j.CompleteNativeCancellation(ctx, c, valid, v, liveNativeTestSession, clock); err != nil {
		t.Fatal("valid Apple assertion did not release the matching launch", err)
	}
	if err := v.VerifyCancellation(ctx, c, valid); !errors.Is(err, ErrPurchaseBinding) {
		t.Fatal("Apple assertion counter was not consumed", err)
	}
	if _, err := j.ReserveNative(ctx, owner, family, Sandbox, Play, Monthly, clock()); err != nil {
		t.Fatal("cancelled Apple launch still blocked a new reservation", err)
	}
}
