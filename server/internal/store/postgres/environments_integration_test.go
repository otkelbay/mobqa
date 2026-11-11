//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/secret"
	"github.com/mobqa/backend/internal/store/postgres"
	"github.com/mobqa/backend/internal/usecase/environment"
)

func newEnvironments(t *testing.T) (*postgres.Environments, string) {
	t.Helper()
	f := newProjects(t)
	box, err := secret.NewBox([]byte(strings.Repeat("k", secret.KeySize)))
	if err != nil {
		t.Fatalf("NewBox: %v", err)
	}
	return postgres.NewEnvironments(f.db, box), f.create(t, f.account(t, "ada").ID, "Shop").ID
}

func createEnvironment(t *testing.T, environments *postgres.Environments, in environment.NewEnvironment,
) environment.Environment {
	t.Helper()
	created, err := environments.CreateEnvironment(context.Background(), in)
	if err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}
	return created
}

func TestEnvironmentsSecretsRoundTrip(t *testing.T) {
	environments, projectID := newEnvironments(t)
	variables := []environment.Variable{
		{Name: "LOGIN", Type: environment.TypeEmail, Label: "Login", Value: "qa@example.com"},
		{Name: "PASSWORD", Type: environment.TypeSecret, Rule: "eight characters", Value: "hunter22"},
		{Name: "TOKEN", Type: environment.TypeSecret},
	}

	created := createEnvironment(t, environments, environment.NewEnvironment{
		ProjectID: projectID, Name: "Staging", Variables: variables,
	})

	found, err := environments.Environment(context.Background(), projectID, created.ID)
	if err != nil || len(found.Variables) != 3 {
		t.Fatalf("Environment = %+v, %v", found, err)
	}
	for i := range variables {
		if found.Variables[i] != variables[i] {
			t.Fatalf("variable %d = %+v, want %+v", i, found.Variables[i], variables[i])
		}
	}
}

func TestEnvironmentsOrderAndSingleDefault(t *testing.T) {
	environments, projectID := newEnvironments(t)
	staging := createEnvironment(t, environments,
		environment.NewEnvironment{ProjectID: projectID, Name: "Staging", IsDefault: true})
	prod := createEnvironment(t, environments,
		environment.NewEnvironment{ProjectID: projectID, Name: "Prod", IsDefault: true})
	ctx := context.Background()

	listed, err := environments.Environments(ctx, projectID)
	if err != nil || len(listed) != 2 || listed[0].ID != staging.ID || listed[0].IsDefault ||
		listed[1].ID != prod.ID || !listed[1].IsDefault || listed[1].Position != 1 {
		t.Fatalf("Environments = %+v, %v", listed, err)
	}

	isDefault := true
	if _, err := environments.UpdateEnvironment(ctx, environment.Change{
		ProjectID: projectID, EnvironmentID: staging.ID, IsDefault: &isDefault,
	}); err != nil {
		t.Fatalf("UpdateEnvironment: %v", err)
	}
	listed, _ = environments.Environments(ctx, projectID)
	if !listed[0].IsDefault || listed[1].IsDefault {
		t.Fatalf("defaults after switch = %+v", listed)
	}
}

func TestEnvironmentsUpdateKeepsVariablesWhenNil(t *testing.T) {
	environments, projectID := newEnvironments(t)
	created := createEnvironment(t, environments, environment.NewEnvironment{
		ProjectID: projectID, Name: "Staging",
		Variables: []environment.Variable{{Name: "PASSWORD", Type: environment.TypeSecret, Value: "hunter22"}},
	})
	name := "QA"

	updated, err := environments.UpdateEnvironment(context.Background(), environment.Change{
		ProjectID: projectID, EnvironmentID: created.ID, Name: &name,
	})

	if err != nil || updated.Name != "QA" || len(updated.Variables) != 1 || updated.Variables[0].Value != "hunter22" {
		t.Fatalf("UpdateEnvironment = %+v, %v", updated, err)
	}
}

func TestEnvironmentsNameConflict(t *testing.T) {
	environments, projectID := newEnvironments(t)
	createEnvironment(t, environments, environment.NewEnvironment{ProjectID: projectID, Name: "Staging"})
	prod := createEnvironment(t, environments, environment.NewEnvironment{ProjectID: projectID, Name: "Prod"})
	ctx := context.Background()

	_, err := environments.CreateEnvironment(ctx, environment.NewEnvironment{ProjectID: projectID, Name: "Staging"})
	if !errors.Is(err, fault.ErrConflict) {
		t.Fatalf("CreateEnvironment = %v, want ErrConflict", err)
	}
	name := "Staging"
	_, err = environments.UpdateEnvironment(ctx, environment.Change{
		ProjectID: projectID, EnvironmentID: prod.ID, Name: &name,
	})
	if !errors.Is(err, fault.ErrConflict) {
		t.Fatalf("UpdateEnvironment = %v, want ErrConflict", err)
	}
}

func TestEnvironmentsScopedToProject(t *testing.T) {
	environments, projectID := newEnvironments(t)
	created := createEnvironment(t, environments, environment.NewEnvironment{ProjectID: projectID, Name: "Staging"})
	ctx := context.Background()
	otherProject := "0190f3c4-0000-7000-8000-0000000000aa"

	if _, err := environments.Environment(ctx, otherProject, created.ID); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("Environment(other project) = %v", err)
	}
	if err := environments.DeleteEnvironment(ctx, otherProject, created.ID); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("DeleteEnvironment(other project) = %v", err)
	}
	if _, err := environments.Environment(ctx, projectID, "nope"); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("Environment(malformed id) = %v", err)
	}
	if err := environments.DeleteEnvironment(ctx, projectID, created.ID); err != nil {
		t.Fatalf("DeleteEnvironment: %v", err)
	}
	if _, err := environments.Environment(ctx, projectID, created.ID); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("Environment after delete = %v", err)
	}
}
