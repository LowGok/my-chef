package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestNewPasswordPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, raw, normalized string
		want                  error
	}{
		{name: "below minimum", raw: "abcdefghi", want: ErrPasswordLength},
		{name: "exact minimum without composition", raw: "abcdefghij", normalized: "abcdefghij"},
		{name: "spaces retained", raw: " a phrase ", normalized: " a phrase "},
		{name: "blocked whole password", raw: "1234567890", want: ErrPasswordCommon},
		{name: "blocked word as part of phrase", raw: "1234567890 plus words", normalized: "1234567890 plus words"},
		{name: "decomposed upper boundary", raw: strings.Repeat("e\u0301", 128), normalized: strings.Repeat("é", 128)},
		{name: "expanded lower boundary", raw: strings.Repeat("क़", 5), normalized: strings.Repeat("क़", 5)},
		{name: "exact byte bound", raw: strings.Repeat("😀", 128), normalized: strings.Repeat("😀", 128)},
		{name: "past byte bound", raw: strings.Repeat("😀", 128) + "a", want: ErrPasswordLength},
		{name: "past rune bound", raw: strings.Repeat("a", 129), want: ErrPasswordLength},
		{name: "invalid UTF-8", raw: "some words\xff", want: ErrPasswordLength},
		{name: "explicit replacement character", raw: "word � phrase", normalized: "word � phrase"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := checkNewPassword(tc.raw)
			if !errors.Is(err, tc.want) || got != tc.normalized {
				t.Fatalf("normalized policy result or error differed: %v", err)
			}
		})
	}
}

func TestArgon2idPasswordFormat(t *testing.T) {
	ctx := context.Background()
	input := "a long test phrase"
	first, err := newPasswordHash(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newPasswordHash(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(first, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" || parts[3] != "m=19456,t=2,p=1" {
		t.Fatal("unexpected hash format or parameters")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) != 16 || parts[4] == strings.Split(second, "$")[4] {
		t.Fatal("expected a fresh 16-byte salt")
	}
	digest, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(digest) != 32 {
		t.Fatal("expected a 32-byte digest")
	}
	for _, malformed := range []string{
		strings.Replace(first, "m=19456", "m=2147483647", 1),
		strings.Replace(first, "p=1", "p=3", 1),
		strings.Replace(first, "v=19", "v=16", 1),
		first + "AAAA",
		first + "$",
	} {
		if _, _, ok := parseArgon2id(malformed); ok {
			t.Fatal("malformed or unsupported hash accepted")
		}
	}
	if !verifyArgon2id(first, input) || verifyArgon2id(first, input+"!") {
		t.Fatal("password verification did not distinguish correct input")
	}
}

func TestPasswordWorkConcurrency(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered := make(chan struct{}, 8)
	release := make(chan struct{})
	defer close(release)
	for range 4 {
		go func() {
			_ = passwordWork.run(ctx, func() {
				entered <- struct{}{}
				<-release
			})
		}()
	}
	for range 4 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("four operations did not start together")
		}
	}
	if cap(passwordWork) != 4 || len(passwordWork) != 4 {
		t.Fatal("the shared process budget is not saturated at four")
	}
	waiting, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- passwordWork.run(waiting, func() { entered <- struct{}{} }) }()
	stop()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || len(entered) != 0 {
			t.Fatal("canceled waiter performed password work")
		}
	case <-ctx.Done():
		t.Fatal("canceled waiter did not exit")
	}
}

func TestBcryptWorkBudget(t *testing.T) {
	for cost := bcrypt.MinCost; cost <= bcrypt.DefaultCost+1; cost++ {
		steps := uint64(1) << cost
		bcryptWorkBudget(cost, func(padding int) { steps += uint64(1) << padding })
		want := uint64(1) << max(cost, bcrypt.DefaultCost)
		if steps != want {
			t.Fatalf("cost %d: got %d rounds, want %d", cost, steps, want)
		}
	}
}

func TestInvalidLoginTimingEnvelope(t *testing.T) {
	if os.Getenv("MYCHEF_CHECK_PASSWORD_TIMING") != "1" {
		t.Skip("run separately on the target host without concurrent test load")
	}
	ctx := context.Background()
	s := openPasswordTestStore(t)
	m := New(s)
	cheap := addLegacyUser(t, s, "cheap", "old legacy phrase", bcrypt.MinCost)
	defaultCost := addLegacyUser(t, s, "default", "old legacy phrase", bcrypt.DefaultCost)
	current, err := m.Register(ctx, Registration{
		Email: "current@example.test", Username: "current", FirstName: "Current", LastName: "Test", Password: "a valid test phrase",
	})
	if err != nil {
		t.Fatal(err)
	}
	cohorts := []struct {
		name, email, input string
		timings            []time.Duration
	}{
		{name: "missing", email: "missing@example.test", input: "a different test phrase"},
		{name: "bcrypt minimum", email: cheap.Email, input: "a different test phrase"},
		{name: "bcrypt default", email: defaultCost.Email, input: "a different test phrase"},
		{name: "argon2id", email: current.Email, input: "a different test phrase"},
		{name: "long bcrypt candidate", email: defaultCost.Email, input: strings.Repeat("x", 73)},
		{name: "long argon2id candidate", email: current.Email, input: strings.Repeat("x", 73)},
	}
	for round := range 7 {
		for step := range cohorts {
			cohort := &cohorts[(round+step)%len(cohorts)]
			start := time.Now()
			if _, err := m.Login(ctx, cohort.email, cohort.input); !errors.Is(err, ErrCredentials) {
				t.Fatal("invalid attempt did not return generic invalid credentials")
			}
			if round > 0 {
				cohort.timings = append(cohort.timings, time.Since(start))
			}
		}
	}
	fastest, slowest := time.Second, time.Duration(0)
	for _, cohort := range cohorts {
		slices.Sort(cohort.timings)
		median := (cohort.timings[2] + cohort.timings[3]) / 2
		t.Logf("%s median: %v", cohort.name, median)
		fastest, slowest = min(fastest, median), max(slowest, median)
	}
	if slowest >= time.Second || slowest > fastest+fastest/2 {
		t.Fatal("median login verification fell outside the accepted timing envelope")
	}
}
