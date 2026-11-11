package test

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/worker"
)

const (
	defaultPageSize = 20
	maxPageSize     = 100
)

type NewTest struct {
	ProjectID string
	Platform  project.Platform
	Priority  Priority
	Tags      []string
	Source    Source
	Content   Content
	Change    ChangeSource
	CreatedBy string
}

// Change updates a test. Nil Priority and Tags stay as they are; a non-nil Content becomes version
// BaseVersion+1, and the store reports ErrConflict when the test is no longer at BaseVersion.
type Change struct {
	ProjectID   string
	TestID      string
	UserID      string
	Priority    *Priority
	Tags        []string
	Content     *Content
	BaseVersion int
	Source      ChangeSource
	Note        string
}

type Query struct {
	ProjectID string
	Search    string
	Tag       string
	Priority  Priority
	Platform  project.Platform
	Limit     int
	Offset    int
}

// store reports ErrNotFound for a test or version outside the project. Listings return tests without
// steps, newest first.
type store interface {
	CreateTest(ctx context.Context, in NewTest) (Test, error)
	Tests(ctx context.Context, query Query) ([]Test, int, error)
	Test(ctx context.Context, projectID, id string) (Test, error)
	UpdateTest(ctx context.Context, change Change) (Test, error)
	DeleteTest(ctx context.Context, projectID, id string) error
	Versions(ctx context.Context, projectID, testID string) ([]Version, error)
	Version(ctx context.Context, projectID, testID string, number int) (Version, error)
	// CreateGeneration stores the generation and queues its job in one transaction.
	CreateGeneration(ctx context.Context, g Generation, job worker.EnqueueInput) (Generation, error)
	Generation(ctx context.Context, projectID, id string) (Generation, error)
}

type access interface {
	Require(ctx context.Context, projectID, userID string, need project.Role) (project.Role, error)
}

type Service struct {
	store  store
	access access
}

func NewService(tests store, projects access) *Service {
	return &Service{store: tests, access: projects}
}

type CreateInput struct {
	UserID      string
	ProjectID   string
	Platform    project.Platform
	Priority    Priority
	Tags        []string
	Title       string
	Description string
	Steps       []Step
}

func (in CreateInput) Validate() error {
	if err := validatePlatform(in.Platform); err != nil {
		return err
	}
	if in.Priority != "" {
		if err := validatePriority(in.Priority); err != nil {
			return err
		}
	}
	return Content{Title: in.Title, Description: in.Description, Steps: in.Steps}.validate()
}

func (s *Service) Create(ctx context.Context, in CreateInput) (Test, error) {
	if err := in.Validate(); err != nil {
		return Test{}, err
	}
	tags, err := normalizeTags(in.Tags)
	if err != nil {
		return Test{}, err
	}
	if _, err := s.access.Require(ctx, in.ProjectID, in.UserID, project.RoleEngineer); err != nil {
		return Test{}, err
	}
	priority := in.Priority
	if priority == "" {
		priority = PriorityMedium
	}
	created, err := s.store.CreateTest(ctx, NewTest{
		ProjectID: in.ProjectID, Platform: in.Platform, Priority: priority, Tags: tags, Source: SourceManual,
		Content: Content{Title: in.Title, Description: in.Description, Steps: in.Steps}, Change: ChangeManual,
		CreatedBy: in.UserID,
	})
	if err != nil {
		return Test{}, fmt.Errorf("create test: %w", err)
	}
	return created, nil
}

type ListInput struct {
	UserID    string
	ProjectID string
	Search    string
	Tag       string
	Priority  Priority
	Platform  project.Platform
	Page      int
	Limit     int
}

func (in ListInput) Validate() error {
	if in.Priority != "" {
		if err := validatePriority(in.Priority); err != nil {
			return err
		}
	}
	if in.Platform != "" {
		if err := validatePlatform(in.Platform); err != nil {
			return err
		}
	}
	if in.Page < 0 || in.Limit < 0 || in.Limit > maxPageSize {
		return fault.WithDetail(fault.ErrInvalidInput, "page must be positive and limit at most 100")
	}
	return nil
}

type Page struct {
	Items []Test
	Total int
	Page  int
	Limit int
}

func (p Page) HasMore() bool {
	return p.Page*p.Limit < p.Total
}

