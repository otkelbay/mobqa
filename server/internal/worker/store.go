package worker

import (
	"context"
	"time"
)

// Store persists jobs. Every method that touches a claimed job matches on its owner and returns
// ErrLeaseLost when someone else holds it, so a stale owner can never overwrite a newer one.
type Store interface {
	// Claim leases the oldest runnable job of a kind, including one whose previous lease lapsed, and
	// counts the attempt. It reports false when nothing is runnable.
	Claim(ctx context.Context, in ClaimInput) (Job, bool, error)
	Heartbeat(ctx context.Context, in HeartbeatInput) (Lease, error)
	Finish(ctx context.Context, in FinishInput) error
}

// Signals delivers database notifications. Listen blocks until ctx ends or the subscription breaks.
type Signals interface {
	Listen(ctx context.Context, channels []string, receiver Receiver) error
}

// Receiver is called on the listening goroutine and must not block. Subscribed is called once the
// subscription is in place, so that anything sent before it can be picked up by other means.
type Receiver interface {
	Subscribed()
	Receive(n Notification)
}

type ClaimInput struct {
	Kind  string
	Owner string
	Lease time.Duration
}

type HeartbeatInput struct {
	JobID string
	Owner string
	Lease time.Duration
}

// Lease is what a heartbeat learns besides the extension. CancelRequested backs up the cancel
// notification, which is lost if the listener was reconnecting when it was sent.
type Lease struct {
	CancelRequested bool
}

type FinishInput struct {
	JobID string
	Owner string
	// State is terminal, or StateQueued to run the job again after RetryIn.
	State   State
	Error   string
	RetryIn time.Duration
	// RefundAttempt returns the attempt a shutdown interrupted, so draining never exhausts a job.
	RefundAttempt bool
}
