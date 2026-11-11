// Package worker runs background jobs with at-most-one-owner leases. It knows nothing about what a job
// does: domain code enqueues jobs and implements Handler; storage implements Store and Signals.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mobqa/backend/internal/fault"
)

type State string

const (
	StateQueued    State = "queued"
	StateRunning   State = "running"
	StateSucceeded State = "succeeded"
	StateFailed    State = "failed"
	StateCancelled State = "cancelled"
)

type Job struct {
	ID          string
	Kind        string
	Payload     []byte
	Attempt     int
	MaxAttempts int
	// CancelRequested is set when cancellation arrived while a previous owner held the lease.
	CancelRequested bool
}

type Handler interface {
	Handle(ctx context.Context, job Job) error
}

type HandlerFunc func(ctx context.Context, job Job) error

func (f HandlerFunc) Handle(ctx context.Context, job Job) error {
	return f(ctx, job)
}

// ErrLeaseLost means another owner now holds the job; the current owner must stop touching it.
var ErrLeaseLost = errors.New("job lease lost")

type permanentError struct {
	err error
}

func (p permanentError) Error() string { return p.err.Error() }
func (p permanentError) Unwrap() error { return p.err }

// Permanent marks a handler error as not worth retrying.
func Permanent(err error) error {
	return permanentError{err: err}
}

// IsPermanent reports whether err, or an error it wraps, was marked by Permanent.
func IsPermanent(err error) bool {
	var permanent permanentError
	return errors.As(err, &permanent)
}

type EnqueueInput struct {
	Kind string
	// Payload is the JSON document the handler decodes.
	Payload     []byte
	MaxAttempts int
	// Delay postpones the first attempt; the store adds it to its own clock so callers need none.
	Delay time.Duration
}

// maxKindLength keeps kinds usable as LISTEN channel suffixes (Postgres identifiers are 63 bytes).
const maxKindLength = 40

func (in EnqueueInput) Validate() error {
	if err := validateKind(in.Kind); err != nil {
		return err
	}
	if in.MaxAttempts < 1 {
		return fmt.Errorf("max attempts %d: %w", in.MaxAttempts, fault.ErrInvalidInput)
	}
	if in.Delay < 0 {
		return fmt.Errorf("negative delay %s: %w", in.Delay, fault.ErrInvalidInput)
	}
	if !json.Valid(in.Payload) {
		return fmt.Errorf("payload is not JSON: %w", fault.ErrInvalidInput)
	}
	return nil
}

func validateKind(kind string) error {
	if kind == "" || len(kind) > maxKindLength || strings.Trim(kind, "abcdefghijklmnopqrstuvwxyz_") != "" {
		return fmt.Errorf("job kind %q: %w", kind, fault.ErrInvalidInput)
	}
	return nil
}

// WakeChannel is the notification channel that announces new jobs of a kind.
func WakeChannel(kind string) string {
	return "jobs_" + kind
}

// CancelChannel carries the IDs of jobs whose cancellation was requested.
const CancelChannel = "job_cancel"

type Notification struct {
	Channel string
	Payload string
}