// List returns a project's tests, most recently changed first, filtered by a title search, a tag, a
// priority and a platform.
func (s *Service) List(ctx context.Context, in ListInput) (Page, error) {
	if err := in.Validate(); err != nil {
		return Page{}, err
	}
	if _, err := s.access.Require(ctx, in.ProjectID, in.UserID, project.RoleViewer); err != nil {
		return Page{}, err
	}
	page, limit := max(in.Page, 1), in.Limit
	if limit == 0 {
		limit = defaultPageSize
	}
	items, total, err := s.store.Tests(ctx, Query{
		ProjectID: in.ProjectID, Search: strings.TrimSpace(in.Search), Tag: strings.ToLower(strings.TrimSpace(in.Tag)),
		Priority: in.Priority, Platform: in.Platform, Limit: limit, Offset: (page - 1) * limit,
	})
	if err != nil {
		return Page{}, fmt.Errorf("list tests: %w", err)
	}
	return Page{Items: items, Total: total, Page: page, Limit: limit}, nil
}

type Ref struct {
	UserID    string
	ProjectID string
	TestID    string
}

func (s *Service) Get(ctx context.Context, ref Ref) (Test, error) {
	if _, err := s.access.Require(ctx, ref.ProjectID, ref.UserID, project.RoleViewer); err != nil {
		return Test{}, err
	}
	return s.find(ctx, ref.ProjectID, ref.TestID)
}

func (s *Service) find(ctx context.Context, projectID, id string) (Test, error) {
	found, err := s.store.Test(ctx, projectID, id)
	if errors.Is(err, fault.ErrNotFound) {
		return Test{}, fault.WithDetail(fault.ErrNotFound, "test not found")
	}
	if err != nil {
		return Test{}, fmt.Errorf("load test: %w", err)
	}
	return found, nil
}

// UpdateInput changes a test partly; nil fields stay as they are. Changing the title, description or
// steps creates a version and needs BaseVersion, the version the change was made against.
type UpdateInput struct {
	Ref         Ref
	BaseVersion int
	Title       *string
	Description *string
	Steps       []Step
	Priority    *Priority
	Tags        []string
	Note        string
}

func (in UpdateInput) changesContent() bool {
	return in.Title != nil || in.Description != nil || in.Steps != nil
}

func (in UpdateInput) apply(content Content) Content {
	if in.Title != nil {
		content.Title = *in.Title
	}
	if in.Description != nil {
		content.Description = *in.Description
	}
	if in.Steps != nil {
		content.Steps = in.Steps
	}
	return content
}

func (in UpdateInput) Validate() error {
	if in.Priority != nil {
		if err := validatePriority(*in.Priority); err != nil {
			return err
		}
	}
	if in.changesContent() && in.BaseVersion < 1 {
		return fault.WithDetail(fault.ErrInvalidInput, "changing the title, description or steps needs base_version")
	}
	return validateNote(in.Note)
}

func (s *Service) Update(ctx context.Context, in UpdateInput) (Test, error) {
	if err := in.Validate(); err != nil {
		return Test{}, err
	}
	change := Change{
		ProjectID: in.Ref.ProjectID, TestID: in.Ref.TestID, UserID: in.Ref.UserID, Priority: in.Priority,
		Source: ChangeManual, Note: in.Note,
	}
	if in.Tags != nil {
		tags, err := normalizeTags(in.Tags)
		if err != nil {
			return Test{}, err
		}
		change.Tags = tags
	}
	if _, err := s.access.Require(ctx, in.Ref.ProjectID, in.Ref.UserID, project.RoleEngineer); err != nil {
		return Test{}, err
	}
	if in.changesContent() {
		current, err := s.find(ctx, in.Ref.ProjectID, in.Ref.TestID)
		if err != nil {
			return Test{}, err
		}
		if current.Version != in.BaseVersion {
			return Test{}, fault.WithDetail(fault.ErrConflict, "the test has changed since base_version; reload it")
		}
		content := in.apply(current.Content)
		if err := content.validate(); err != nil {
			return Test{}, err
		}
		change.Content, change.BaseVersion = &content, in.BaseVersion
	}
	return s.save(ctx, change)
}

