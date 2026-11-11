package httpapi

import (
	"sync"
	"time"
)

const (
	passwordLimitWindow = 15 * time.Minute
	passwordAccountMax  = 5
	passwordSourceMax   = 15
	passwordKeyMax      = 10000
)

type passwordAttemptOutcome int

const (
	passwordUncounted passwordAttemptOutcome = iota
	passwordWrong
	passwordChanged
)

type passwordAccountAttempts struct {
	failures []time.Time
	pending  int
}

type passwordLimiter struct {
	mu       sync.Mutex
	now      func() time.Time
	accounts map[int64]*passwordAccountAttempts
	sources  map[string][]time.Time
	nextGC   time.Time
}

func (l *passwordLimiter) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

func withinPasswordWindow(events []time.Time, now time.Time) []time.Time {
	for len(events) > 0 && !events[0].Add(passwordLimitWindow).After(now) {
		events = events[1:]
	}
	return events
}

func passwordWait(now, oldest time.Time) int {
	remaining := oldest.Add(passwordLimitWindow).Sub(now)
	return max(1, int((remaining+time.Second-1)/time.Second))
}

func (l *passwordLimiter) expire(now time.Time) {
	if now.Before(l.nextGC) {
		return
	}
	l.nextGC = now.Add(time.Minute)
	for id, account := range l.accounts {
		account.failures = withinPasswordWindow(account.failures, now)
		if len(account.failures) == 0 && account.pending == 0 {
			delete(l.accounts, id)
		}
	}
	for source, attempts := range l.sources {
		attempts = withinPasswordWindow(attempts, now)
		if len(attempts) == 0 {
			delete(l.sources, source)
		} else {
			l.sources[source] = attempts
		}
	}
}

// begin records an allowed source attempt and reserves an account slot before
// credential verification. Every reservation must be finished exactly once.
func (l *passwordLimiter) begin(id int64, source string) (finish func(passwordAttemptOutcome), retry int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock()
	if l.accounts == nil {
		l.accounts = make(map[int64]*passwordAccountAttempts)
		l.sources = make(map[string][]time.Time)
	}
	l.expire(now)
	attempts, knownSource := l.sources[source]
	attempts = withinPasswordWindow(attempts, now)
	account := l.accounts[id]
	if account != nil {
		account.failures = withinPasswordWindow(account.failures, now)
		switch {
		case len(account.failures) >= passwordAccountMax:
			retry = passwordWait(now, account.failures[0])
		case len(account.failures)+account.pending >= passwordAccountMax:
			retry = 1
		}
	}
	if len(attempts) >= passwordSourceMax {
		return nil, max(retry, passwordWait(now, attempts[0]))
	}
	if !knownSource && len(l.sources) >= passwordKeyMax {
		return nil, max(retry, 1)
	}
	l.sources[source] = append(attempts, now)
	if retry != 0 {
		return nil, retry
	}
	if account == nil {
		if len(l.accounts) >= passwordKeyMax {
			return nil, 1
		}
		account = new(passwordAccountAttempts)
		l.accounts[id] = account
	}
	account.pending++
	return func(outcome passwordAttemptOutcome) {
		l.mu.Lock()
		defer l.mu.Unlock()
		account.pending--
		now := l.clock()
		account.failures = withinPasswordWindow(account.failures, now)
		switch outcome {
		case passwordWrong:
			account.failures = append(account.failures, now)
		case passwordChanged:
			account.failures = nil
		}
		if account.pending == 0 && len(account.failures) == 0 {
			delete(l.accounts, id)
		}
	}, 0
}
