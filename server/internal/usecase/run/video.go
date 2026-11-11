package run

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/build"
	"github.com/mobqa/backend/internal/usecase/project"
)

// MaxVideoBytes bounds one attempt's video; the phone's stream of a 30-minute attempt is a few hundred MB.
const MaxVideoBytes = 1 << 30

// videoBlobs stores videos under keys of the result and the attempt. Open and Delete report ErrNotFound
// for a key that holds nothing.
type videoBlobs interface {
	Put(ctx context.Context, key string, content io.Reader) (int64, error)
	Open(ctx context.Context, key string) (build.File, error)
	Delete(ctx context.Context, key string) error
}

type videoStore interface {
	// RunningResult returns a running result of the hub's devices; any other reports ErrNotFound.
	RunningResult(ctx context.Context, hubID, resultID string) (Result, error)
	Run(ctx context.Context, projectID, id string) (Run, error)
	Attempts(ctx context.Context, runID, resultID string) ([]Attempt, error)
}

type VideoDependencies struct {
	Store  videoStore
	Blobs  videoBlobs
	Access access
}

// Videos keeps what hubs filmed of the attempts on their devices and shows it to the project's people.
type Videos struct {
	deps VideoDependencies
}

func NewVideos(deps VideoDependencies) *Videos {
	return &Videos{deps: deps}
}

type SaveVideoInput struct {
	HubID    string
	ResultID string
	Number   int
	Content  io.Reader
}

// Save stores the video of the attempt a hub is finishing. The hub sends it before the attempt's
// outcome, so the result must still be running that attempt.
func (v *Videos) Save(ctx context.Context, in SaveVideoInput) error {
	if in.Number < 1 {
		return fault.WithDetail(fault.ErrInvalidInput, "attempts are numbered from 1")
	}
	result, err := v.deps.Store.RunningResult(ctx, in.HubID, in.ResultID)
	if errors.Is(err, fault.ErrNotFound) {
		return fault.WithDetail(fault.ErrNotFound, "no running result of this hub")
	}
	if err != nil {
		return fmt.Errorf("load result: %w", err)
	}
	if result.Attempts != in.Number-1 {
		return fault.WithDetail(fault.ErrConflict, "the result is not running this attempt")
	}
	key := videoKey(in.ResultID, in.Number)
	size, err := v.deps.Blobs.Put(ctx, key, &io.LimitedReader{R: in.Content, N: MaxVideoBytes + 1})
	if err != nil {
		return fmt.Errorf("store video: %w", err)
	}
	if size > MaxVideoBytes {
		cause := fault.WithDetail(fault.ErrInvalidInput, "the video exceeds the size limit")
		if err := v.deps.Blobs.Delete(context.WithoutCancel(ctx), key); err != nil {
			return errors.Join(cause, fmt.Errorf("discard video: %w", err))
		}
		return cause
	}
	return nil
}

type VideoRef struct {
	Ref
	ResultID string
	Number   int
}

// Open returns the video of an attempt the hub filmed; the caller closes the file.
func (v *Videos) Open(ctx context.Context, ref VideoRef) (build.File, error) {
	if _, err := v.deps.Access.Require(ctx, ref.ProjectID, ref.UserID, project.RoleViewer); err != nil {
		return nil, err
	}
	if _, err := v.deps.Store.Run(ctx, ref.ProjectID, ref.RunID); err != nil {
		if errors.Is(err, fault.ErrNotFound) {
			return nil, fault.WithDetail(fault.ErrNotFound, "run not found")
		}
		return nil, fmt.Errorf("load run: %w", err)
	}
	attempts, err := v.deps.Store.Attempts(ctx, ref.RunID, ref.ResultID)
	if err != nil && !errors.Is(err, fault.ErrNotFound) {
		return nil, fmt.Errorf("load attempts: %w", err)
	}
	for _, a := range attempts {
		if a.Number == ref.Number && a.Video != nil && a.Video.Problem == "" {
			file, err := v.deps.Blobs.Open(ctx, videoKey(ref.ResultID, ref.Number))
			if errors.Is(err, fault.ErrNotFound) {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("open video: %w", err)
			}
			return file, nil
		}
	}
	return nil, fault.WithDetail(fault.ErrNotFound, "video not found")
}

func videoKey(resultID string, number int) string {
	return "videos/" + resultID + "/" + strconv.Itoa(number)
}
