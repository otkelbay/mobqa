// Package environment manages a project's named variable sets ("Staging", "Prod") that tests run
// against. Secret values never leave this package unmasked except through Resolve, which serves runs.
package environment

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/project"
)

type VariableType string

const (
	TypeString VariableType = "string"
	TypeNumber VariableType = "number"
	TypeSecret VariableType = "secret"
	TypePhone  VariableType = "phone"
	TypeEmail  VariableType = "email"
	TypeURL    VariableType = "url"
)

// Mask replaces secret values in responses. Writing it back, or an empty value, keeps the stored secret.
const Mask = "********"

const (
	maxNameLength  = 60
	maxVariables   = 200
	maxLabelLength = 100
	maxRuleLength  = 200
	// maxValueLength fits tokens and certificates while keeping a run's environment small.
	maxValueLength = 4096
)

var phonePattern = regexp.MustCompile(`^\+?[0-9][0-9 ()-]{4,19}$`)

type Variable struct {
	Name  string
	Type  VariableType
	Label string
	// Rule is a hint for people and the authoring agent, such as "+7 and ten digits".
	Rule  string
	Value string
}

type Environment struct {
	ID        string
	ProjectID string
	Name      string
	IsDefault bool
	Position  int
	Variables []Variable
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (v Variable) validate() error {
	if !project.ValidVariableName(v.Name) {
		return fault.WithDetail(fault.ErrInvalidInput, "variable names must be letters, digits and underscores")
	}
	if utf8.RuneCountInString(v.Label) > maxLabelLength || utf8.RuneCountInString(v.Rule) > maxRuleLength {
		return fault.WithDetail(fault.ErrInvalidInput, "labels are limited to 100 characters and rules to 200")
	}
	if len(v.Value) > maxValueLength {
		return fault.WithDetail(fault.ErrInvalidInput, "values must be at most 4096 bytes")
	}
	if v.Value == "" {
		return validateType(v.Type)
	}
	return v.validateValue()
}

func validateType(kind VariableType) error {
	switch kind {
	case TypeString, TypeNumber, TypeSecret, TypePhone, TypeEmail, TypeURL:
		return nil
	default:
		return fault.WithDetail(fault.ErrInvalidInput, "variable type must be string, number, secret, phone, email or url")
	}
}

func (v Variable) validateValue() error {
	valid := true
	switch v.Type {
	case TypeString, TypeSecret:
	case TypeNumber:
		_, err := strconv.ParseFloat(v.Value, 64)
		valid = err == nil
	case TypePhone:
		valid = phonePattern.MatchString(v.Value)
	case TypeEmail:
		address, err := mail.ParseAddress(v.Value)
		valid = err == nil && address.Address == v.Value
	case TypeURL:
		parsed, err := url.Parse(v.Value)
		valid = err == nil && parsed.Scheme != "" && parsed.Host != ""
	default:
		return validateType(v.Type)
	}
	if !valid {
		return fault.WithDetail(fault.ErrInvalidInput, "a variable value does not match its type")
	}
	return nil
}

func validateName(name string) error {
	if name == "" || strings.TrimSpace(name) != name || utf8.RuneCountInString(name) > maxNameLength {
		return fault.WithDetail(fault.ErrInvalidInput, "name must be 1 to 60 characters without surrounding spaces")
	}
	return nil
}

func validateVariables(variables []Variable) error {
	if len(variables) > maxVariables {
		return fault.WithDetail(fault.ErrInvalidInput, "an environment holds at most 200 variables")
	}
	for i, variable := range variables {
		if err := variable.validate(); err != nil {
			return err
		}
		if slices.ContainsFunc(variables[:i], func(other Variable) bool { return other.Name == variable.Name }) {
			return fault.WithDetail(fault.ErrInvalidInput, "variable names must be unique")
		}
	}
	return nil
}

// masked hides secret values and leaves an empty secret empty, so the client can tell "unset" apart.
func (e Environment) masked() Environment {
	variables := make([]Variable, len(e.Variables))
	for i, variable := range e.Variables {
		if variable.Type == TypeSecret && variable.Value != "" {
			variable.Value = Mask
		}
		variables[i] = variable
	}
	e.Variables = variables
	return e
}

// keepSecrets fills secrets submitted as empty or masked with their stored values.
func keepSecrets(submitted, stored []Variable) []Variable {
	merged := make([]Variable, len(submitted))
	for i, variable := range submitted {
		if variable.Type == TypeSecret && (variable.Value == "" || variable.Value == Mask) {
			variable.Value = storedValue(stored, variable.Name)
		}
		merged[i] = variable
	}
	return merged
}

func storedValue(stored []Variable, name string) string {
	for _, variable := range stored {
		if variable.Name == name && variable.Type == TypeSecret {
			return variable.Value
		}
	}
	return ""
}

type NewEnvironment struct {
	ProjectID string
	Name      string
	IsDefault bool
	Variables []Variable
}

// Change is a partial update; nil fields stay as they are. Variables, when set, replace the whole list.
type Change struct {
	ProjectID     string
	EnvironmentID string
	Name          *string
	IsDefault     *bool
	Position      *int
	Variables     []Variable
}

// store keeps at most one default environment per project: making one default clears the others. It
// reports ErrConflict when the name is taken in the project.
type store interface {
	Environments(ctx context.Context, projectID string) ([]Environment, error)
	Environment(ctx context.Context, projectID, id string) (Environment, error)
	CreateEnvironment(ctx context.Context, in NewEnvironment) (Environment, error)
	UpdateEnvironment(ctx context.Context, change Change) (Environment, error)
	DeleteEnvironment(ctx context.Context, projectID, id string) error
}

type access interface {
	Require(ctx context.Context, projectID, userID string, need project.Role) (project.Role, error)
}

type Service struct {
	store  store
	access access
}

func NewService(environments store, projects access) *Service {
	return &Service{store: environments, access: projects}
}

func (s *Service) List(ctx context.Context, userID, projectID string) ([]Environment, error) {
	if _, err := s.access.Require(ctx, projectID, userID, project.RoleViewer); err != nil {
		return nil, err
	}
	environments, err := s.store.Environments(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list environments: %w", err)
	}
	for i := range environments {
		environments[i] = environments[i].masked()
	}
	return environments, nil
}

type CreateInput struct {
	UserID    string
	ProjectID string
	Name      string
	IsDefault bool
	Variables []Variable
}

func (in CreateInput) Validate() error {
	if err := validateName(in.Name); err != nil {
		return err
	}
	return validateVariables(in.Variables)
}

func (s *Service) Create(ctx context.Context, in CreateInput) (Environment, error) {
	if err := in.Validate(); err != nil {
		return Environment{}, err
	}
	if _, err := s.access.Require(ctx, in.ProjectID, in.UserID, project.RoleEngineer); err != nil {
		return Environment{}, err
	}
	created, err := s.store.CreateEnvironment(ctx, NewEnvironment{
		ProjectID: in.ProjectID, Name: in.Name, IsDefault: in.IsDefault, Variables: in.Variables,
	})
	if errors.Is(err, fault.ErrConflict) {
		return Environment{}, fault.WithDetail(fault.ErrConflict, "another environment already has this name")
	}
	if err != nil {
		return Environment{}, fmt.Errorf("create environment: %w", err)
	}
	return created.masked(), nil
}

type UpdateInput struct {
	UserID string
	Change Change
}

func (in UpdateInput) Validate() error {
	if in.Change.Name != nil {
		if err := validateName(*in.Change.Name); err != nil {
			return err
		}
	}
	if in.Change.Position != nil && *in.Change.Position < 0 {
		return fault.WithDetail(fault.ErrInvalidInput, "position must not be negative")
	}
	return validateVariables(in.Change.Variables)
}

func (s *Service) Update(ctx context.Context, in UpdateInput) (Environment, error) {
	if err := in.Validate(); err != nil {
		return Environment{}, err
	}
	change := in.Change
	if _, err := s.access.Require(ctx, change.ProjectID, in.UserID, project.RoleEngineer); err != nil {
		return Environment{}, err
	}
	if change.Variables != nil {
		current, err := s.store.Environment(ctx, change.ProjectID, change.EnvironmentID)
		if err != nil {
			return Environment{}, fmt.Errorf("load environment: %w", err)
		}
		change.Variables = keepSecrets(change.Variables, current.Variables)
	}
	updated, err := s.store.UpdateEnvironment(ctx, change)
	if errors.Is(err, fault.ErrConflict) {
		return Environment{}, fault.WithDetail(fault.ErrConflict, "another environment already has this name")
	}
	if err != nil {
		return Environment{}, fmt.Errorf("update environment: %w", err)
	}
	return updated.masked(), nil
}

func (s *Service) Delete(ctx context.Context, userID, projectID, environmentID string) error {
	if _, err := s.access.Require(ctx, projectID, userID, project.RoleEngineer); err != nil {
		return err
	}
	if err := s.store.DeleteEnvironment(ctx, projectID, environmentID); err != nil {
		return fmt.Errorf("delete environment: %w", err)
	}
	return nil
}

// Resolve returns an environment with secrets in clear text, for the run that injects them. It performs
// no access check: callers resolve only environments of runs they have already authorised.
func (s *Service) Resolve(ctx context.Context, projectID, environmentID string) (Environment, error) {
	found, err := s.store.Environment(ctx, projectID, environmentID)
	if err != nil {
		return Environment{}, fmt.Errorf("resolve environment: %w", err)
	}
	return found, nil
}
