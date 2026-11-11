package fault_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/mobqa/backend/internal/fault"
)

func TestDetailSurvivesWrapping(t *testing.T) {
	err := fmt.Errorf("register: %w", fault.WithDetail(fault.ErrConflict, "email is already registered"))

	detail, ok := fault.Detail(err)

	if !ok || detail != "email is already registered" {
		t.Fatalf("Detail = %q, %v", detail, ok)
	}
	if !errors.Is(err, fault.ErrConflict) {
		t.Fatal("wrapped detail lost its kind")
	}
}

func TestDetailAbsent(t *testing.T) {
	if detail, ok := fault.Detail(fmt.Errorf("user: %w", fault.ErrNotFound)); ok {
		t.Fatalf("Detail = %q, want none", detail)
	}
}
