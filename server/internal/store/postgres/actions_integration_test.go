//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/store/postgres"
	"github.com/mobqa/backend/internal/usecase/action"
)

func newActions(t *testing.T) (*postgres.Actions, projectFixture, string) {
	t.Helper()
	f := newProjects(t)
	return postgres.NewActions(f.db), f, f.create(t, f.account(t, "ada").ID, "Shop").ID
}

func otpAction(name string) action.Definition {
	return action.Definition{Name: name, Description: "Last code", Request: action.Request{
		Method: action.MethodGet, URL: "{{API_URL}}/otp", Headers: []action.Header{{Name: "X-Key", Value: "{{KEY}}"}},
		ExpectedStatus: 200, Extract: []action.Extraction{{Variable: "OTP", Pattern: `(\d{4})`}},
	}}
}

func TestActionsRoundTripAndKeepNamesUnique(t *testing.T) {
	actions, _, projectID := newActions(t)
	ctx := context.Background()

	created, err := actions.CreateAction(ctx, projectID, otpAction("Fetch OTP"))
	if err != nil {
		t.Fatalf("CreateAction: %v", err)
	}
	found, err := actions.Action(ctx, projectID, created.ID)
	if err != nil || found.Name != "Fetch OTP" || found.Request.Headers[0].Value != "{{KEY}}" ||
		found.Request.Extract[0].Pattern != `(\d{4})` || !found.CreatedAt.Equal(found.UpdatedAt) {
		t.Fatalf("Action = %+v, %v", found, err)
	}
	if _, err := actions.CreateAction(ctx, projectID, otpAction("Fetch OTP")); !errors.Is(err, fault.ErrConflict) {
		t.Fatalf("duplicate CreateAction error = %v", err)
	}
	seed, err := actions.CreateAction(ctx, projectID, otpAction("Create user"))
	if err != nil {
		t.Fatalf("CreateAction seed: %v", err)
	}
	if _, err := actions.UpdateAction(ctx, projectID, seed.ID, otpAction("Fetch OTP")); !errors.Is(err,
		fault.ErrConflict) {
		t.Fatalf("renaming onto a taken name error = %v", err)
	}
	changed := otpAction("Seed user")
	changed.Request.Method, changed.Request.Body, changed.Request.Headers = action.MethodPost, `{"x":1}`, nil
	updated, err := actions.UpdateAction(ctx, projectID, seed.ID, changed)
	if err != nil || updated.Name != "Seed user" || updated.Request.Method != action.MethodPost ||
		len(updated.Request.Headers) != 0 {
		t.Fatalf("UpdateAction = %+v, %v", updated, err)
	}
	list, err := actions.Actions(ctx, projectID)
	if err != nil || len(list) != 2 || list[0].Name != "Fetch OTP" || list[1].Name != "Seed user" {
		t.Fatalf("Actions = %+v, %v", list, err)
	}
}

func TestActionsAreScopedToTheirProject(t *testing.T) {
	actions, f, projectID := newActions(t)
	ctx := context.Background()
	created, err := actions.CreateAction(ctx, projectID, otpAction("Fetch OTP"))
	if err != nil {
		t.Fatalf("CreateAction: %v", err)
	}
	other := f.create(t, f.account(t, "bob").ID, "Other").ID

	if _, err := actions.Action(ctx, other, created.ID); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("Action from another project error = %v", err)
	}
	if _, err := actions.UpdateAction(ctx, other, created.ID, otpAction("X")); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("UpdateAction from another project error = %v", err)
	}
	for _, id := range []string{created.ID, uuid.NewString(), "not-a-uuid"} {
		if err := actions.DeleteAction(ctx, other, id); !errors.Is(err, fault.ErrNotFound) {
			t.Fatalf("DeleteAction(%s) from another project error = %v", id, err)
		}
	}
	if err := actions.DeleteAction(ctx, projectID, created.ID); err != nil {
		t.Fatalf("DeleteAction: %v", err)
	}
	if list, err := actions.Actions(ctx, projectID); err != nil || len(list) != 0 {
		t.Fatalf("Actions after delete = %+v, %v", list, err)
	}
}
