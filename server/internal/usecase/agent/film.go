package agent

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/mobqa/backend/internal/usecase/run"
)

// videoTimeout bounds wrapping one attempt's video and sending it to the API.
const videoTimeout = 2 * time.Minute

// ErrNotStreaming is a device whose screen is not being streamed, so there is nothing to film.
var ErrNotStreaming = errors.New("the device is not streaming its screen")

// Recorder films the screens of streaming devices.
type Recorder interface {
	Record(serial string) (Recording, error)
}

// Recording is a film in progress. Stop ends it and returns the file it wrote, which the caller removes;
// Discard ends it and keeps nothing.
type Recording interface {
	Stop(ctx context.Context) (Video, error)
	Discard()
}

type Video struct {
	Path     string
	Duration time.Duration
}

// Uploader sends an execution's video to the API.
type Uploader interface {
	Upload(ctx context.Context, executionID, path string) error
}

// Film runs executions with another runner and films every one whose device is streaming its screen. A
// video that cannot be kept never changes the attempt's outcome; the outcome says why it is missing.
type Film struct {
	runner   Runner
	recorder Recorder
	uploader Uploader
}

func NewFilm(runner Runner, recorder Recorder, uploader Uploader) *Film {
	return &Film{runner: runner, recorder: recorder, uploader: uploader}
}

func (f *Film) Run(ctx context.Context, execution run.Execution) (run.Outcome, error) {
	recording, err := f.recorder.Record(execution.Serial)
	if errors.Is(err, ErrNotStreaming) {
		return f.runner.Run(ctx, execution)
	}
	outcome, runErr := f.runner.Run(ctx, execution)
	switch {
	case err != nil:
		outcome.Video = &run.Video{Problem: "the attempt could not be filmed: " + err.Error()}
	case runErr != nil:
		recording.Discard()
	default:
		outcome.Video = f.keep(ctx, execution.ID, recording)
	}
	return outcome, runErr
}

func (f *Film) keep(ctx context.Context, executionID string, recording Recording) *run.Video {
	ctx, cancel := context.WithTimeout(ctx, videoTimeout)
	defer cancel()
	video, err := recording.Stop(ctx)
	if err != nil {
		return &run.Video{Problem: "the attempt could not be filmed: " + err.Error()}
	}
	defer func() { _ = os.Remove(video.Path) }()
	if err := f.uploader.Upload(ctx, executionID, video.Path); err != nil {
		return &run.Video{Problem: "the video could not be uploaded: " + err.Error()}
	}
	return &run.Video{Duration: video.Duration}
}
