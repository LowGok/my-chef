package billing

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/binary"
)

// AppleCancellationCredential contains the stored uncompressed P-256 key
// from verified App Attest enrollment. It never comes from the request.
type AppleCancellationCredential struct {
	Point [65]byte
	Count uint32
}

// AppleCancellationKeys must select an already-attested key for this exact
// app, installation and environment. AdvanceCancellationCounter atomically
// checks the selected key and its old counter before recording the new one.
type AppleCancellationKeys interface {
	CancellationCredential(context.Context, NativeCancellation) (AppleCancellationCredential, error)
	AdvanceCancellationCounter(context.Context, NativeCancellation, AppleCancellationCredential, uint32) (bool, error)
}

type AppleCancellationVerifier struct {
	AppID string
	Keys  AppleCancellationKeys
	Build AppleAttestBuildPolicy
}

func (v AppleCancellationVerifier) VerifyCancellation(ctx context.Context, c NativeCancellation, proof []byte) error {
	return v.verifyAssertion(ctx, c, proof, "mychef.storekit.cancel.v1")
}

// VerifyLaunch proves the current approved binary at the moment it opens the
// sheet. An assertion made for cancellation cannot authorize this transition.
func (v AppleCancellationVerifier) VerifyLaunch(ctx context.Context, c NativeCancellation, proof []byte) error {
	return v.verifyAssertion(ctx, c, proof, "mychef.storekit.launch.v1")
}

func (v AppleCancellationVerifier) verifyAssertion(ctx context.Context, c NativeCancellation, proof []byte, purpose string) error {
	a := c.Attempt
	if v.AppID == "" || v.Keys == nil || a.Store != Apple || !validOffer(a.Store, a.Environment, a.Interval) ||
		!accountUUID.MatchString(a.ID) || !accountUUID.MatchString(a.Binding) || c.Challenge == [32]byte{} || c.AppInstance == [32]byte{} ||
		!validOwner(c.Owner) || !validOwner(c.Family) {
		return ErrPurchaseBinding
	}
	auth, signature, ok := decodeAppleAssertion(proof)
	if !ok || !v.Build.accepts(auth[32], auth[37:]) {
		return ErrPurchaseBinding
	}
	app := sha256.Sum256([]byte(v.AppID))
	if !bytes.Equal(auth[:32], app[:]) {
		return ErrPurchaseBinding
	}
	credential, err := v.Keys.CancellationCredential(ctx, c)
	if err != nil {
		return err
	}
	count := binary.BigEndian.Uint32(auth[33:37])
	if count <= credential.Count || sha256.Sum256(credential.Point[:]) != c.AppInstance {
		return ErrPurchaseBinding
	}
	key, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), credential.Point[:])
	if err != nil {
		return ErrPurchaseBinding
	}
	client := appleCheckoutClientHash(c, purpose)
	nonce := appleAttestNonce(auth, client)
	if !ecdsa.VerifyASN1(key, nonce[:], signature) {
		return ErrPurchaseBinding
	}
	// A later journal conflict can still keep checkout pending. It must
	// not make this cryptographic assertion reusable.
	advanced, err := v.Keys.AdvanceCancellationCounter(ctx, c, credential, count)
	if err != nil {
		return err
	}
	if !advanced {
		return ErrPurchaseBinding
	}
	return nil
}

func appleCheckoutClientHash(c NativeCancellation, purpose string) [sha256.Size]byte {
	a := c.Attempt
	msg := []byte(purpose + "\x00" + string(a.Environment) + "\x00" +
		a.ID + "\x00" + a.Binding + "\x00" + string(a.Interval) + "\x00")
	return sha256.Sum256(append(msg, c.Challenge[:]...))
}

// Assertions share the same bounded CBOR decoder as key enrollment.
func decodeAppleAssertion(proof []byte) (auth, signature []byte, ok bool) {
	if len(proof) == 0 || len(proof) > 1024 {
		return nil, nil, false
	}
	var object struct {
		Signature []byte `cbor:"signature"`
		Auth      []byte `cbor:"authenticatorData"`
	}
	if err := appleAttestCBOR.Unmarshal(proof, &object); err != nil ||
		len(object.Signature) < 8 || len(object.Signature) > 72 ||
		len(object.Auth) < 37 || len(object.Auth) > 37+512 || object.Auth[32]&0x40 != 0 {
		return nil, nil, false
	}
	return object.Auth, object.Signature, true
}
