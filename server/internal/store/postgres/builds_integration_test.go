//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/store/postgres"
	"github.com/mobqa/backend/internal/usecase/build"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/worker"
)

type buildFixture struct {
	projectFixture
	builds    *postgres.Builds
	jobs      *postgres.Jobs
	projectID string
	userID    string
}

func newBuilds(t *testing.T) buildFixture {
	t.Helper()
	f := newProjects(t)
	owner := f.account(t, "ada")
	return buildFixture{
		projectFixture: f, builds: postgres.NewBuilds(f.db), jobs: postgres.NewJobs(f.db),
		projectID: f.create(t, owner.ID, "Shop").ID, userID: owner.ID,
	}
}

func (f buildFixture) upload(t *testing.T, platform project.Platform, version string) build.Build {
	t.Helper()
	id := uuid.Must(uuid.NewV7()).String()
	created, err := f.builds.CreateBuild(context.Background(), build.Build{
		ID: id, ProjectID: f.projectID, Platform: platform, Version: version, BuildNumber: version,
		FileName: "shop.apk", Size: 1024, SHA256: "abc", BlobKey: "builds/" + id, UploadedBy: f.userID,
		Status: build.StatusProcessing,
	}, worker.EnqueueInput{Kind: "build_analysis", Payload: []byte(`{"build_id":"` + id + `"}`), MaxAttempts: 3})
	if err != nil {
		t.Fatalf("CreateBuild: %v", err)
	}
	f.now.Advance(time.Minute)
	return created
}

func TestBuildsCreateQueuesAnalysis(t *testing.T) {
	f := newBuilds(t)

	created := f.upload(t, project.PlatformAndroid, "1.0")

	job, ok, err := f.jobs.Claim(context.Background(), worker.ClaimInput{
		Kind: "build_analysis", Owner: "test", Lease: time.Minute,
	})
	if err != nil || !ok || string(job.Payload) != `{"build_id": "`+created.ID+`"}` {
		t.Fatalf("Claim = %+v, %v, %v", job, ok, err)
	}
	if created.Status != build.StatusProcessing || created.Analysis != nil || created.IsCurrent ||
		!created.UploadedAt.Equal(testStart()) {
		t.Fatalf("created = %+v", created)
	}
}

func TestBuildsCreateRollsBackWithInvalidJob(t *testing.T) {
	f := newBuilds(t)
	id := uuid.Must(uuid.NewV7()).String()

	_, err := f.builds.CreateBuild(context.Background(), build.Build{
		ID: id, ProjectID: f.projectID, Platform: project.PlatformAndroid, Version: "1", BuildNumber: "1",
		FileName: "a.apk", Size: 1, SHA256: "x", BlobKey: "builds/" + id, UploadedBy: f.userID,
		Status: build.StatusProcessing,
	}, worker.EnqueueInput{Kind: "Bad Kind", Payload: []byte(`{}`), MaxAttempts: 1})

	if err == nil {
		t.Fatal("CreateBuild with an invalid job succeeded")
	}
	if _, err := f.builds.Build(context.Background(), f.projectID, id); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("build survived the rollback: %v", err)
	}
}

func TestBuildsProvisioningRoundTrip(t *testing.T) {
	f := newBuilds(t)
	id := uuid.Must(uuid.NewV7()).String()
	want := build.Provisioning{
		ProfileName: "Ad Hoc", TeamID: "TEAM", BundleID: "com.example.shop", ExpiresAt: testStart().Add(48 * time.Hour),
	}

	_, err := f.builds.CreateBuild(context.Background(), build.Build{
		ID: id, ProjectID: f.projectID, Platform: project.PlatformIOS, Version: "1", BuildNumber: "1",
		FileName: "a.ipa", Size: 1, SHA256: "x", BlobKey: "builds/" + id, UploadedBy: f.userID,
		Status: build.StatusProcessing, Provisioning: &want,
	}, worker.EnqueueInput{Kind: "build_analysis", Payload: []byte(`{}`), MaxAttempts: 1})
	if err != nil {
		t.Fatalf("CreateBuild: %v", err)
	}

	found, err := f.builds.Build(context.Background(), f.projectID, id)
	if err != nil || found.Provisioning == nil || *found.Provisioning != want {
		t.Fatalf("Build = %+v, %v", found.Provisioning, err)
	}
}

