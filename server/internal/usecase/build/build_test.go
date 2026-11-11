package build_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/mobqa/backend/internal/clock/clocktest"
	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/build"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/worker"
)

const projID = "project-1"

func testStart() time.Time {
	return time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
}

type fakeAccess struct{}

func (fakeAccess) Require(_ context.Context, _, userID string, need project.Role) (project.Role, error) {
	roles := map[string]project.Role{"viewer": project.RoleViewer, "engineer": project.RoleEngineer}
	role, ok := roles[userID]
	if !ok {
		return "", fault.ErrNotFound
	}
	if !role.Includes(need) {
		return "", fault.ErrForbidden
	}
	return role, nil
}

type file struct {
	*bytes.Reader
}

func (file) Close() error { return nil }

type fakeBlobs struct {
	objects map[string][]byte
	deleted []string
}

func (b *fakeBlobs) Put(_ context.Context, key string, content io.Reader) (int64, error) {
	data, err := io.ReadAll(content)
	if err != nil {
		return 0, err
	}
	b.objects[key] = data
	return int64(len(data)), nil
}

func (b *fakeBlobs) Open(_ context.Context, key string) (build.File, error) {
	data, ok := b.objects[key]
	if !ok {
		return nil, fault.ErrNotFound
	}
	return file{bytes.NewReader(data)}, nil
}

func (b *fakeBlobs) Delete(_ context.Context, key string) error {
	b.deleted = append(b.deleted, key)
	delete(b.objects, key)
	return nil
}

type fakeStore struct {
	builds   map[string]build.Build
	jobs     []worker.EnqueueInput
	analyses map[string]build.Analysis
	previous string
	current  []string
}

func newFakes() (*fakeStore, *fakeBlobs) {
	return &fakeStore{builds: map[string]build.Build{}, analyses: map[string]build.Analysis{}},
		&fakeBlobs{objects: map[string][]byte{}}
}

func (s *fakeStore) CreateBuild(_ context.Context, b build.Build, analysis worker.EnqueueInput) (build.Build, error) {
	s.builds[b.ID] = b
	s.jobs = append(s.jobs, analysis)
	return b, nil
}

func (s *fakeStore) Builds(context.Context, build.Query) ([]build.Build, int, error) {
	return nil, 0, nil
}

func (s *fakeStore) Build(_ context.Context, projectID, id string) (build.Build, error) {
	b, ok := s.builds[id]
	if !ok || b.ProjectID != projectID {
		return build.Build{}, fault.ErrNotFound
	}
	return b, nil
}

func (s *fakeStore) PreviousBuild(context.Context, build.Build) (build.Build, error) {
	if s.previous == "" {
		return build.Build{}, fault.ErrNotFound
	}
	return s.builds[s.previous], nil
}

func (s *fakeStore) SetAnalysis(_ context.Context, buildID string, analysis build.Analysis) error {
	s.analyses[buildID] = analysis
	return nil
}

func (s *fakeStore) SetCurrent(_ context.Context, _, id string) (build.Build, error) {
	s.current = append(s.current, id)
	b := s.builds[id]
	b.IsCurrent = true
	return b, nil
}

func (s *fakeStore) DeleteBuild(ctx context.Context, projectID, id string) (build.Build, error) {
	b, err := s.Build(ctx, projectID, id)
	delete(s.builds, id)
	return b, err
}

func newService(store *fakeStore, blobs *fakeBlobs, maxSize int64) (*build.Service, *clocktest.Manual) {
	now := clocktest.New(testStart())
	return build.NewService(build.Config{MaxSize: maxSize}, build.Dependencies{
		Store: store, Blobs: blobs, Access: fakeAccess{}, Clock: now,
	}), now
}

