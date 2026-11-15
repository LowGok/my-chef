package billing

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/binary"
)

// AppleCancellationCredential is a public key from verified App Attest
// enrollment. It must never come from the cancellation request.
type AppleCancellationCredential struct {
	PublicKey *ecdsa.PublicKey
	Count     uint32
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
}

func (v AppleCancellationVerifier) VerifyCancellation(ctx context.Context, c NativeCancellation, proof []byte) error {
	a := c.Attempt
	if v.AppID == "" || v.Keys == nil || a.Store != Apple || !validOffer(a.Store, a.Environment, a.Interval) ||
		!accountUUID.MatchString(a.ID) || !accountUUID.MatchString(a.Binding) || c.Challenge == [32]byte{} || c.AppInstance == [32]byte{} ||
		!validOwner(c.Owner) || !validOwner(c.Family) {
		return ErrPurchaseBinding
	}
	auth, signature, ok := decodeAppleAssertion(proof)
	if !ok {
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
	key := credential.PublicKey
	count := binary.BigEndian.Uint32(auth[33:])
	if key == nil || key.Curve != elliptic.P256() || key.X == nil || key.Y == nil ||
		!key.Curve.IsOnCurve(key.X, key.Y) || count <= credential.Count {
		return ErrPurchaseBinding
	}
	point, err := key.ECDH()
	if err != nil || sha256.Sum256(point.Bytes()) != c.AppInstance {
		return ErrPurchaseBinding
	}
	client := appleCancellationClientHash(c)
	var signed [37 + sha256.Size]byte
	copy(signed[:37], auth)
	copy(signed[37:], client[:])
	nonce := sha256.Sum256(signed[:])
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

func appleCancellationClientHash(c NativeCancellation) [sha256.Size]byte {
	a := c.Attempt
	msg := []byte("mychef.storekit.cancel.v1\x00" + string(a.Environment) + "\x00" +
		a.ID + "\x00" + a.Binding + "\x00" + string(a.Interval) + "\x00")
	return sha256.Sum256(append(msg, c.Challenge[:]...))
}

// An App Attest assertion is a definite-length CBOR map of two byte
// strings. Accept either field order, and reject duplicate or extra fields,
// indefinite lengths, and data outside the exact supported shape.
func decodeAppleAssertion(proof []byte) (auth, signature []byte, ok bool) {
	if len(proof) > 160 || len(proof) == 0 || proof[0] != 0xa2 {
		return nil, nil, false
	}
	proof = proof[1:]
	for range 2 {
		if len(proof) == 0 {
			return nil, nil, false
		}
		var name string
		switch {
		case bytes.HasPrefix(proof, []byte("\x69signature")):
			name, proof = "signature", proof[10:]
		case bytes.HasPrefix(proof, []byte("\x71authenticatorData")):
			name, proof = "authenticatorData", proof[18:]
		default:
			return nil, nil, false
		}
		if len(proof) < 2 || proof[0] != 0x58 {
			return nil, nil, false
		}
		size := int(proof[1])
		proof = proof[2:]
		if size < 24 || size > len(proof) {
			return nil, nil, false
		}
		switch name {
		case "signature":
			if signature != nil || size > 72 {
				return nil, nil, false
			}
			signature = proof[:size]
		case "authenticatorData":
			if auth != nil || size != 37 {
				return nil, nil, false
			}
			auth = proof[:size]
		}
		proof = proof[size:]
	}
	return auth, signature, len(proof) == 0 && auth != nil && signature != nil
}