func (s *Service) save(ctx context.Context, change Change) (Test, error) {
	updated, err := s.store.UpdateTest(ctx, change)
	switch {
	case errors.Is(err, fault.ErrConflict):
		return Test{}, fault.WithDetail(fault.ErrConflict, "the test has changed since base_version; reload it")
	case errors.Is(err, fault.ErrNotFound):
		return Test{}, fault.WithDetail(fault.ErrNotFound, "test not found")
	case err != nil:
		return Test{}, fmt.Errorf("update test: %w", err)
	}
	return updated, nil
}

func (s *Service) Delete(ctx context.Context, ref Ref) error {
	if _, err := s.access.Require(ctx, ref.ProjectID, ref.UserID, project.RoleEngineer); err != nil {
		return err
	}
	err := s.store.DeleteTest(ctx, ref.ProjectID, ref.TestID)
	if errors.Is(err, fault.ErrNotFound) {
		return fault.WithDetail(fault.ErrNotFound, "test not found")
	}
	if err != nil {
		return fmt.Errorf("delete test: %w", err)
	}
	return nil
}

// Duplicate copies a test's current content and metadata into a new test with its own history.
func (s *Service) Duplicate(ctx context.Context, ref Ref) (Test, error) {
	if _, err := s.access.Require(ctx, ref.ProjectID, ref.UserID, project.RoleEngineer); err != nil {
		return Test{}, err
	}
	original, err := s.find(ctx, ref.ProjectID, ref.TestID)
	if err != nil {
		return Test{}, err
	}
	content := original.Content
	content.Title = copyTitle(content.Title)
	duplicate, err := s.store.CreateTest(ctx, NewTest{
		ProjectID: ref.ProjectID, Platform: original.Platform, Priority: original.Priority, Tags: original.Tags,
		Source: SourceDuplicate, Content: content, Change: ChangeManual, CreatedBy: ref.UserID,
	})
	if err != nil {
		return Test{}, fmt.Errorf("duplicate test: %w", err)
	}
	return duplicate, nil
}

// Versions lists a test's versions, newest first, without their steps.
func (s *Service) Versions(ctx context.Context, ref Ref) ([]Version, error) {
	if _, err := s.access.Require(ctx, ref.ProjectID, ref.UserID, project.RoleViewer); err != nil {
		return nil, err
	}
	versions, err := s.store.Versions(ctx, ref.ProjectID, ref.TestID)
	if errors.Is(err, fault.ErrNotFound) {
		return nil, fault.WithDetail(fault.ErrNotFound, "test not found")
	}
	if err != nil {
		return nil, fmt.Errorf("list test versions: %w", err)
	}
	return versions, nil
}

func (s *Service) Version(ctx context.Context, ref Ref, number int) (Version, error) {
	if _, err := s.access.Require(ctx, ref.ProjectID, ref.UserID, project.RoleViewer); err != nil {
		return Version{}, err
	}
	return s.version(ctx, ref, number)
}

func (s *Service) version(ctx context.Context, ref Ref, number int) (Version, error) {
	found, err := s.store.Version(ctx, ref.ProjectID, ref.TestID, number)
	if errors.Is(err, fault.ErrNotFound) {
		return Version{}, fault.WithDetail(fault.ErrNotFound, "version not found")
	}
	if err != nil {
		return Version{}, fmt.Errorf("load test version: %w", err)
	}
	return found, nil
}

// Restore makes an earlier version's content current again by recording it as a new version, so the
// versions after it stay in the history.
func (s *Service) Restore(ctx context.Context, ref Ref, number int) (Test, error) {
	if _, err := s.access.Require(ctx, ref.ProjectID, ref.UserID, project.RoleEngineer); err != nil {
		return Test{}, err
	}
	current, err := s.find(ctx, ref.ProjectID, ref.TestID)
	if err != nil {
		return Test{}, err
	}
	if number == current.Version {
		return Test{}, fault.WithDetail(fault.ErrInvalidInput, "this version is already current")
	}
	restored, err := s.version(ctx, ref, number)
	if err != nil {
		return Test{}, err
	}
	return s.save(ctx, Change{
		ProjectID: ref.ProjectID, TestID: ref.TestID, UserID: ref.UserID, Content: &restored.Content,
		BaseVersion: current.Version, Source: ChangeRestore, Note: "restored version " + strconv.Itoa(number),
	})
}