func archive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := writer.Create(name)
		if err != nil {
			t.Fatalf("zip create: %v", err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip write: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func profile(expires string) string {
	return "\x30\x82binary-cms-header<?xml version=\"1.0\"?><plist version=\"1.0\"><dict>" +
		"<key>Name</key><string>MobQA &amp; Ad Hoc</string>" +
		"<key>TeamIdentifier</key><array><string>TEAM123456</string></array>" +
		"<key>Entitlements</key><dict><key>application-identifier</key>" +
		"<string>TEAM123456.com.example.shop</string><key>get-task-allow</key><false/></dict>" +
		"<key>ExpirationDate</key><date>" + expires + "</date></dict></plist>\x00signature"
}

func upload(svc *build.Service, platform project.Platform, content []byte) (build.Build, error) {
	return svc.Upload(context.Background(), build.UploadInput{
		UserID: "engineer", ProjectID: projID, Platform: platform, Version: "1.2.0",
		FileName: "../../shop release.apk", Content: bytes.NewReader(content),
	})
}

func TestUploadAndroidStoresAndQueuesAnalysis(t *testing.T) {
	store, blobs := newFakes()
	svc, _ := newService(store, blobs, 1<<20)

	created, err := upload(svc, project.PlatformAndroid, archive(t, map[string]string{"AndroidManifest.xml": "x"}))

	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if created.Status != build.StatusProcessing || created.BuildNumber != "1.2.0" ||
		created.FileName != "shop-release.apk" || created.BlobKey != "builds/project-1/"+created.ID ||
		len(created.SHA256) != 64 || created.Provisioning != nil {
		t.Fatalf("created = %+v", created)
	}
	if _, ok := blobs.objects[created.BlobKey]; !ok || int64(len(blobs.objects[created.BlobKey])) != created.Size {
		t.Fatal("package not stored")
	}
	var payload map[string]string
	if err := json.Unmarshal(store.jobs[0].Payload, &payload); err != nil || store.jobs[0].Kind != build.AnalysisJobKind ||
		payload["build_id"] != created.ID || payload["project_id"] != projID {
		t.Fatalf("job = %+v (%v)", store.jobs, err)
	}
}

func TestUploadIOSReadsProvisioning(t *testing.T) {
	store, blobs := newFakes()
	svc, _ := newService(store, blobs, 1<<20)

	created, err := upload(svc, project.PlatformIOS, archive(t, map[string]string{
		"Payload/Shop.app/Shop":                     "binary",
		"Payload/Shop.app/embedded.mobileprovision": profile("2027-01-31T10:00:00Z"),
	}))

	want := build.Provisioning{
		ProfileName: "MobQA & Ad Hoc", TeamID: "TEAM123456", BundleID: "com.example.shop",
		ExpiresAt: time.Date(2027, 1, 31, 10, 0, 0, 0, time.UTC),
	}
	if err != nil || created.Provisioning == nil || *created.Provisioning != want {
		t.Fatalf("Upload = %+v, %v", created.Provisioning, err)
	}
	if !strings.HasSuffix(created.FileName, ".ipa") {
		t.Fatalf("file name = %q", created.FileName)
	}
}

func TestUploadRejectsBadPackages(t *testing.T) {
	tests := []struct {
		name     string
		platform project.Platform
		files    map[string]string
		raw      []byte
		detail   string
	}{
		{
			name: "not a zip", platform: project.PlatformAndroid, raw: []byte("plain text"),
			detail: "the file is not a zip-based APK or IPA",
		},
		{
			name: "ipa uploaded as android", platform: project.PlatformAndroid,
			files: map[string]string{"Payload/Shop.app/Shop": "x"}, detail: "the file is not an Android APK",
		},
		{
			name: "apk uploaded as ios", platform: project.PlatformIOS,
			files: map[string]string{"AndroidManifest.xml": "x"}, detail: "the file is not an iOS IPA",
		},
		{
			name: "broken profile", platform: project.PlatformIOS,
			files: map[string]string{
				"Payload/Shop.app/Shop": "x", "Payload/Shop.app/embedded.mobileprovision": "no plist here",
			},
			detail: "the IPA's provisioning profile cannot be read",
		},
		{
			name: "too large", platform: project.PlatformAndroid, raw: bytes.Repeat([]byte("a"), 2048),
			detail: "the file exceeds the build size limit",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, blobs := newFakes()
			svc, _ := newService(store, blobs, 1024)
			content := tt.raw
			if content == nil {
				content = archive(t, tt.files)
			}

			_, err := upload(svc, tt.platform, content)

			if detail, _ := fault.Detail(err); !errors.Is(err, fault.ErrInvalidInput) || detail != tt.detail {
				t.Fatalf("Upload = %v, want %q", err, tt.detail)
			}
			if len(blobs.objects) != 0 || len(blobs.deleted) != 1 || len(store.builds) != 0 {
				t.Fatalf("rejected package kept: objects %d, deleted %v", len(blobs.objects), blobs.deleted)
			}
		})
	}
}

func TestUploadValidatesBeforeReading(t *testing.T) {
	tests := []struct {
		name string
		in   build.UploadInput
		want error
	}{
		{
			name: "platform",
			in:   build.UploadInput{UserID: "engineer", Platform: "web", Version: "1"}, want: fault.ErrInvalidInput,
		},
		{name: "version", in: build.UploadInput{UserID: "engineer", Platform: "android"}, want: fault.ErrInvalidInput},
		{name: "role", in: build.UploadInput{UserID: "viewer", Platform: "android", Version: "1"}, want: fault.ErrForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, blobs := newFakes()
			svc, _ := newService(store, blobs, 1024)
			tt.in.ProjectID = projID

			_, err := svc.Upload(context.Background(), tt.in)

			if !errors.Is(err, tt.want) || len(blobs.objects) != 0 {
				t.Fatalf("Upload = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestSetCurrentRefusesExpiredProfile(t *testing.T) {
	store, blobs := newFakes()
	svc, now := newService(store, blobs, 1<<20)
	created, err := upload(svc, project.PlatformIOS, archive(t, map[string]string{
		"Payload/Shop.app/embedded.mobileprovision": profile("2026-11-12T09:00:00Z"),
	}))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	ref := build.Ref{UserID: "engineer", ProjectID: projID, BuildID: created.ID}

	if current, err := svc.SetCurrent(context.Background(), ref); err != nil || !current.IsCurrent {
		t.Fatalf("SetCurrent before expiry = %+v, %v", current, err)
	}
	now.Advance(24 * time.Hour)
	_, err = svc.SetCurrent(context.Background(), ref)

	detail, _ := fault.Detail(err)
	if !errors.Is(err, fault.ErrConflict) || detail != "the build's provisioning profile has expired" {
		t.Fatalf("SetCurrent after expiry = %v", err)
	}
}

func TestDeleteRemovesRowAndPackage(t *testing.T) {
	store, blobs := newFakes()
	svc, _ := newService(store, blobs, 1<<20)
	created, err := upload(svc, project.PlatformAndroid, archive(t, map[string]string{"AndroidManifest.xml": "x"}))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	ref := build.Ref{UserID: "engineer", ProjectID: projID, BuildID: created.ID}

	if err := svc.Delete(context.Background(), ref); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if len(store.builds) != 0 || len(blobs.objects) != 0 {
		t.Fatal("build or package survived")
	}
	if err := svc.Delete(context.Background(), ref); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("second Delete = %v", err)
	}
}

func TestDownloadNeedsMembership(t *testing.T) {
	store, blobs := newFakes()
	svc, _ := newService(store, blobs, 1<<20)
	created, err := upload(svc, project.PlatformAndroid, archive(t, map[string]string{"AndroidManifest.xml": "x"}))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}

	ref := build.Ref{UserID: "viewer", ProjectID: projID, BuildID: created.ID}
	_, opened, err := svc.Download(context.Background(), ref)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	_ = opened.Close()
	_, _, err = svc.Download(context.Background(), build.Ref{UserID: "outsider", ProjectID: projID, BuildID: created.ID})
	if !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("Download by outsider = %v", err)
	}
	_, opened, err = svc.DownloadForHub(context.Background(), projID, created.ID)
	if err != nil {
		t.Fatalf("DownloadForHub: %v", err)
	}
	_ = opened.Close()
	_, _, err = svc.DownloadForHub(context.Background(), "other-project", created.ID)
	if detail, _ := fault.Detail(err); !errors.Is(err, fault.ErrNotFound) || detail != "build not found" {
		t.Fatalf("DownloadForHub from another project = %v", err)
	}
}

func analysisJob(t *testing.T, buildID string) worker.Job {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"project_id": projID, "build_id": buildID})
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	return worker.Job{ID: "job-1", Kind: build.AnalysisJobKind, Payload: payload, Attempt: 1, MaxAttempts: 3}
}

