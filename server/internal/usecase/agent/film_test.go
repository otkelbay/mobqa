package agent_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mobqa/backend/internal/usecase/agent"
	"github.com/mobqa/backend/internal/usecase/run"
)

// camera films into files under dir, or reports that the device is not streaming.
type camera struct {
	dir       string
	streaming bool
	discarded bool
}

func (c *camera) Record(string) (agent.Recording, error) {
	if !c.streaming {
		return nil, agent.ErrNotStreaming
	}
	return c, nil
}

func (c *camera) Stop(context.Context) (agent.Video, error) {
	path := filepath.Join(c.dir, "attempt.mp4")
	if err := os.WriteFile(path, []byte("mp4"), 0o600); err != nil {
		return agent.Video{}, err
	}
	return agent.Video{Path: path, Duration: 3 * time.Second}, nil
}

func (c *camera) Discard() {
	c.discarded = true
}

type uploads struct {
	received map[string]string
	err      error
}

func (u *uploads) Upload(_ context.Context, executionID, path string) error {
	if u.err != nil {
		return u.err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	u.received[executionID] = string(content)
	return nil
}

type passingRunner struct {
	err error
}

func (r passingRunner) Run(context.Context, run.Execution) (run.Outcome, error) {
	return run.Outcome{Status: run.AttemptPassed}, r.err
}

func TestFilmKeepsAVideoOfEachAttemptOnAStreamingDevice(t *testing.T) {
	lens := &camera{dir: t.TempDir(), streaming: true}
	api := &uploads{received: map[string]string{}}

	outcome, err := agent.NewFilm(passingRunner{}, lens, api).Run(context.Background(),
		run.Execution{ID: "result-1/1", Serial: "00008"})

	if err != nil || outcome.Status != run.AttemptPassed || outcome.Video == nil ||
		*outcome.Video != (run.Video{Duration: 3 * time.Second}) || api.received["result-1/1"] != "mp4" {
		t.Fatalf("Run = %+v, %v; uploads %v", outcome, err, api.received)
	}
	if _, err := os.Stat(filepath.Join(lens.dir, "attempt.mp4")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the video was left on the hub: %v", err)
	}
}

func TestFilmSaysWhyAVideoIsMissing(t *testing.T) {
	lens := &camera{dir: t.TempDir(), streaming: true}
	api := &uploads{err: errors.New("the API is unreachable")}

	outcome, err := agent.NewFilm(passingRunner{}, lens, api).Run(context.Background(), run.Execution{ID: "r/1"})

	if err != nil || outcome.Status != run.AttemptPassed || outcome.Video == nil ||
		outcome.Video.Problem != "the video could not be uploaded: the API is unreachable" {
		t.Fatalf("Run = %+v, %v", outcome, err)
	}
}

func TestFilmSkipsDevicesThatDoNotStream(t *testing.T) {
	outcome, err := agent.NewFilm(passingRunner{}, &camera{}, &uploads{}).Run(context.Background(),
		run.Execution{ID: "r/1"})

	if err != nil || outcome.Video != nil {
		t.Fatalf("Run = %+v, %v", outcome, err)
	}
}

func TestFilmDiscardsTheVideoOfAnExecutionThatBroke(t *testing.T) {
	lens := &camera{streaming: true}

	_, err := agent.NewFilm(passingRunner{err: context.Canceled}, lens, &uploads{}).Run(context.Background(),
		run.Execution{ID: "r/1"})

	if !errors.Is(err, context.Canceled) || !lens.discarded {
		t.Fatalf("Run error = %v, discarded %v", err, lens.discarded)
	}
}
