//go:build e2e

package e2e

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/mobqa/backend/internal/dbtest"
)

// stopTimeout is how long a binary may take to shut down after SIGTERM.
const stopTimeout = 10 * time.Second

// process is a running binary whose output goes to a file that is shown when the test fails.
type process struct {
	name string
	cmd  *exec.Cmd
	done chan error
}

// buildBinaries compiles the three commands into a temporary directory.
func buildBinaries(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	build := exec.Command("go", "build", "-o", dir, "../cmd/mobqa-api", "../cmd/mobqa-worker", "../cmd/mobqa-hub")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v: %s", err, out)
	}
	return dir
}

func startProcess(t *testing.T, binary string, env []string) *process {
	t.Helper()
	logs := filepath.Join(t.TempDir(), filepath.Base(binary)+".log")
	out, err := os.Create(logs)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary)
	cmd.Env, cmd.Stdout, cmd.Stderr = env, out, out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", binary, err)
	}
	p := &process{name: filepath.Base(binary), cmd: cmd, done: make(chan error, 1)}
	go func() {
		p.done <- cmd.Wait()
		_ = out.Close()
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-p.done
		if t.Failed() {
			written, _ := os.ReadFile(logs)
			t.Logf("%s output:\n%s", p.name, written)
		}
	})
	return p
}

// stop sends SIGTERM and requires the process to exit cleanly in time.
func (p *process) stop(t *testing.T) {
	t.Helper()
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal %s: %v", p.name, err)
	}
	select {
	case err := <-p.done:
		p.done <- err
		if err != nil {
			t.Fatalf("%s exited with %v", p.name, err)
		}
	case <-time.After(stopTimeout):
		t.Fatalf("%s did not stop within %s", p.name, stopTimeout)
	}
}

func freeAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	return listener.Addr().String()
}

// awaitHealthy waits for the API to answer its health check, which it does once migrations have run.
func awaitHealthy(t *testing.T, base string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/healthz", nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.DefaultClient.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the API never became healthy: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// fakeLLM is an OpenAI-compatible provider whose every chat reply is the given content.
func fakeLLM(t *testing.T, content string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "c1", "object": "chat.completion", "model": "vendor/model",
			"choices": []map[string]any{{
				"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": content},
			}},
		})
	}))
	t.Cleanup(server.Close)
	return server.URL
}

// fakeADB writes an adb that sees one Pixel 7, R58M1, on Android 14 showing a white screen. It goes into a
// directory of its own, which becomes the hub's whole PATH, so the hub finds neither Xcode nor ffmpeg.
func fakeADB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	var screen bytes.Buffer
	if err := png.Encode(&screen, image.NewGray(image.Rect(0, 0, 4, 8))); err != nil {
		t.Fatal(err)
	}
	shot := filepath.Join(t.TempDir(), "screen.png")
	script := fmt.Sprintf(`#!/bin/sh
case "$*" in
"devices -l") printf 'List of devices attached\nR58M1 device usb:1-1 product:panther model:Pixel_7 transport_id:1\n' ;;
"-s R58M1 shell getprop ro.build.version.release") echo 14 ;;
"-s R58M1 exec-out screencap -p") exec /bin/cat %q ;;
*) echo "unexpected adb $*" >&2; exit 1 ;;
esac
`, shot)
	if err := errors.Join(os.WriteFile(shot, screen.Bytes(), 0o600),
		os.WriteFile(filepath.Join(dir, "adb"), []byte(script), 0o700)); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestBinariesWorkTogether runs the shipped commands as separate processes, configured only through their
// environment, the way a deployment does: the API migrates an empty database, the worker generates tests
// with the language model, and the hub reports its phone and shows its screen. Each stops cleanly on
// SIGTERM.
func TestBinariesWorkTogether(t *testing.T) {
	database := dbtest.URL(t)
	bin := buildBinaries(t)
	address := freeAddress(t)
	shared := []string{
		"DATABASE_URL=" + database, "MOBQA_BLOB_DIR=" + t.TempDir(), "MOBQA_LOG_LEVEL=debug",
		"MOBQA_LLM_API_KEY=sk-e2e", "MOBQA_LLM_BASE_URL=" + fakeLLM(t, `{"tests":[{"title":"Sign in",`+
			`"priority":"high","tags":[],"steps":[{"action":"tap","target":"Continue"}]}]}`),
		"MOBQA_WORKER_POLL_INTERVAL=100ms",
	}
	apiProcess := startProcess(t, filepath.Join(bin, "mobqa-api"), append([]string{
		"MOBQA_HTTP_ADDRESS=" + address, "MOBQA_TOKEN_SECRET=binaries-secret-binaries-secret-bin",
		"MOBQA_SECRET_KEY=" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)),
	}, shared...))
	base := "http://" + address
	awaitHealthy(t, base)
	api := &api{t: t, url: base, client: &http.Client{Timeout: 10 * time.Second}}
	ada := register(api)
	shop := createProject(api, ada.AccessToken, "Shop")
	reg := registerHub(api, ada.AccessToken, shop.ID, "Device lab")

	workerProcess := startProcess(t, filepath.Join(bin, "mobqa-worker"), shared)
	hubProcess := startProcess(t, filepath.Join(bin, "mobqa-hub"), []string{
		"MOBQA_API_URL=" + base, "MOBQA_HUB_TOKEN=" + reg.Token, "MOBQA_ADB_PATH=adb", "PATH=" + fakeADB(t),
		"MOBQA_HUB_CACHE_DIR=" + t.TempDir(), "MOBQA_HUB_POLL_INTERVAL=100ms", "MOBQA_LOG_LEVEL=debug",
	})

	devices := await(api, call{method: http.MethodGet, path: "/projects/" + shop.ID + "/devices", token: ada.AccessToken},
		func(d []deviceBody) bool { return len(d) == 1 && d[0].Status == "online" })
	screen := api.send(call{method: http.MethodGet, path: "/projects/" + shop.ID + "/devices/" + devices[0].ID +
		"/screen", token: ada.AccessToken}, rawBody{}, http.StatusOK, nil)
	if _, format, err := image.DecodeConfig(bytes.NewReader(screen)); err != nil || format != "jpeg" {
		t.Fatalf("screen is %q, %v", format, err)
	}
	var pending generationBody
	api.do(call{method: http.MethodPost, path: "/projects/" + shop.ID + "/test-generations", token: ada.AccessToken,
		body: map[string]string{"platform": "android", "brief": "Sign in"}}, http.StatusAccepted, &pending)
	done := awaitGeneration(api, call{method: http.MethodGet,
		path: "/projects/" + shop.ID + "/test-generations/" + pending.ID, token: ada.AccessToken})
	if done.Status != "succeeded" || len(done.TestIDs) != 1 {
		t.Fatalf("generation = %+v", done)
	}

	hubProcess.stop(t)
	workerProcess.stop(t)
	apiProcess.stop(t)
}
