package project

import (
	"context"
	"fmt"
	"strings"

	"github.com/mobqa/backend/internal/fault"
)

const (
	defaultPageSize = 20
	maxPageSize     = 100
	maxSearchLength = 100
)

type NewProject struct {
	OwnerID     string
	Name        string
	Description string
	Platforms   []Platform
	PackageName string
	Appearance  Appearance
}

type Query struct {
	UserID string
	Search string
	Limit  int
	Offset int
}

// Change is a partial update: nil fields are left as they are.
type Change struct {
	ProjectID   string
	Name        *string
	Description *string
	Platforms   []Platform
	PackageName *string
	Appearance  *Appearance
}

// NewMember names the account by email; the store resolves it and reports ErrNotFound when no account
// has that email and ErrConflict when it already belongs to the project.
type NewMember struct {
	ProjectID string
	Email     string
	Role      Role
}

type store interface {
	roleStore
	CreateProject(ctx context.Context, in NewProject) (Project, error)
	Projects(ctx context.Context, query Query) ([]Project, int, error)
	Project(ctx context.Context, id string) (Project, error)
	UpdateProject(ctx context.Context, change Change) (Project, error)
	ArchiveProject(ctx context.Context, id string) error
	SetAuthProfile(ctx context.Context, projectID string, profile AuthProfile) error
	Members(ctx context.Context, projectID string) ([]Member, error)
	AddMember(ctx context.Context, in NewMember) (Member, error)
	SetMemberRole(ctx context.Context, projectID, userID string, role Role) error
	RemoveMember(ctx context.Context, projectID, userID string) error
}

type Service struct {
	store  store
	access *Access
}

func NewService(projects store) *Service {
	return &Service{store: projects, access: NewAccess(projects)}
}

type CreateInput struct {
	UserID      string
	Name        string
	Description string
	Platforms   []Platform
	PackageName string
}

func (in CreateInput) Validate() error {
	if err := validateName(in.Name); err != nil {
		return err
	}
	if err := validateDescription(in.Description); err != nil {
		return err
	}
	if err := validatePlatforms(in.Platforms); err != nil {
		return err
	}
	return validatePackageName(in.PackageName)
}

// Create makes the user the owner of a new project.
func (s *Service) Create(ctx context.Context, in CreateInput) (Project, error) {
	if err := in.Validate(); err != nil {
		return Project{}, err
	}
	created, err := s.store.CreateProject(ctx, NewProject{
		OwnerID:     in.UserID,
		Name:        in.Name,
		Description: in.Description,
		Platforms:   in.Platforms,
		PackageName: in.PackageName,
		Appearance:  defaultAppearance(in.Name),
	})
	if err != nil {
		return Project{}, fmt.Errorf("create project: %w", err)
	}
	return created, nil
}

type ListInput struct {
	UserID string
	Search string
	Page   int
	Limit  int
}

func (in ListInput) Validate() error {
	if in.Page < 0 || in.Limit < 0 || in.Limit > maxPageSize {
		return fault.WithDetail(fault.ErrInvalidInput, "page must be positive and limit at most 100")
	}
	if len(in.Search) > maxSearchLength {
		return fault.WithDetail(fault.ErrInvalidInput, "search must be at most 100 characters")
	}
	return nil
}

type Page struct {
	Items []Project
	Total int
	Page  int
	Limit int
}

func (p Page) HasMore() bool {
	return p.Page*p.Limit < p.Total
}

// List returns the projects the user belongs to, newest first. Zero page and limit mean the defaults.
func (s *Service) List(ctx context.Context, in ListInput) (Page, error) {
	if err := in.Validate(); err != nil {
		return Page{}, err
	}
	page, limit := max(in.Page, 1), in.Limit
	if limit == 0 {
		limit = defaultPageSize
	}
	items, total, err := s.store.Projects(ctx, Query{
		UserID: in.UserID, Search: strings.TrimSpace(in.Search), Limit: limit, Offset: (page - 1) * limit,
	})
	if err != nil {
		return Page{}, fmt.Errorf("list projects: %w", err)
	}
	return Page{Items: items, Total: total, Page: page, Limit: limit}, nil
}

func (s *Service) Get(ctx context.Context, userID, projectID string) (Detail, error) {
	role, err := s.access.Require(ctx, projectID, userID, RoleViewer)
	if err != nil {
		return Detail{}, err
	}
	found, err := s.store.Project(ctx, projectID)
	if err != nil {
		return Detail{}, fmt.Errorf("load project: %w", err)
	}
	members, err := s.store.Members(ctx, projectID)
	if err != nil {
		return Detail{}, fmt.Errorf("load members: %w", err)
	}
	return Detail{Project: found, Role: role, Members: members}, nil
}

type UpdateInput struct {
	UserID string
	Change Change
}

func (in UpdateInput) Validate() error {
	c := in.Change
	if c.Name != nil {
		if err := validateName(*c.Name); err != nil {
			return err
		}
	}
	if c.Description != nil {
		if err := validateDescription(*c.Description); err != nil {
			return err
		}
	}
	if c.Platforms != nil {
		if err := validatePlatforms(c.Platforms); err != nil {
			return err
		}
	}
	if c.PackageName != nil {
		if err := validatePackageName(*c.PackageName); err != nil {
			return err
		}
	}
	if c.Appearance != nil {
		return c.Appearance.Validate()
	}
	return nil
}

// Update changes project settings; it needs an admin.
func (s *Service) Update(ctx context.Context, in UpdateInput) (Project, error) {
	if err := in.Validate(); err != nil {
		return Project{}, err
	}
	if _, err := s.access.Require(ctx, in.Change.ProjectID, in.UserID, RoleAdmin); err != nil {
		return Project{}, err
	}
	updated, err := s.store.UpdateProject(ctx, in.Change)
	if err != nil {
		return Project{}, fmt.Errorf("update project: %w", err)
	}
	return updated, nil
}

// Archive hides a project from everyone; only its owner may do it.
func (s *Service) Archive(ctx context.Context, userID, projectID string) error {
	if _, err := s.access.Require(ctx, projectID, userID, RoleOwner); err != nil {
		return err
	}
	if err := s.store.ArchiveProject(ctx, projectID); err != nil {
		return fmt.Errorf("archive project: %w", err)
	}
	return nil
}

func (s *Service) AuthProfile(ctx context.Context, userID, projectID string) (AuthProfile, error) {
	if _, err := s.access.Require(ctx, projectID, userID, RoleViewer); err != nil {
		return AuthProfile{}, err
	}
	found, err := s.store.Project(ctx, projectID)
	if err != nil {
		return AuthProfile{}, fmt.Errorf("load project: %w", err)
	}
	return found.AuthProfile, nil
}

type SetAuthProfileInput struct {
	UserID    string
	ProjectID string
	Profile   AuthProfile
}

func (in SetAuthProfileInput) Validate() error {
	return in.Profile.Validate()
}

// SetAuthProfile replaces the login recipe; engineers maintain it alongside the tests that use it.
func (s *Service) SetAuthProfile(ctx context.Context, in SetAuthProfileInput) (AuthProfile, error) {
	if err := in.Validate(); err != nil {
		return AuthProfile{}, err
	}
	if _, err := s.access.Require(ctx, in.ProjectID, in.UserID, RoleEngineer); err != nil {
		return AuthProfile{}, err
	}
	if err := s.store.SetAuthProfile(ctx, in.ProjectID, in.Profile); err != nil {
		return AuthProfile{}, fmt.Errorf("store auth profile: %w", err)
	}
	return in.Profile, nil
}
