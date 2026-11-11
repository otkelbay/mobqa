//go:build e2e

package e2e

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"io"
	"mime/multipart"
	"net/http"
	"testing"
	"time"
)

type buildBody struct {
	ID        string `json:"id"`
	Platform  string `json:"platform"`
	Version   string `json:"version"`
	FileName  string `json:"file_name"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	Status    string `json:"status"`
	IsCurrent bool   `json:"is_current"`
	Analysis  *struct {
		PreviousID string   `json:"previous_id"`
		Changes    []string `json:"changes"`
	} `json:"analysis"`
}

func apk(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	files["AndroidManifest.xml"] = "manifest"
	for name, content := range files {
		w, err := writer.Create(name)
		if err != nil {
			t.Fatalf("zip create: %v", err)
		}
		if _, err := io.WriteString(w, content); err != nil {
			t.Fatalf("zip write: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

type buildUpload struct {
	path    string
	token   string
	version string
	content []byte
}

// uploadBuild sends the fields before the file, as the streaming endpoint requires.
func uploadBuild(a *api, u buildUpload, wantStatus int) buildBody {
	a.t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for _, field := range [][2]string{{"platform", "android"}, {"version", u.version}} {
		if err := form.WriteField(field[0], field[1]); err != nil {
			a.t.Fatalf("write field: %v", err)
		}
	}
	part, err := form.CreateFormFile("file", "shop.apk")
	if err != nil {
		a.t.Fatalf("create file part: %v", err)
	}
	if _, err := part.Write(u.content); err != nil {
		a.t.Fatalf("write file part: %v", err)
	}
	if err := form.Close(); err != nil {
		a.t.Fatalf("close form: %v", err)
	}
	var created buildBody
	a.send(call{method: http.MethodPost, path: u.path, token: u.token},
		rawBody{reader: &body, contentType: form.FormDataContentType()}, wantStatus, &created)
	return created
}

// awaitAnalysis polls until the worker has analysed the build.
func awaitAnalysis(a *api, c call) buildBody {
	a.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var found buildBody
		a.do(c, http.StatusOK, &found)
		if found.Status == "ready" {
			return found
		}
		if time.Now().After(deadline) {
			a.t.Fatalf("build %s still %s", found.ID, found.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestBuildUploadAnalysisAndDownload(t *testing.T) {
	api := newAPI(t)
	api.startWorker()
	ada := register(api)
	builds := "/projects/" + createProject(api, ada.AccessToken, "Shop").ID + "/builds"
	firstPackage := apk(t, map[string]string{"classes.dex": "v1"})

	first := uploadBuild(api, buildUpload{path: builds, token: ada.AccessToken, version: "1.0", content: firstPackage},
		http.StatusCreated)
	if first.Status != "processing" || first.FileName != "shop.apk" || first.Size != int64(len(firstPackage)) {
		t.Fatalf("first = %+v", first)
	}
	analysed := awaitAnalysis(api, call{method: http.MethodGet, path: builds + "/" + first.ID, token: ada.AccessToken})
	if analysed.Analysis == nil || analysed.Analysis.PreviousID != "" {
		t.Fatalf("first analysis = %+v", analysed.Analysis)
	}

	api.clock.Advance(time.Minute)
	secondPackage := apk(t, map[string]string{"classes.dex": "v2", "res/new.png": "png"})
	second := uploadBuild(api, buildUpload{path: builds, token: ada.AccessToken, version: "1.1", content: secondPackage},
		http.StatusCreated)
	analysed = awaitAnalysis(api, call{method: http.MethodGet, path: builds + "/" + second.ID, token: ada.AccessToken})
	if analysed.Analysis.PreviousID != first.ID || len(analysed.Analysis.Changes) == 0 ||
		analysed.Analysis.Changes[0] != "version 1.0 → 1.1" {
		t.Fatalf("second analysis = %+v", analysed.Analysis)
	}

	firstPath := builds + "/" + first.ID
	downloaded := api.send(call{method: http.MethodGet, path: firstPath + "/download", token: ada.AccessToken},
		rawBody{}, http.StatusOK, nil)
	if !bytes.Equal(downloaded, firstPackage) {
		t.Fatal("downloaded package differs from the upload")
	}

	var current buildBody
	api.do(call{method: http.MethodPost, path: builds + "/" + second.ID + "/current", token: ada.AccessToken},
		http.StatusOK, &current)
	var page struct {
		Items []buildBody `json:"items"`
		Total int         `json:"total"`
	}
	api.do(call{method: http.MethodGet, path: builds + "?platform=android", token: ada.AccessToken}, http.StatusOK, &page)
	if !current.IsCurrent || page.Total != 2 || page.Items[0].ID != second.ID || !page.Items[0].IsCurrent {
		t.Fatalf("page = %+v", page)
	}

	api.do(call{method: http.MethodDelete, path: firstPath, token: ada.AccessToken}, http.StatusNoContent, nil)
	api.do(call{method: http.MethodGet, path: firstPath + "/download", token: ada.AccessToken}, http.StatusNotFound, nil)
}

func TestBuildUploadRejections(t *testing.T) {
	api := newAPI(t)
	ada := register(api)
	bob := registerAs(api, "bob@example.com")
	project := createProject(api, ada.AccessToken, "Shop")
	builds := "/projects/" + project.ID + "/builds"
	api.do(call{method: http.MethodPost, path: "/projects/" + project.ID + "/members", token: ada.AccessToken,
		body: map[string]string{"email": "bob@example.com", "role": "viewer"}}, http.StatusCreated, nil)

	valid := apk(t, map[string]string{})
	uploadBuild(api, buildUpload{path: builds, token: bob.AccessToken, version: "1.0", content: valid},
		http.StatusForbidden)
	uploadBuild(api, buildUpload{path: builds, token: ada.AccessToken, version: "1.0", content: []byte("not a zip")},
		http.StatusBadRequest)
	incompressible := make([]byte, 2<<20)
	_, _ = rand.Read(incompressible)
	tooLarge := apk(t, map[string]string{"big.bin": string(incompressible)})
	uploadBuild(api, buildUpload{path: builds, token: ada.AccessToken, version: "1.0", content: tooLarge},
		http.StatusBadRequest)
	api.do(call{method: http.MethodPost, path: builds, token: ada.AccessToken, body: map[string]string{}},
		http.StatusBadRequest, nil)

	var page struct {
		Total int `json:"total"`
	}
	api.do(call{method: http.MethodGet, path: builds, token: ada.AccessToken}, http.StatusOK, &page)
	if page.Total != 0 {
		t.Fatalf("rejected uploads were recorded: %d", page.Total)
	}
}