func TestBuildsListAndPrevious(t *testing.T) {
	f := newBuilds(t)
	first := f.upload(t, project.PlatformAndroid, "1.0")
	f.upload(t, project.PlatformIOS, "1.0")
	third := f.upload(t, project.PlatformAndroid, "1.1")
	ctx := context.Background()

	android, total, err := f.builds.Builds(ctx, build.Query{ProjectID: f.projectID, Platform: "android", Limit: 10})
	if err != nil || total != 2 || android[0].ID != third.ID || android[1].ID != first.ID {
		t.Fatalf("Builds(android) = %+v, %d, %v", android, total, err)
	}
	all, total, err := f.builds.Builds(ctx, build.Query{ProjectID: f.projectID, Limit: 1, Offset: 1})
	if err != nil || total != 3 || len(all) != 1 {
		t.Fatalf("Builds(page 2) = %+v, %d, %v", all, total, err)
	}

	previous, err := f.builds.PreviousBuild(ctx, third)
	if err != nil || previous.ID != first.ID {
		t.Fatalf("PreviousBuild = %+v, %v", previous, err)
	}
	if _, err := f.builds.PreviousBuild(ctx, first); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("PreviousBuild(first) = %v", err)
	}
}

func TestBuildsAnalysisMarksReady(t *testing.T) {
	f := newBuilds(t)
	created := f.upload(t, project.PlatformAndroid, "1.0")
	ctx := context.Background()

	if err := f.builds.SetAnalysis(ctx, created.ID, build.Analysis{Changes: []string{"size +1 KB"}}); err != nil {
		t.Fatalf("SetAnalysis: %v", err)
	}

	found, err := f.builds.Build(ctx, f.projectID, created.ID)
	if err != nil || found.Status != build.StatusReady || found.Analysis == nil ||
		found.Analysis.Changes[0] != "size +1 KB" {
		t.Fatalf("Build = %+v, %v", found, err)
	}
}

func TestBuildsOneCurrentPerPlatform(t *testing.T) {
	f := newBuilds(t)
	first := f.upload(t, project.PlatformAndroid, "1.0")
	second := f.upload(t, project.PlatformAndroid, "1.1")
	ios := f.upload(t, project.PlatformIOS, "1.0")
	ctx := context.Background()

	for _, id := range []string{ios.ID, first.ID, second.ID} {
		if current, err := f.builds.SetCurrent(ctx, f.projectID, id); err != nil || !current.IsCurrent {
			t.Fatalf("SetCurrent(%s) = %+v, %v", id, current, err)
		}
	}

	listed, _, err := f.builds.Builds(ctx, build.Query{ProjectID: f.projectID, Limit: 10})
	if err != nil {
		t.Fatalf("Builds: %v", err)
	}
	current := map[string]bool{}
	for _, b := range listed {
		current[b.ID] = b.IsCurrent
	}
	if !current[second.ID] || current[first.ID] || !current[ios.ID] {
		t.Fatalf("current = %v", current)
	}
}

func TestBuildsDeleteReturnsRow(t *testing.T) {
	f := newBuilds(t)
	created := f.upload(t, project.PlatformAndroid, "1.0")
	ctx := context.Background()

	deleted, err := f.builds.DeleteBuild(ctx, f.projectID, created.ID)
	if err != nil || deleted.BlobKey != created.BlobKey {
		t.Fatalf("DeleteBuild = %+v, %v", deleted, err)
	}
	for _, id := range []string{created.ID, "malformed"} {
		if _, err := f.builds.DeleteBuild(ctx, f.projectID, id); !errors.Is(err, fault.ErrNotFound) {
			t.Fatalf("DeleteBuild(%s) = %v", id, err)
		}
	}
	if err := f.builds.SetAnalysis(ctx, created.ID, build.Analysis{}); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("SetAnalysis(deleted) = %v", err)
	}
}
