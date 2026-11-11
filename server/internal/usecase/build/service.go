package build

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"

	"github.com/mobqa/backend/internal/clock"
	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/worker"
)

const (
	defaultPageSize = 20
	maxPageSize     = 100
	// analysisAttempts retries a change analysis that failed on a transient storage error.
	analysisAttempts = 3
)

// AnalysisJobKind is the job that compares a new build with its predecessor.
const AnalysisJobKind = "build_analysis"

// File is an opened package: random access for zip reading, seeking for ranged downloads.
type File interface {
	io.ReadSeekCloser
	io.ReaderAt
}

// blobs stores package bytes under keys the service chooses. Open and Delete report ErrNotFound for a
// key that holds nothing.
type blobs interface {
	Put(ctx context.Context, key string, content io.Reader) (int64, error)
	Open(ctx context.Context, key string) (File, error)
	Delete(ctx context.Context, key string) error
}

type Query struct {
	ProjectID string
	Platform  project.Platform
	Limit     int
	Offset    int
}

type store interface {
	// CreateBuild stores the build and queues its analysis in one transaction, so no build is left
	// without one.
	CreateBuild(ctx context.Context, b Build, analysis worker.EnqueueInput) (Build, error)
	Builds(ctx context.Context, query Query) ([]Build, int, error)
	Build(ctx context.Context, projectID, id string) (Build, error)
	// PreviousBuild is the latest build of the same project and platform uploaded before b.
	PreviousBuild(ctx context.Context, b Build) (Build, error)
	SetAnalysis(ctx context.Context, buildID string, analysis Analysis) error
	// SetCurrent makes the build current and clears the flag from other builds of its platform.
	SetCurrent(ctx context.Context, projectID, id string) (Build, error)
	DeleteBuild(ctx context.Context, projectID, id string) (Build, error)
}

type access interface {
	Require(ctx context.Context, projectID, userID string, need project.Role) (project.Role, error)
}

type Config struct {
	MaxSize int64
}

type Dependencies struct {
	Store  store
	Blobs  blobs
	Access access
	Clock  clock.Clock
}

type Service struct {
	cfg    Config
	store  store
	blobs  blobs
	access access
	clock  clock.Clock
}

func NewService(cfg Config, deps Dependencies) *Service {
	return &Service{cfg: cfg, store: deps.Store, blobs: deps.Blobs, access: deps.Access, clock: deps.Clock}
}

type UploadInput struct {
	UserID      string
	ProjectID   string
	Platform    project.Platform
	Version     string
	BuildNumber string
	FileName    string
	Content     io.Reader
}

func (in UploadInput) Validate() error {
	if err := validatePlatform(in.Platform); err != nil {
		return err
	}
	return validateVersion(in.Version, in.BuildNumber)
}

type analysisPayload struct {
	ProjectID string `json:"project_id"`
	BuildID   string `json:"build_id"`
}

// Upload streams the package into blob storage while hashing it, then checks it before recording the
// build. The build's id is chosen here because its blob key and analysis job need it before the row
// exists.
func (s *Service) Upload(ctx context.Context, in UploadInput) (Build, error) {
	if err := in.Validate(); err != nil {
		return Build{}, err
	}
	if _, err := s.access.Require(ctx, in.ProjectID, in.UserID, project.RoleEngineer); err != nil {
		return Build{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Build{}, fmt.Errorf("generate build id: %w", err)
	}
	b := Build{
		ID: id.String(), ProjectID: in.ProjectID, Platform: in.Platform, Version: in.Version,
		BuildNumber: in.BuildNumber, FileName: downloadName(in.FileName, in.Version, in.Platform),
		BlobKey: "builds/" + in.ProjectID + "/" + id.String(), UploadedBy: in.UserID, Status: StatusProcessing,
	}
	if b.BuildNumber == "" {
		b.BuildNumber = b.Version
	}
	if err := s.save(ctx, &b, in.Content); err != nil {
		return Build{}, s.discard(ctx, b.BlobKey, err)
	}
	payload, err := json.Marshal(analysisPayload{ProjectID: b.ProjectID, BuildID: b.ID})
	if err != nil {
		return Build{}, s.discard(ctx, b.BlobKey, fmt.Errorf("encode analysis payload: %w", err))
	}
	created, err := s.store.CreateBuild(ctx, b,
		worker.EnqueueInput{Kind: AnalysisJobKind, Payload: payload, MaxAttempts: analysisAttempts})
	if err != nil {
		return Build{}, s.discard(ctx, b.BlobKey, fmt.Errorf("create build: %w", err))
	}
	return created, nil
}

// save writes the package, fills in its size, hash and signing profile, and rejects packages that are
// too large or not what their platform says.
func (s *Service) save(ctx context.Context, b *Build, content io.Reader) error {
	hash := sha256.New()
	limited := &io.LimitedReader{R: io.TeeReader(content, hash), N: s.cfg.MaxSize + 1}
	size, err := s.blobs.Put(ctx, b.BlobKey, limited)
	if err != nil {
		return fmt.Errorf("store package: %w", err)
	}
	if size > s.cfg.MaxSize {
		return fault.WithDetail(fault.ErrInvalidInput, "the file exceeds the build size limit")
	}
	b.Size, b.SHA256 = size, hex.EncodeToString(hash.Sum(nil))
	file, err := s.blobs.Open(ctx, b.BlobKey)
	if err != nil {
		return fmt.Errorf("reopen package: %w", err)
	}
	defer func() { _ = file.Close() }()
	archive, err := zip.NewReader(file, size)
	if err != nil {
		return fault.WithDetail(fault.ErrInvalidInput, "the file is not a zip-based APK or IPA")
	}
	b.Provisioning, err = inspect(archive, b.Platform)
	return err
}

// discard removes a package whose build will not be recorded and returns the cause.
func (s *Service) discard(ctx context.Context, key string, cause error) error {
	if err := s.blobs.Delete(context.WithoutCancel(ctx), key); err != nil && !errors.Is(err, fault.ErrNotFound) {
		return errors.Join(cause, fmt.Errorf("discard package: %w", err))
	}
	return cause
}

type ListInput struct {
	UserID    string
	ProjectID string
	Platform  project.Platform
	Page      int
	Limit     int
}

func (in ListInput) Validate() error {
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
	Items []Build
	Total int
	Page  int
	Limit int
}

func (p Page) HasMore() bool {
	return p.Page*p.Limit < p.Total
}

// List returns a project's builds, newest first, optionally for one platform.
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
	items, total, err := s.store.Builds(ctx, Query{
		ProjectID: in.ProjectID, Platform: in.Platform, Limit: limit, Offset: (page - 1) * limit,
	})
	if err != nil {
		return Page{}, fmt.Errorf("list builds: %w", err)
	}
	return Page{Items: items, Total: total, Page: page, Limit: limit}, nil
}

