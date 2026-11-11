package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"errors"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/text/unicode/norm"
)

const (
	MaxPasswordBytes    = 512
	minNewPasswordRunes = 10
	maxNewPasswordRunes = 128
	argon2Memory        = 19 * 1024
	argon2Iterations    = 2
	argon2Parallelism   = 1
	argon2SaltBytes     = 16
	argon2HashBytes     = 32
	argon2Prefix        = "$argon2id$v=19$m=19456,t=2,p=1$"
)

var (
	ErrPasswordLength = errors.New("password length outside policy")
	ErrPasswordCommon = errors.New("password on blocklist")
	passwordWork      = make(passwordWorkLimit, 4)
)

//go:embed blocklist/10k-most-common.txt
var blocklistSource string

var commonPasswords = func() map[string]struct{} {
	values := make(map[string]struct{}, 10000)
	for line := range strings.SplitSeq(blocklistSource, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line != "" && utf8.ValidString(line) {
			values[norm.NFC.String(line)] = struct{}{}
		}
	}
	return values
}()

func normalizePassword(raw string) (string, error) {
	if len(raw) == 0 || len(raw) > MaxPasswordBytes || !utf8.ValidString(raw) {
		return "", ErrPasswordLength
	}
	normalized := norm.NFC.String(raw)
	if len(normalized) > MaxPasswordBytes {
		return "", ErrPasswordLength
	}
	return normalized, nil
}

func checkNewPassword(raw string) (string, error) {
	normalized, err := normalizePassword(raw)
	if err != nil {
		return "", err
	}
	if n := utf8.RuneCountInString(normalized); n < minNewPasswordRunes || n > maxNewPasswordRunes {
		return "", ErrPasswordLength
	}
	if _, blocked := commonPasswords[normalized]; blocked {
		return "", ErrPasswordCommon
	}
	return normalized, nil
}

type passwordWorkLimit chan struct{}

func (limit passwordWorkLimit) run(ctx context.Context, work func()) error {
	select {
	case limit <- struct{}{}:
		defer func() { <-limit }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	work()
	return nil
}

func argon2Digest(password string, salt []byte) []byte {
	return argon2.IDKey([]byte(password), salt, argon2Iterations, argon2Memory, argon2Parallelism, argon2HashBytes)
}

var phcBase64 = base64.RawStdEncoding.Strict()

func newPasswordHash(ctx context.Context, normalized string) (string, error) {
	var salt [argon2SaltBytes]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return "", err
	}
	var digest []byte
	if err := passwordWork.run(ctx, func() { digest = argon2Digest(normalized, salt[:]) }); err != nil {
		return "", err
	}
	return argon2Prefix + phcBase64.EncodeToString(salt[:]) + "$" + phcBase64.EncodeToString(digest), nil
}

func parseArgon2id(hash string) (salt, digest []byte, ok bool) {
	value, ok := strings.CutPrefix(hash, argon2Prefix)
	if !ok || len(value) != phcBase64.EncodedLen(argon2SaltBytes)+1+phcBase64.EncodedLen(argon2HashBytes) {
		return nil, nil, false
	}
	encodedSalt, encodedDigest, ok := strings.Cut(value, "$")
	if !ok {
		return nil, nil, false
	}
	salt, saltErr := phcBase64.DecodeString(encodedSalt)
	digest, digestErr := phcBase64.DecodeString(encodedDigest)
	return salt, digest, saltErr == nil && digestErr == nil && len(salt) == argon2SaltBytes && len(digest) == argon2HashBytes
}

// verifyArgon2id runs inside the password work budget.
func verifyArgon2id(hash, normalized string) bool {
	salt, digest, ok := parseArgon2id(hash)
	return ok && subtle.ConstantTimeCompare(argon2Digest(normalized, salt), digest) == 1
}

// A dummy digest is never accepted. Adjusting the cost header changes how
// much work the verifier performs without needing a real hash at each cost.
var dummyBcrypt = func() [bcrypt.DefaultCost + 1][]byte {
	base, err := bcrypt.GenerateFromPassword([]byte("mychef-dummy-account"), bcrypt.MinCost)
	if err != nil {
		panic(err)
	}
	var hashes [bcrypt.DefaultCost + 1][]byte
	for cost := bcrypt.MinCost; cost <= bcrypt.DefaultCost; cost++ {
		value := append([]byte(nil), base...)
		value[4], value[5] = byte('0'+cost/10), byte('0'+cost%10)
		hashes[cost] = value
	}
	return hashes
}()

var dummyArgonSalt = [argon2SaltBytes]byte{0x6d, 0x79, 0x63, 0x68, 0x65, 0x66}

func bcryptWorkBudget(cost int, run func(int)) {
	for ; cost < bcrypt.DefaultCost; cost++ {
		run(cost)
	}
}

// comparePassword covers normal, legacy and unknown accounts with an Argon2id
// operation and the same bcrypt work budget for enrolled costs up to 10.
func comparePassword(ctx context.Context, hash, raw, normalized string) (matched, legacy bool, err error) {
	encoded := []byte(hash)
	cost, costErr := bcrypt.Cost(encoded)
	legacy = costErr == nil
	err = passwordWork.run(ctx, func() {
		input := []byte(raw)
		if len(input) > 72 {
			input = []byte("mychef-long-password-padding")
		}
		if legacy && len(raw) <= 72 {
			matched = bcrypt.CompareHashAndPassword(encoded, input) == nil
			bcryptWorkBudget(cost, func(paddingCost int) {
				_ = bcrypt.CompareHashAndPassword(dummyBcrypt[paddingCost], input)
			})
		} else {
			_ = bcrypt.CompareHashAndPassword(dummyBcrypt[bcrypt.DefaultCost], input)
		}
		if _, _, valid := parseArgon2id(hash); valid {
			matched = verifyArgon2id(hash, normalized)
		} else {
			_ = argon2Digest(normalized, dummyArgonSalt[:])
		}
	})
	return matched, legacy, err
}
