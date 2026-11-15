package billing

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	_ "embed"
	"encoding/asn1"
	"encoding/binary"
	"encoding/hex"
	"encoding/pem"
	"time"

	"github.com/fxamacker/cbor/v2"
)

var appleAttestCBOR = func() cbor.DecMode {
	mode, err := (cbor.DecOptions{
		DupMapKey: cbor.DupMapKeyEnforcedAPF, IndefLength: cbor.IndefLengthForbidden,
		TagsMd: cbor.TagsForbidden, MaxNestedLevels: 4, MaxArrayElements: 16, MaxMapPairs: 16,
		ExtraReturnErrors: cbor.ExtraDecErrorUnknownField, FieldNameMatching: cbor.FieldNameMatchingCaseSensitive,
	}).DecMode()
	if err != nil {
		panic(err)
	}
	return mode
}()

// AppleAttestBuildPolicy is selected by the server for the approved native
// candidate. Older devices that send no build metadata need explicit approval.
type AppleAttestBuildPolicy struct {
	Category    uint32
	Version     string
	AllowLegacy bool
}

func (p AppleAttestBuildPolicy) accepts(flags byte, extension []byte) bool {
	if p.Category < 2 || p.Category > 5 || p.Version == "" || len(p.Version) > 64 {
		return false
	}
	if flags&0x80 == 0 {
		return p.AllowLegacy && len(extension) == 0
	}
	if len(extension) == 0 || len(extension) > 512 {
		return false
	}
	var build struct {
		Category uint32 `cbor:"apple_validation_category_01"`
		Version  string `cbor:"apple_bundle_version_01"`
	}
	return appleAttestCBOR.Unmarshal(extension, &build) == nil &&
		build.Category == p.Category && build.Version == p.Version
}

type AppleEnrollmentVerifier struct {
	AppID       string
	Roots       *x509.CertPool // Only the independently approved Apple App Attest roots.
	Build       AppleAttestBuildPolicy
	Development bool // Attestation environment, independent of purchase sandbox.
}

// Source: https://www.apple.com/certificateauthority/private/
//
//go:embed apple_app_attestation_root.pem
var appleAppAttestationRoot []byte

const appleAppAttestationRootFingerprint = "1cb9823ba28ba6ad2d33a006941de2ae4f513ef1d4e831b9f7e0fa7b6242c932"

// NewAppleEnrollmentVerifier uses only Apple's published App Attestation CA,
// never a root from a request or the host's general-purpose trust pool.
func NewAppleEnrollmentVerifier(appID string, build AppleAttestBuildPolicy, development bool) (AppleEnrollmentVerifier, error) {
	block, trailing := pem.Decode(appleAppAttestationRoot)
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(trailing)) != 0 {
		return AppleEnrollmentVerifier{}, ErrStoreUnavailable
	}
	digest := sha256.Sum256(block.Bytes)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || hex.EncodeToString(digest[:]) != appleAppAttestationRootFingerprint ||
		!cert.IsCA || cert.CheckSignatureFrom(cert) != nil {
		return AppleEnrollmentVerifier{}, ErrStoreUnavailable
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	return AppleEnrollmentVerifier{AppID: appID, Roots: roots, Build: build, Development: development}, nil
}

