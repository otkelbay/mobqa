package environment_test

import (
	"context"
	"errors"
	"testing"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/environment"
	"github.com/mobqa/backend/internal/usecase/project"
)

const (
	projID = "project-1"
	envID  = "env-1"
)

type fakeAccess struct {
	roles map[string]project.Role
}

func (a fakeAccess) Require(_ context.Context, _, userID string, need project.Role) (project.Role, error) {
	role, ok := a.roles[userID]
	if !ok {
		return "", fault.ErrNotFound
	}
	if !role.Includes(need) {
		return "", fault.ErrForbidden
	}
	return role, nil
}

func access() fakeAccess {
	return fakeAccess{roles: map[string]project.Role{"viewer": project.RoleViewer, "engineer": project.RoleEngineer}}
}

type fakeStore struct {
	stored  environment.Environment
	created []environment.NewEnvironment
	changes []environment.Change
	deleted []string
}

func staging() environment.Environment {
	return environment.Environment{ID: envID, ProjectID: projID, Name: "Staging", Variables: []environment.Variable{
		{Name: "LOGIN", Type: environment.TypePhone, Value: "+77010000000"},
		{Name: "PASSWORD", Type: environment.TypeSecret, Value: "hunter2"},
		{Name: "API_TOKEN", Type: environment.TypeSecret},
	}}
}

func (s *fakeStore) Environments(context.Context, string) ([]environment.Environment, error) {
	return []environment.Environment{s.stored}, nil
}

func (s *fakeStore) Environment(_ context.Context, projectID, id string) (environment.Environment, error) {
	if projectID != s.stored.ProjectID || id != s.stored.ID {
		return environment.Environment{}, fault.ErrNotFound
	}
	return s.stored, nil
}

func (s *fakeStore) CreateEnvironment(_ context.Context, in environment.NewEnvironment,
) (environment.Environment, error) {
	s.created = append(s.created, in)
	return environment.Environment{ID: "env-2", ProjectID: in.ProjectID, Name: in.Name, Variables: in.Variables}, nil
}

func (s *fakeStore) UpdateEnvironment(_ context.Context, change environment.Change) (environment.Environment, error) {
	s.changes = append(s.changes, change)
	updated := s.stored
	if change.Variables != nil {
		updated.Variables = change.Variables
	}
	return updated, nil
}

func (s *fakeStore) DeleteEnvironment(_ context.Context, _, id string) error {
	s.deleted = append(s.deleted, id)
	return nil
}

func requireVariables(t *testing.T, got, want []environment.Variable) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("variables = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("variable %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestListMasksSecrets(t *testing.T) {
	store := &fakeStore{stored: staging()}

	environments, err := environment.NewService(store, access()).List(context.Background(), "viewer", projID)

	if err != nil || len(environments) != 1 {
		t.Fatalf("List = %+v, %v", environments, err)
	}
	requireVariables(t, environments[0].Variables, []environment.Variable{
		{Name: "LOGIN", Type: environment.TypePhone, Value: "+77010000000"},
		{Name: "PASSWORD", Type: environment.TypeSecret, Value: "********"},
		{Name: "API_TOKEN", Type: environment.TypeSecret},
	})
	if store.stored.Variables[1].Value != "hunter2" {
		t.Fatal("masking changed the stored environment")
	}
}

func TestListHiddenFromOutsiders(t *testing.T) {
	_, err := environment.NewService(&fakeStore{stored: staging()}, access()).
		List(context.Background(), "outsider", projID)

	if !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("List = %v, want ErrNotFound", err)
	}
}

func TestUpdateKeepsMaskedAndEmptySecrets(t *testing.T) {
	store := &fakeStore{stored: staging()}

	updated, err := environment.NewService(store, access()).Update(context.Background(), environment.UpdateInput{
		UserID: "engineer",
		Change: environment.Change{ProjectID: projID, EnvironmentID: envID, Variables: []environment.Variable{
			{Name: "LOGIN", Type: environment.TypePhone, Value: "+77019999999"},
			{Name: "PASSWORD", Type: environment.TypeSecret, Value: "********"},
			{Name: "API_TOKEN", Type: environment.TypeSecret, Value: "new-token"},
			{Name: "PIN", Type: environment.TypeSecret, Value: ""},
		}},
	})

	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	requireVariables(t, store.changes[0].Variables, []environment.Variable{
		{Name: "LOGIN", Type: environment.TypePhone, Value: "+77019999999"},
		{Name: "PASSWORD", Type: environment.TypeSecret, Value: "hunter2"},
		{Name: "API_TOKEN", Type: environment.TypeSecret, Value: "new-token"},
		{Name: "PIN", Type: environment.TypeSecret, Value: ""},
	})
	if updated.Variables[2].Value != "********" {
		t.Fatalf("response leaked a secret: %+v", updated.Variables)
	}
}

