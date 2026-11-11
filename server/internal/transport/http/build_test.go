package http

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/build"
	"github.com/mobqa/backend/internal/usecase/hub"
	"github.com/mobqa/backend/internal/usecase/project"
)

type fakeBuilds struct {
	Builds
	uploads  []build.UploadInput
	contents []string
	readErr  error
	file     []byte
}

func (f *fakeBuilds) Upload(_ context.Context, in build.UploadInput) (build.Build, error) {
	content, err := io.ReadAll(in.Content)
	if err != nil {
		f.readErr = err
		return build.Build{}, err
	}
	in.Content = nil
	f.uploads = append(f.uploads, in)
	f.contents = append(f.contents, string(content))
	return build.Build{ID: "build-1", Platform: in.Platform, Version: in.Version, UploadedAt: shopTime()}, nil
}

type readSeekNopCloser struct {
	*bytes.Reader
}

func (readSeekNopCloser) Close() error { return nil }

func (f *fakeBuilds) Download(_ context.Context, ref build.Ref) (build.Build, build.File, error) {
	if ref.BuildID != "build-1" {
		return build.Build{}, nil, fault.WithDetail(fault.ErrNotFound, "build not found")
	}
	return build.Build{
		ID: "build-1", Platform: project.PlatformAndroid, FileName: "shop 1.0.apk", SHA256: "abc", UploadedAt: shopTime(),
	}, readSeekNopCloser{bytes.NewReader(f.file)}, nil
}

func (f *fakeBuilds) DownloadForHub(ctx context.Context, projectID, buildID string) (build.Build, build.File, error) {
	return f.Download(ctx, build.Ref{ProjectID: projectID, BuildID: buildID})
}

type fakeHubTokens struct{}

func (fakeHubTokens) Authenticate(_ context.Context, token string) (hub.Hub, error) {
	if token != "mqh_lab" {
		return hub.Hub{}, fault.ErrUnauthorized
	}
	return hub.Hub{ID: "hub-1", ProjectID: "project-1"}, nil
}

type formPart struct {
	name  string
	value string
}

func multipartRequest(t *testing.T, parts []formPart) *http.Request {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for _, part := range parts {
		var err error
		if part.name == "file" {
			var w io.Writer
			if w, err = form.CreateFormFile("file", "shop.apk"); err == nil {
				_, err = io.WriteString(w, part.value)
			}
		} else {
			err = form.WriteField(part.name, part.value)
		}
		if err != nil {
			t.Fatalf("write part %s: %v", part.name, err)
		}
	}
	if err := form.Close(); err != nil {
		t.Fatalf("close form: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/projects/project-1/builds", &body)
	request.Header.Set("Content-Type", form.FormDataContentType())
	return request
}

func TestUploadBuildStreamsFileAfterFields(t *testing.T) {
	builds := &fakeBuilds{}

	recorder := serveAuthorized(t, Dependencies{Builds: builds}, multipartRequest(t, []formPart{
		{"platform", "android"}, {"version", "1.0"}, {"build_number", "7"}, {"file", "package bytes"},
	}))

	if recorder.Code != http.StatusCreated || len(builds.uploads) != 1 {
		t.Fatalf("status %d, body %s", recorder.Code, recorder.Body.String())
	}
	want := build.UploadInput{
		UserID: "user-1", ProjectID: "project-1", Platform: project.PlatformAndroid, Version: "1.0",
		BuildNumber: "7", FileName: "shop.apk",
	}
	if builds.uploads[0] != want || builds.contents[0] != "package bytes" {
		t.Fatalf("upload = %+v, content %q", builds.uploads[0], builds.contents[0])
	}
}

func TestUploadBuildRejectsMalformedForms(t *testing.T) {
	tests := []struct {
		name   string
		parts  []formPart
		detail string
	}{
		{name: "no file", parts: []formPart{{"platform", "android"}}, detail: "the upload has no file part"},
		{
			name: "unknown field", parts: []formPart{{"admin", "true"}, {"file", "x"}},
			detail: "upload fields are platform, version, build_number and file",
		},
		{
			name: "long field", parts: []formPart{{"version", strings.Repeat("1", 300)}, {"file", "x"}},
			detail: "upload fields must be short text",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builds := &fakeBuilds{}

			recorder := serveAuthorized(t, Dependencies{Builds: builds}, multipartRequest(t, tt.parts))

			requireResponse(t, recorder, http.StatusBadRequest, `{"error":"`+tt.detail+`"}`)
			if len(builds.uploads) != 0 {
				t.Fatal("malformed form reached the usecase")
			}
		})
	}
}

