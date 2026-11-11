// Package clock is the time seam: production code reads time only through Clock so tests control it.
package clock

import "time"

type Clock interface {
	Now() time.Time
}

type System struct{}

// Now returns UTC truncated to microseconds, the precision Postgres stores.
func (System) Now() time.Time {
	return time.Now().UTC().Truncate(time.Microsecond)
}
