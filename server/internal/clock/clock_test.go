package clock

import (
	"testing"
	"time"
)

func TestSystemNowIsUTCMicroseconds(t *testing.T) {
	now := System{}.Now()

	if now.Location() != time.UTC {
		t.Fatalf("location = %v, want UTC", now.Location())
	}
	if now.Nanosecond()%1000 != 0 {
		t.Fatalf("nanoseconds = %d, want microsecond precision", now.Nanosecond())
	}
}