func TestUploadBuildNeedsMultipart(t *testing.T) {
	recorder := serveAuthorized(t, Dependencies{Builds: &fakeBuilds{}},
		jsonRequest(http.MethodPost, "/projects/project-1/builds", `{}`))

	requireResponse(t, recorder, http.StatusBadRequest, `{"error":"the upload must be multipart/form-data"}`)
}

func TestUploadBuildTreatsTruncatedBodyAsClientError(t *testing.T) {
	builds := &fakeBuilds{}
	request := multipartRequest(t, []formPart{{"platform", "android"}, {"file", strings.Repeat("x", 4096)}})
	full, _ := io.ReadAll(request.Body)
	request.Body = io.NopCloser(bytes.NewReader(full[:len(full)/2]))

	recorder := serveAuthorized(t, Dependencies{Builds: builds}, request)

	requireResponse(t, recorder, http.StatusBadRequest, `{"error":"the upload was interrupted"}`)
	if !errors.Is(builds.readErr, fault.ErrInvalidInput) {
		t.Fatalf("read error = %v", builds.readErr)
	}
}

func TestDownloadBuildServesRanges(t *testing.T) {
	builds := &fakeBuilds{file: []byte("0123456789")}
	request := httptest.NewRequest(http.MethodGet, "/projects/project-1/builds/build-1/download", nil)
	request.Header.Set("Range", "bytes=4-6")

	recorder := serveAuthorized(t, Dependencies{Builds: builds}, request)

	requireResponse(t, recorder, http.StatusPartialContent, "456")
	headers := recorder.Header()
	if headers.Get("Content-Disposition") != `attachment; filename="shop 1.0.apk"` || headers.Get("ETag") != `"abc"` ||
		headers.Get("Content-Type") != "application/vnd.android.package-archive" {
		t.Fatalf("headers = %v", headers)
	}
}

func TestDownloadBuildHonoursETag(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/projects/project-1/builds/build-1/download", nil)
	request.Header.Set("If-None-Match", `"abc"`)

	recorder := serveAuthorized(t, Dependencies{Builds: &fakeBuilds{file: []byte("x")}}, request)

	if recorder.Code != http.StatusNotModified {
		t.Fatalf("status = %d", recorder.Code)
	}
}

func TestHubDownloadsBuildsOfItsProject(t *testing.T) {
	deps := Dependencies{Builds: &fakeBuilds{file: []byte("0123456789")}, HubTokens: fakeHubTokens{}}
	request := httptest.NewRequest(http.MethodGet, "/hub/builds/build-1", nil)
	request.Header.Set("Authorization", "Bearer mqh_lab")

	requireResponse(t, serve(t, deps, request), http.StatusOK, "0123456789")
	missing := httptest.NewRequest(http.MethodGet, "/hub/builds/build-2", nil)
	missing.Header.Set("Authorization", "Bearer mqh_lab")
	if recorder := serve(t, deps, missing); recorder.Code != http.StatusNotFound {
		t.Fatalf("missing build status = %d", recorder.Code)
	}
	stranger := httptest.NewRequest(http.MethodGet, "/hub/builds/build-1", nil)
	stranger.Header.Set("Authorization", "Bearer mqh_other")
	if recorder := serve(t, deps, stranger); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unknown hub status = %d", recorder.Code)
	}
}