type Ref struct {
	UserID    string
	ProjectID string
	BuildID   string
}

func (s *Service) Get(ctx context.Context, ref Ref) (Build, error) {
	if _, err := s.access.Require(ctx, ref.ProjectID, ref.UserID, project.RoleViewer); err != nil {
		return Build{}, err
	}
	return s.find(ctx, ref.ProjectID, ref.BuildID)
}

func (s *Service) find(ctx context.Context, projectID, id string) (Build, error) {
	found, err := s.store.Build(ctx, projectID, id)
	if errors.Is(err, fault.ErrNotFound) {
		return Build{}, fault.WithDetail(fault.ErrNotFound, "build not found")
	}
	if err != nil {
		return Build{}, fmt.Errorf("load build: %w", err)
	}
	return found, nil
}

// Download opens a build's package; the caller closes the file.
func (s *Service) Download(ctx context.Context, ref Ref) (Build, File, error) {
	found, err := s.Get(ctx, ref)
	if err != nil {
		return Build{}, nil, err
	}
	file, err := s.blobs.Open(ctx, found.BlobKey)
	if err != nil {
		return Build{}, nil, fmt.Errorf("open package: %w", err)
	}
	return found, file, nil
}

// DownloadForHub opens a build's package for a hub of the build's project; the caller closes the file.
func (s *Service) DownloadForHub(ctx context.Context, projectID, buildID string) (Build, File, error) {
	found, err := s.find(ctx, projectID, buildID)
	if err != nil {
		return Build{}, nil, err
	}
	file, err := s.blobs.Open(ctx, found.BlobKey)
	if err != nil {
		return Build{}, nil, fmt.Errorf("open package: %w", err)
	}
	return found, file, nil
}

// SetCurrent makes the build the one runs install on its platform. An iOS build whose profile has
// expired cannot be installed, so it cannot become current.
func (s *Service) SetCurrent(ctx context.Context, ref Ref) (Build, error) {
	if _, err := s.access.Require(ctx, ref.ProjectID, ref.UserID, project.RoleEngineer); err != nil {
		return Build{}, err
	}
	found, err := s.find(ctx, ref.ProjectID, ref.BuildID)
	if err != nil {
		return Build{}, err
	}
	if found.Provisioning.expired(s.clock.Now()) {
		return Build{}, fault.WithDetail(fault.ErrConflict, "the build's provisioning profile has expired")
	}
	current, err := s.store.SetCurrent(ctx, ref.ProjectID, ref.BuildID)
	if err != nil {
		return Build{}, fmt.Errorf("set current build: %w", err)
	}
	return current, nil
}

// Delete removes the build and then its package. The row goes first: a package left behind by a failed
// blob delete costs only space, while a row without its package would break installs.
func (s *Service) Delete(ctx context.Context, ref Ref) error {
	if _, err := s.access.Require(ctx, ref.ProjectID, ref.UserID, project.RoleEngineer); err != nil {
		return err
	}
	deleted, err := s.store.DeleteBuild(ctx, ref.ProjectID, ref.BuildID)
	if errors.Is(err, fault.ErrNotFound) {
		return fault.WithDetail(fault.ErrNotFound, "build not found")
	}
	if err != nil {
		return fmt.Errorf("delete build: %w", err)
	}
	if err := s.blobs.Delete(ctx, deleted.BlobKey); err != nil && !errors.Is(err, fault.ErrNotFound) {
		return fmt.Errorf("delete package: %w", err)
	}
	return nil
}