func TestAnalyzeFirstBuild(t *testing.T) {
	store, blobs := newFakes()
	svc, _ := newService(store, blobs, 1<<20)
	created, _ := upload(svc, project.PlatformAndroid, archive(t, map[string]string{"AndroidManifest.xml": "x"}))

	if err := svc.Analyze(context.Background(), analysisJob(t, created.ID)); err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	if got := store.analyses[created.ID]; got.PreviousID != "" || len(got.Changes) != 0 {
		t.Fatalf("analysis = %+v", got)
	}
}

func TestAnalyzeComparesWithPrevious(t *testing.T) {
	store, blobs := newFakes()
	svc, _ := newService(store, blobs, 1<<20)
	first, _ := upload(svc, project.PlatformAndroid, archive(t, map[string]string{
		"AndroidManifest.xml": "x", "classes.dex": "old", "res/old.png": "o", "res/same.png": "s",
	}))
	second, err := svc.Upload(context.Background(), build.UploadInput{
		UserID: "engineer", ProjectID: projID, Platform: project.PlatformAndroid, Version: "1.3.0", BuildNumber: "42",
		Content: bytes.NewReader(archive(t, map[string]string{
			"AndroidManifest.xml": "x", "classes.dex": "new code", "res/same.png": "s", "res/a.png": "a", "res/b.png": "b",
		})),
	})
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	store.previous = first.ID

	if err := svc.Analyze(context.Background(), analysisJob(t, second.ID)); err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	got := store.analyses[second.ID]
	if got.PreviousID != first.ID || len(got.Changes) != 4 || got.Changes[0] != "version 1.2.0 → 1.3.0" ||
		got.Changes[1] != "build number 1.2.0 → 42" || !strings.HasPrefix(got.Changes[2], "size +") ||
		got.Changes[3] != "files: 2 added, 1 removed, 1 changed" {
		t.Fatalf("analysis = %+v", got)
	}
}

func TestAnalyzeSkipsDeletedBuildAndRejectsBadPayload(t *testing.T) {
	store, blobs := newFakes()
	svc, _ := newService(store, blobs, 1<<20)

	if err := svc.Analyze(context.Background(), analysisJob(t, "gone")); err != nil {
		t.Fatalf("Analyze(deleted) = %v", err)
	}
	err := svc.Analyze(context.Background(), worker.Job{Payload: []byte("[")})
	if err == nil || len(store.analyses) != 0 {
		t.Fatalf("Analyze(bad payload) = %v", err)
	}
}