func TestUpdateWithoutVariablesLeavesThemAlone(t *testing.T) {
	store := &fakeStore{stored: staging()}
	name := "QA"

	_, err := environment.NewService(store, access()).Update(context.Background(), environment.UpdateInput{
		UserID: "engineer", Change: environment.Change{ProjectID: projID, EnvironmentID: envID, Name: &name},
	})

	if err != nil || store.changes[0].Variables != nil || *store.changes[0].Name != "QA" {
		t.Fatalf("Update = %v, change %+v", err, store.changes)
	}
}

func TestCreateValidatesVariables(t *testing.T) {
	tests := []struct {
		name      string
		variables []environment.Variable
		detail    string
	}{
		{
			name:      "bad name",
			variables: []environment.Variable{{Name: "1PASS", Type: environment.TypeString}},
			detail:    "variable names must be letters, digits and underscores",
		},
		{
			name:      "unknown type",
			variables: []environment.Variable{{Name: "PASS", Type: "password"}},
			detail:    "variable type must be string, number, secret, phone, email or url",
		},
		{
			name: "duplicate name",
			variables: []environment.Variable{
				{Name: "PASS", Type: environment.TypeString}, {Name: "PASS", Type: environment.TypeSecret},
			},
			detail: "variable names must be unique",
		},
		{
			name:      "number that is not",
			variables: []environment.Variable{{Name: "AGE", Type: environment.TypeNumber, Value: "ten"}},
			detail:    "a variable value does not match its type",
		},
		{
			name:      "email with display name",
			variables: []environment.Variable{{Name: "MAIL", Type: environment.TypeEmail, Value: "A <a@b.co>"}},
			detail:    "a variable value does not match its type",
		},
		{
			name:      "url without host",
			variables: []environment.Variable{{Name: "SITE", Type: environment.TypeURL, Value: "/path"}},
			detail:    "a variable value does not match its type",
		},
		{
			name:      "phone with letters",
			variables: []environment.Variable{{Name: "PHONE", Type: environment.TypePhone, Value: "call me"}},
			detail:    "a variable value does not match its type",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{}

			_, err := environment.NewService(store, access()).Create(context.Background(), environment.CreateInput{
				UserID: "engineer", ProjectID: projID, Name: "Staging", Variables: tt.variables,
			})

			if detail, _ := fault.Detail(err); !errors.Is(err, fault.ErrInvalidInput) || detail != tt.detail {
				t.Fatalf("Create = %v, want %q", err, tt.detail)
			}
			if len(store.created) != 0 {
				t.Fatal("invalid environment reached the store")
			}
		})
	}
}

func TestCreateNeedsEngineer(t *testing.T) {
	_, err := environment.NewService(&fakeStore{}, access()).Create(context.Background(),
		environment.CreateInput{UserID: "viewer", ProjectID: projID, Name: "Staging"})

	if !errors.Is(err, fault.ErrForbidden) {
		t.Fatalf("Create = %v, want ErrForbidden", err)
	}
}

func TestCreateMasksResponse(t *testing.T) {
	store := &fakeStore{}

	created, err := environment.NewService(store, access()).Create(context.Background(), environment.CreateInput{
		UserID: "engineer", ProjectID: projID, Name: "Prod", IsDefault: true,
		Variables: []environment.Variable{{Name: "PASSWORD", Type: environment.TypeSecret, Value: "hunter2"}},
	})

	if err != nil || created.Variables[0].Value != "********" || store.created[0].Variables[0].Value != "hunter2" {
		t.Fatalf("Create = %+v, %v; stored %+v", created, err, store.created)
	}
	if !store.created[0].IsDefault {
		t.Fatal("default flag lost")
	}
}

func TestResolveReturnsSecrets(t *testing.T) {
	resolved, err := environment.NewService(&fakeStore{stored: staging()}, access()).
		Resolve(context.Background(), projID, envID)

	if err != nil || resolved.Variables[1].Value != "hunter2" {
		t.Fatalf("Resolve = %+v, %v", resolved, err)
	}
}

func TestDelete(t *testing.T) {
	store := &fakeStore{}

	err := environment.NewService(store, access()).Delete(context.Background(), "engineer", projID, envID)

	if err != nil || len(store.deleted) != 1 || store.deleted[0] != envID {
		t.Fatalf("Delete = %v, deleted %v", err, store.deleted)
	}
}