// Verify authenticates the key against a server-owned, one-use challenge. It
// does not enroll the key or retain the attestation or Apple's risk receipt.
func (v AppleEnrollmentVerifier) Verify(proof []byte, challenge, keyID [32]byte, now time.Time) (*ecdsa.PublicKey, error) {
	if v.AppID == "" || v.Roots == nil || now.IsZero() || challenge == [32]byte{} || keyID == [32]byte{} ||
		len(proof) == 0 || len(proof) > 64<<10 {
		return nil, ErrPurchaseBinding
	}
	var object struct {
		Format    string `cbor:"fmt"`
		Auth      []byte `cbor:"authData"`
		Statement struct {
			Chain   [][]byte `cbor:"x5c"`
			Receipt []byte   `cbor:"receipt"`
		} `cbor:"attStmt"`
	}
	if err := appleAttestCBOR.Unmarshal(proof, &object); err != nil ||
		object.Format != "apple-appattest" || len(object.Statement.Receipt) == 0 ||
		len(object.Statement.Chain) < 2 || len(object.Statement.Chain) > 4 ||
		len(object.Auth) < 88 || len(object.Auth) > 1024 {
		return nil, ErrPurchaseBinding
	}
	auth := object.Auth
	app := sha256.Sum256([]byte(v.AppID))
	var environment [16]byte
	copy(environment[:], "appattest")
	if v.Development {
		copy(environment[:], "appattestdevelop")
	}
	if !bytes.Equal(auth[:32], app[:]) || auth[32]&0x40 == 0 || binary.BigEndian.Uint32(auth[33:37]) != 0 ||
		!bytes.Equal(auth[37:53], environment[:]) || binary.BigEndian.Uint16(auth[53:55]) != 32 ||
		!bytes.Equal(auth[55:87], keyID[:]) {
		return nil, ErrPurchaseBinding
	}

	chain := make([]*x509.Certificate, len(object.Statement.Chain))
	intermediate := x509.NewCertPool()
	for n, entry := range object.Statement.Chain {
		cert, err := x509.ParseCertificate(entry)
		if err != nil {
			return nil, ErrPurchaseBinding
		}
		chain[n] = cert
		if n != 0 {
			intermediate.AddCert(cert)
		}
	}
	leaf := chain[0]
	if leaf.IsCA || leaf.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		return nil, ErrPurchaseBinding
	}
	paths, err := leaf.Verify(x509.VerifyOptions{Roots: v.Roots, Intermediates: intermediate,
		CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}})
	if err != nil || !orderedAppleAttestPath(paths, chain) {
		return nil, ErrPurchaseBinding
	}
	client := sha256.Sum256(challenge[:])
	nonce := appleAttestNonce(auth, client)
	if !matchesAppleAttestNonce(leaf, nonce) {
		return nil, ErrPurchaseBinding
	}
	key, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok || key.Curve != elliptic.P256() {
		return nil, ErrPurchaseBinding
	}
	point, err := key.ECDH()
	if err != nil || sha256.Sum256(point.Bytes()) != keyID {
		return nil, ErrPurchaseBinding
	}
	var wireKey struct {
		Type      int    `cbor:"1,keyasint"`
		Algorithm int    `cbor:"3,keyasint"`
		Curve     int    `cbor:"-1,keyasint"`
		X         []byte `cbor:"-2,keyasint"`
		Y         []byte `cbor:"-3,keyasint"`
	}
	extensions, err := appleAttestCBOR.UnmarshalFirst(auth[87:], &wireKey)
	pointBytes := point.Bytes()
	if err != nil || wireKey.Type != 2 || wireKey.Algorithm != -7 || wireKey.Curve != 1 ||
		!bytes.Equal(wireKey.X, pointBytes[1:33]) || !bytes.Equal(wireKey.Y, pointBytes[33:]) ||
		!v.Build.accepts(auth[32], extensions) {
		return nil, ErrPurchaseBinding
	}
	return key, nil
}

func orderedAppleAttestPath(paths [][]*x509.Certificate, presented []*x509.Certificate) bool {
	for _, path := range paths {
		if len(path) != len(presented)+1 {
			continue
		}
		same := true
		for i, cert := range presented {
			same = same && cert.Equal(path[i])
		}
		if same {
			return true
		}
	}
	return false
}

func appleAttestNonce(auth []byte, client [32]byte) [32]byte {
	h := sha256.New()
	_, _ = h.Write(auth)
	_, _ = h.Write(client[:])
	var digest [32]byte
	copy(digest[:], h.Sum(nil))
	return digest
}

var appleAttestNonceOID = asn1.ObjectIdentifier{1, 2, 840, 113635, 100, 8, 2}

func matchesAppleAttestNonce(cert *x509.Certificate, want [32]byte) bool {
	var found bool
	for _, ext := range cert.Extensions {
		if !ext.Id.Equal(appleAttestNonceOID) {
			continue
		}
		if found {
			return false
		}
		found = true
		var sequence, tagged asn1.RawValue
		var nonce []byte
		rest, err := asn1.Unmarshal(ext.Value, &sequence)
		if err != nil || len(rest) != 0 || sequence.Class != 0 || sequence.Tag != asn1.TagSequence || !sequence.IsCompound {
			return false
		}
		rest, err = asn1.Unmarshal(sequence.Bytes, &tagged)
		if err != nil || len(rest) != 0 || tagged.Class != 2 || tagged.Tag != 1 || !tagged.IsCompound {
			return false
		}
		rest, err = asn1.Unmarshal(tagged.Bytes, &nonce)
		if err != nil || len(rest) != 0 || !bytes.Equal(nonce, want[:]) {
			return false
		}
	}
	return found
}
