package billing

import (
	"context"
	"testing"
	"time"
)

func TestPlayServiceDrainsExistingWorkWhilePullWaitsAndWakesAfterRetention(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	pulling := make(chan struct{}, 1)
	allowPull := make(chan struct{})
	drained := make(chan struct{}, 2)
	polled := make(chan struct{}, 1)
	service := PlayService{
		pull: func(ctx context.Context) (int, error) {
			pulling <- struct{}{}
			select {
			case <-allowPull:
				return 1, ErrStoreUnavailable // Other work still made it to disk.
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		},
		known: func(context.Context) (ReconcileBatch, error) {
			polled <- struct{}{}
			return ReconcileBatch{Processed: 1}, nil
		},
		drain: func(context.Context) (ReconcileBatch, error) {
			drained <- struct{}{}
			return ReconcileBatch{Processed: 1}, nil
		},
	}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		service.run(ctx, nil, nil, nil)
	}()
	for _, stage := range []struct {
		label string
		done  <-chan struct{}
	}{{"pull opened", pulling}, {"startup drain while pull was blocked", drained}, {"poll while pull was blocked", polled}} {
		select {
		case <-stage.done:
		case <-time.After(time.Second):
			t.Fatal(stage.label)
		}
	}
	close(allowPull)
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("the durable notice did not wake the queue after pull")
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("the worker did not stop both loops")
	}
}
