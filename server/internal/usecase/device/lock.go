package device

import (
	"context"
	"sync"
)

// Locks lets one holder at a time use a device, so that a run and an authoring session never send
// commands to the same phone in turn. Locks live in memory and so order one process: a hub for its own
// devices, and the API, which alone holds the hub links.
type Locks struct {
	mu    sync.Mutex
	slots map[string]chan struct{}
}

func NewLocks() *Locks {
	return &Locks{slots: map[string]chan struct{}{}}
}

// Lock waits until the device is free or ctx ends; the returned func frees it.
func (l *Locks) Lock(ctx context.Context, key string) (func(), error) {
	slot := l.slot(key)
	select {
	case slot <- struct{}{}:
		return func() { <-slot }, nil
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
}

func (l *Locks) slot(key string) chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	slot, ok := l.slots[key]
	if !ok {
		slot = make(chan struct{}, 1)
		l.slots[key] = slot
	}
	return slot
}
