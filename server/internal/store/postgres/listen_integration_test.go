//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mobqa/backend/internal/clock/clocktest"
	"github.com/mobqa/backend/internal/dbtest"
	"github.com/mobqa/backend/internal/store/postgres"
	"github.com/mobqa/backend/internal/worker"
)

type receiver struct {
	subscribed    chan struct{}
	notifications chan worker.Notification
}

func newReceiver() receiver {
	return receiver{subscribed: make(chan struct{}, 1), notifications: make(chan worker.Notification, 100)}
}

func (r receiver) Subscribed() {
	r.subscribed <- struct{}{}
}

func (r receiver) Receive(n worker.Notification) {
	r.notifications <- n
}

func TestListenDeliversWakeAndCancelNotifications(t *testing.T) {
	db := dbtest.New(t, clocktest.New(time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)))
	jobs := postgres.NewJobs(db)
	kind := "listen_probe"
	received := newReceiver()
	ctx, cancel := context.WithCancel(context.Background())
	listened := make(chan error, 1)
	go func() { listened <- db.Listen(ctx, []string{worker.WakeChannel(kind), worker.CancelChannel}, received) }()
	awaitSignal(t, received.subscribed, "subscription")

	if _, err := jobs.Enqueue(context.Background(), worker.EnqueueInput{Kind: kind, Payload: []byte(`{}`),
		MaxAttempts: 1}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if wake := awaitSignal(t, received.notifications, "wake"); wake.Channel != worker.WakeChannel(kind) ||
		wake.Payload != "" {
		t.Fatalf("wake = %+v", wake)
	}

	// A kind of its own, so the claim cannot pick the probe job enqueued above.
	id := enqueue(t, jobs, renderJob())
	if job, found := claim(t, jobs, ownerA); !found || job.ID != id {
		t.Fatalf("Claim = %+v, %v, want job %s", job, found, id)
	}
	if err := jobs.RequestCancel(context.Background(), id); err != nil {
		t.Fatalf("RequestCancel: %v", err)
	}
	if cancelled := awaitSignal(t, received.notifications, "cancel"); cancelled.Channel != worker.CancelChannel ||
		cancelled.Payload != id {
		t.Fatalf("cancel = %+v, want job %s", cancelled, id)
	}

	cancel()
	if err := awaitSignal(t, listened, "Listen return"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Listen returned %v, want context.Canceled", err)
	}
}

func awaitSignal[T any](t *testing.T, signals <-chan T, what string) T {
	t.Helper()
	select {
	case value := <-signals:
		return value
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		panic("unreachable")
	}
}
