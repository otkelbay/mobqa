package run_test

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/build"
	"github.com/mobqa/backend/internal/usecase/run"
)

type videoStore struct {
	*fakeStore
}

func (videoStore) RunningResult(_ context.Context, hubID, resultID string) (run.Result, error) {
	if hubID != "hub-1" || resultID != "result-1" {
		return run.Result{}, fault.ErrNotFound
	}
	return run.Result{ID: resultID, Status: run.ResultRunning, Attempts: 1}, nil
}

func (videoStore) Attempts(_ context.Context, _, resultID string) ([]run.Attempt, error) {
	if resultID != "result-1" {
		return nil, fault.ErrNotFound
	}
	return []run.Attempt{
		{Number: 1, Video: &run.Video{Problem: "the attempt could not be filmed: no frames"}},
		{Number: 2, Video: &run.Video{Duration: 9 * time.Second}},
		{Number: 3},
	}, nil
}

type file struct {
	*bytes.Reader
}

func (file) Close() error { return nil }

type memoryBlobs map[string][]byte

func (b memoryBlobs) Put(_ context.Context, key string, content io.Reader) (int64, error) {
	data, err := io.ReadAll(content)
	b[key] = data
	return int64(len(data)), err
}

func (b memoryBlobs) Open(_ context.Context, key string) (build.File, error) {
	data, ok := b[key]
	if !ok {
		return nil, fault.ErrNotFound
	}
	return file{bytes.NewReader(data)}, nil
}

func (b memoryBlobs) Delete(_ context.Context, key string) error {
	delete(b, key)
	return nil
}

func newVideos(blobs memoryBlobs) *run.Videos {
	return run.NewVideos(run.VideoDependencies{Store: videoStore{newStore()}, Blobs: blobs, Access: fakeAccess{}})
}

func TestHubsSaveTheVideoOfTheAttemptTheyAreFinishing(t *testing.T) {
	blobs := memoryBlobs{}
	videos := newVideos(blobs)

	err := videos.Save(context.Background(), run.SaveVideoInput{
		HubID: "hub-1", ResultID: "result-1", Number: 2, Content: strings.NewReader("ftyp"),
	})

	if err != nil || string(blobs["videos/result-1/2"]) != "ftyp" {
		t.Fatalf("Save = %v, blobs %v", err, blobs)
	}
}

func TestSaveVideoRejects(t *testing.T) {
	tests := []struct {
		name   string
		in     run.SaveVideoInput
		kind   error
		detail string
	}{
		{name: "attempt zero", in: run.SaveVideoInput{HubID: "hub-1", ResultID: "result-1"},
			kind: fault.ErrInvalidInput, detail: "attempts are numbered from 1"},
		{name: "another hub's result", in: run.SaveVideoInput{HubID: "hub-2", ResultID: "result-1", Number: 2},
			kind: fault.ErrNotFound, detail: "no running result of this hub"},
		{name: "a finished attempt", in: run.SaveVideoInput{HubID: "hub-1", ResultID: "result-1", Number: 1},
			kind: fault.ErrConflict, detail: "the result is not running this attempt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blobs := memoryBlobs{}
			tt.in.Content = strings.NewReader("ftyp")

			err := newVideos(blobs).Save(context.Background(), tt.in)

			requireKind(t, err, tt.kind, tt.detail)
			if len(blobs) != 0 {
				t.Fatalf("stored %v", blobs)
			}
		})
	}
}

func TestPeopleOpenTheVideosOfFilmedAttempts(t *testing.T) {
	blobs := memoryBlobs{"videos/result-1/1": []byte("x"), "videos/result-1/2": []byte("ftyp")}
	videos := newVideos(blobs)
	ref := func(user, runID string, number int) run.VideoRef {
		return run.VideoRef{Ref: run.Ref{UserID: user, ProjectID: projID, RunID: runID}, ResultID: "result-1",
			Number: number}
	}

	video, err := videos.Open(context.Background(), ref("viewer", "run-1", 2))
	if err != nil {
		t.Fatalf("Open = %v", err)
	}
	if got, _ := io.ReadAll(video); string(got) != "ftyp" {
		t.Fatalf("video = %q", got)
	}
	for _, tt := range []struct {
		name   string
		ref    run.VideoRef
		kind   error
		detail string
	}{
		{name: "not filmed", ref: ref("viewer", "run-1", 3), kind: fault.ErrNotFound, detail: "video not found"},
		{name: "filming failed", ref: ref("viewer", "run-1", 1), kind: fault.ErrNotFound, detail: "video not found"},
		{name: "another run", ref: ref("viewer", "run-2", 2), kind: fault.ErrNotFound, detail: "run not found"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := videos.Open(context.Background(), tt.ref)

			requireKind(t, err, tt.kind, tt.detail)
		})
	}
	if _, err := videos.Open(context.Background(), ref("stranger", "run-1", 2)); err == nil {
		t.Fatal("a stranger opened the video")
	}
}
