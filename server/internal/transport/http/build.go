package http

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/build"
	"github.com/mobqa/backend/internal/usecase/hub"
	"github.com/mobqa/backend/internal/usecase/project"
)

// maxFieldBytes caps each text field sent before the file in a build upload.
const maxFieldBytes = 256

type Builds interface {
	Upload(ctx context.Context, in build.UploadInput) (build.Build, error)
	List(ctx context.Context, in build.ListInput) (build.Page, error)
	Get(ctx context.Context, ref build.Ref) (build.Build, error)
	Download(ctx context.Context, ref build.Ref) (build.Build, build.File, error)
	DownloadForHub(ctx context.Context, projectID, buildID string) (build.Build, build.File, error)
	SetCurrent(ctx context.Context, ref build.Ref) (build.Build, error)
	Delete(ctx context.Context, ref build.Ref) error
}

// HubTokens tells which hub a hub token belongs to.
type HubTokens interface {
	Authenticate(ctx context.Context, token string) (hub.Hub, error)
}

type provisioningResponse struct {
	ProfileName string    `json:"profile_name"`
	TeamID      string    `json:"team_id"`
	BundleID    string    `json:"bundle_id"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type analysisResponse struct {
	PreviousID string   `json:"previous_id,omitempty"`
	Changes    []string `json:"changes"`
}

type buildResponse struct {
	ID           string                `json:"id"`
	Platform     string                `json:"platform"`
	Version      string                `json:"version"`
	BuildNumber  string                `json:"build_number"`
	FileName     string                `json:"file_name"`
	Size         int64                 `json:"size"`
	SHA256       string                `json:"sha256"`
	UploadedBy   string                `json:"uploaded_by"`
	Status       string                `json:"status"`
	Analysis     *analysisResponse     `json:"analysis,omitempty"`
	IsCurrent    bool                  `json:"is_current"`
	Provisioning *provisioningResponse `json:"provisioning,omitempty"`
	UploadedAt   time.Time             `json:"uploaded_at"`
}

type buildPageResponse struct {
	Items   []buildResponse `json:"items"`
	Total   int             `json:"total"`
	Page    int             `json:"page"`
	Limit   int             `json:"limit"`
	HasMore bool            `json:"has_more"`
}

func newBuildResponse(b build.Build) buildResponse {
	response := buildResponse{
		ID:          b.ID,
		Platform:    string(b.Platform),
		Version:     b.Version,
		BuildNumber: b.BuildNumber,
		FileName:    b.FileName,
		Size:        b.Size,
		SHA256:      b.SHA256,
		UploadedBy:  b.UploadedBy,
		Status:      string(b.Status),
		IsCurrent:   b.IsCurrent,
		UploadedAt:  b.UploadedAt,
	}
	if a := b.Analysis; a != nil {
		response.Analysis = &analysisResponse{PreviousID: a.PreviousID, Changes: a.Changes}
	}
	if p := b.Provisioning; p != nil {
		response.Provisioning = &provisioningResponse{
			ProfileName: p.ProfileName, TeamID: p.TeamID, BundleID: p.BundleID, ExpiresAt: p.ExpiresAt,
		}
	}
	return response
}

// interruptedReader reports a failed read of the request body as the client's fault, so an aborted
// upload is a 400 and not a logged server error.
type interruptedReader struct {
	r io.Reader
}

func (i interruptedReader) Read(p []byte) (int, error) {
	n, err := i.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		return n, fault.WithDetail(fault.ErrInvalidInput, "the upload was interrupted")
	}
	return n, err
}

type buildHandler struct {
	builds Builds
	hubs   HubTokens
	logger *slog.Logger
}

func (h buildHandler) ref(r *http.Request) build.Ref {
	return build.Ref{
		UserID: currentUserID(r.Context()), ProjectID: r.PathValue("projectID"), BuildID: r.PathValue("buildID"),
	}
}

// upload streams a multipart body: the platform, version and build_number fields come first, then the
// file part, which goes to storage without being buffered.
func (h buildHandler) upload(w http.ResponseWriter, r *http.Request) {
	reader, err := r.MultipartReader()
	if err != nil {
		writeFault(w, r, h.logger, fault.WithDetail(fault.ErrInvalidInput, "the upload must be multipart/form-data"))
		return
	}
	in := build.UploadInput{UserID: currentUserID(r.Context()), ProjectID: r.PathValue("projectID")}
	var platform string
	fields := map[string]*string{"platform": &platform, "version": &in.Version, "build_number": &in.BuildNumber}
	for {
		part, err := reader.NextPart()
		if err != nil {
			writeFault(w, r, h.logger, fault.WithDetail(fault.ErrInvalidInput, "the upload has no file part"))
			return
		}
		if part.FormName() == "file" {
			in.Platform, in.FileName, in.Content = project.Platform(platform), part.FileName(), interruptedReader{r: part}
			break
		}
		if err := readField(part, fields); err != nil {
			writeFault(w, r, h.logger, err)
			return
		}
	}
	created, err := h.builds.Upload(r.Context(), in)
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusCreated, newBuildResponse(created))
}

func readField(part *multipart.Part, fields map[string]*string) error {
	target, ok := fields[part.FormName()]
	if !ok {
		return fault.WithDetail(fault.ErrInvalidInput, "upload fields are platform, version, build_number and file")
	}
	value, err := io.ReadAll(io.LimitReader(part, maxFieldBytes+1))
	if err != nil || len(value) > maxFieldBytes {
		return fault.WithDetail(fault.ErrInvalidInput, "upload fields must be short text")
	}
	*target = string(value)
	return nil
}

func (h buildHandler) list(w http.ResponseWriter, r *http.Request) {
	page, err := queryInt(r, "page")
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	limit, err := queryInt(r, "limit")
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	found, err := h.builds.List(r.Context(), build.ListInput{
		UserID: currentUserID(r.Context()), ProjectID: r.PathValue("projectID"),
		Platform: project.Platform(r.URL.Query().Get("platform")), Page: page, Limit: limit,
	})
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	items := make([]buildResponse, len(found.Items))
	for i, item := range found.Items {
		items[i] = newBuildResponse(item)
	}
	writeJSON(w, http.StatusOK, buildPageResponse{
		Items: items, Total: found.Total, Page: found.Page, Limit: found.Limit, HasMore: found.HasMore(),
	})
}

func (h buildHandler) get(w http.ResponseWriter, r *http.Request) {
	found, err := h.builds.Get(r.Context(), h.ref(r))
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, newBuildResponse(found))
}

// download serves the package with range and conditional request support, so an interrupted
// download of a large build resumes instead of starting over.
func (h buildHandler) download(w http.ResponseWriter, r *http.Request) {
	found, file, err := h.builds.Download(r.Context(), h.ref(r))
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	serveBuild(w, r, found, file)
}

// hubDownload serves a build to a hub of its project, which installs it before running tests.
func (h buildHandler) hubDownload(w http.ResponseWriter, r *http.Request) {
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	caller, err := h.hubs.Authenticate(r.Context(), token)
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	found, file, err := h.builds.DownloadForHub(r.Context(), caller.ProjectID, r.PathValue("buildID"))
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	serveBuild(w, r, found, file)
}

func serveBuild(w http.ResponseWriter, r *http.Request, found build.Build, file build.File) {
	defer func() { _ = file.Close() }()
	contentType := "application/octet-stream"
	if found.Platform == project.PlatformAndroid {
		contentType = "application/vnd.android.package-archive"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition",
		mime.FormatMediaType("attachment", map[string]string{"filename": found.FileName}))
	w.Header().Set("ETag", fmt.Sprintf("%q", found.SHA256))
	http.ServeContent(w, r, found.FileName, found.UploadedAt, file)
}

func (h buildHandler) setCurrent(w http.ResponseWriter, r *http.Request) {
	current, err := h.builds.SetCurrent(r.Context(), h.ref(r))
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, newBuildResponse(current))
}

func (h buildHandler) remove(w http.ResponseWriter, r *http.Request) {
	if err := h.builds.Delete(r.Context(), h.ref(r)); err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
