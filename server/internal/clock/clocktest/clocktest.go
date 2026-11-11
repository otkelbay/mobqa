// Package clocktest provides a clock that tests move by hand.
package clocktest

import (
	"sync"
	"time"
)

// Manual is safe for concurrent use; it only changes when the test calls Advance.
type Manual struct {
	mu  sync.Mutex
	now time.Time
}

func New(now time.Time) *Manual {
	return &Manual{now: now.UTC().Truncate(time.Microsecond)}
}

func (m *Manual) Now() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.now
}

func (m *Manual) Advance(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = m.now.Add(d)
}
