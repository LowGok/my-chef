package notifications

import (
	"context"
	"time"
)

type State uint8

const (
	Uncertain State = iota
	Accepted
	Temporary
	Permanent
	InvalidRegistration
)

// Delivery values are private and must never be used as diagnostics.
type Delivery struct {
	Provider  string
	Token     string
	Reference string
	Deadline  time.Time
}

type Result struct {
	State      State
	RetryAfter time.Duration
}

type Send func(context.Context, Delivery) Result

func RetryDelay(attempt int, providerWait time.Duration) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	return max(providerWait, time.Minute<<min(attempt-1, 4))
}
