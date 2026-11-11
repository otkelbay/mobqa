package worker

import (
	"context"
	"sync"
)

type dispatcher struct {
	kind    string
	wake    chan struct{}
	running *runningJobs
}

// Subscribed claims once, since a job enqueued before the subscription woke nobody.
func (d dispatcher) Subscribed() {
	poke(d.wake)
}

func (d dispatcher) Receive(n Notification) {
	switch n.Channel {
	case WakeChannel(d.kind):
		poke(d.wake)
	case CancelChannel:
		d.running.cancel(n.Payload)
	}
}

// runningJobs maps the jobs this pool is handling to their cancel functions. Cancel notifications go to
// every pool, so an unknown ID is normal and ignored.
type runningJobs struct {
	mu      sync.Mutex
	cancels map[string]context.CancelCauseFunc
}

func (r *runningJobs) add(id string, cancel context.CancelCauseFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancels[id] = cancel
}

func (r *runningJobs) remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.cancels, id)
}

func (r *runningJobs) cancel(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cancel, ok := r.cancels[id]; ok {
		cancel(errCancelRequested)
	}
}
