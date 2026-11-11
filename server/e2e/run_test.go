//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/mobqa/backend/internal/clock"
	"github.com/mobqa/backend/internal/httpcall"
	"github.com/mobqa/backend/internal/transport/hubws"
	"github.com/mobqa/backend/internal/usecase/agent"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/ui"
)

type runBody struct {
	ID              string `json:"id"`
	Status          string `json:"status"`
	EnvironmentName string `json:"environment_name"`
	Summary         struct {
		Total  int `json:"total"`
		Passed int `json:"passed"`
		Flaky  int `json:"flaky"`
		Failed int `json:"failed"`
	} `json:"summary"`
	Results []resultBody `json:"results"`
}

type resultBody struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	Attempts   int    `json:"attempts"`
	FailedStep int    `json:"failed_step"`
}

type attemptBody struct {
	Number int    `json:"number"`
	Status string `json:"status"`
	Error  string `json:"error"`
	Video  *struct {
		DurationMS int64 `json:"duration_ms"`
	} `json:"video"`
}

// lab stands in for adb on a phone that shows a button per test. A title listed for a launch is hidden
// until the next launch, which makes a test flaky; with block set, launching waits until cancelled.
type lab struct {
	mu       sync.Mutex
	launches int
	hide     map[int]string
	hidden   string
	block    bool
	stopped  chan error
	installs [][]byte
}

func (l *lab) Observe(context.Context, string) (ui.Screen, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	screen := ui.Screen{App: "com.example.shop", Width: 1080, Height: 2400}
	for i, title := range []string{"Sign in", "Pay"} {
		if title != l.hidden {
			screen.Elements = append(screen.Elements, ui.Element{
				Text: title, Class: "android.widget.Button", Clickable: true, Enabled: true,
				Bounds: ui.Rect{X: 100, Y: 300 + 200*i, Width: 880, Height: 150},
			})
		}
	}
	return screen, nil
}

func (l *lab) Act(ctx context.Context, command ui.Command) error {
	if command.Kind != ui.KindLaunch {
		return nil
	}
	l.mu.Lock()
	block := l.block
	l.launches++
	l.hidden = l.hide[l.launches]
	l.mu.Unlock()
	if block {
		<-ctx.Done()
		l.stopped <- ctx.Err()
		return ctx.Err()
	}
	return nil
}

func (l *lab) Install(_ context.Context, _, path string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.installs = append(l.installs, content)
	return nil
}

func (l *lab) installed() [][]byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.installs
}

// replayFrom makes the lab count launches from zero, as a run starts after its tests were authored.
func (l *lab) replayFrom(hide map[int]string, block bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.launches, l.hide, l.block = 0, hide, block
}

// echoSFU stands in for the hub's SFU, answering each offer with what it was given. The phone is always
// streaming, so every attempt is filmed, as a file holding the device's serial.
type echoSFU struct{}

const film = "ftyp e2e film of "

func (echoSFU) Record(serial string) (agent.Recording, error) {
	return echoRecording{serial: serial, started: time.Now()}, nil
}

type echoRecording struct {
	serial  string
	started time.Time
}

func (r echoRecording) Stop(context.Context) (agent.Video, error) {
	file, err := os.CreateTemp("", "mobqa-e2e-*.mp4")
	if err != nil {
		return agent.Video{}, err
	}
	_, err = file.WriteString(film + r.serial)
	return agent.Video{Path: file.Name(), Duration: time.Since(r.started)}, errors.Join(err, file.Close())
}

func (echoRecording) Discard() {}

func (echoSFU) Publish(_ context.Context, serial, offer string) (string, error) {
	return "phone answer from " + serial + " to " + offer, nil
}

func (echoSFU) Watch(_ context.Context, serial, offer string) (string, error) {
	return "viewer answer from " + serial + " to " + offer, nil
}

type hubSetup struct {
	shop    projectBody
	token   string
	device  string
	tests   []string
	running runningHub
}

// onlineDevice registers a hub with one Android phone driven by the controller and waits for it to be
// online. The hub replays scripts with short waits, so a missing element fails a step quickly; a
// controller that can install gets the builds the hub downloads from the API, and one that can capture
// shows its screen to the browser. API actions go out over HTTP, as from a real hub, and live screens
// reach a stand-in SFU, which films every attempt; the videos go to the API over HTTP.
func onlineDevice(api *api, token string, controller agent.Controller) hubSetup {
	api.t.Helper()
	shop := createProject(api, token, "Shop")
	reg := registerHub(api, token, shop.ID, "Device lab")
	phones := &bench{}
	phones.plug("R58M1")
	controllers := map[project.Platform]agent.Controller{project.PlatformAndroid: controller}
	installers := map[project.Platform]agent.Installer{}
	if installer, ok := controller.(agent.Installer); ok {
		installers[project.PlatformAndroid] = installer
	}
	capturers := map[project.Platform]agent.Capturer{}
	if capturer, ok := controller.(agent.Capturer); ok {
		capturers[project.PlatformAndroid] = capturer
	}
	packages, err := hubws.NewPackages(hubws.PackagesConfig{URL: api.url, Token: reg.Token, Dir: api.t.TempDir()})
	if err != nil {
		api.t.Fatalf("NewPackages: %v", err)
	}
	sender := httpcall.New()
	running := api.startHubWith(reg.Token, agent.Config{
		Version: "e2e", Discoverers: []agent.Discoverer{phones}, Controllers: controllers, Capturers: capturers,
		Sender: sender, Streamer: echoSFU{},
		Runner: agent.NewFilm(
			agent.NewPlayer(agent.PlayerConfig{Wait: 100 * time.Millisecond, Settle: 5 * time.Millisecond},
				agent.PlayerDependencies{
					Controllers: controllers, Installers: installers, Packages: packages, Sender: sender,
					Clock: clock.System{},
				}),
			echoSFU{}, hubws.NewVideos(hubws.VideosConfig{URL: api.url, Token: reg.Token})),
	})
	api.t.Cleanup(func() {
		running.stop()
		_ = running.wait(api.t)
	})
	devices := await(api, call{method: http.MethodGet, path: "/projects/" + shop.ID + "/devices", token: token},
		func(d []deviceBody) bool { return len(d) == 1 && d[0].Status == "online" })
	return hubSetup{shop: shop, token: token, device: devices[0].ID, running: running}
}

// addScriptedTests creates the tests "Sign in" and "Pay", each tapping its button, and authors them on
// the device so that they can run.
func addScriptedTests(api *api, b *hubSetup) {
	api.t.Helper()
	for i, title := range []string{"Sign in", "Pay"} {
		var created testBody
		api.do(call{method: http.MethodPost, path: "/projects/" + b.shop.ID + "/tests", token: b.token,
			body: map[string]any{"platform": "android", "title": title, "steps": []stepBody{{Action: "tap", Target: title}}}},
			http.StatusCreated, &created)
		api.model.queue(fmt.Sprintf(`{"action":"tap","element":%d}`, i), `{"action":"done"}`)
		var authoring authoringBody
		api.do(call{method: http.MethodPost, path: "/projects/" + b.shop.ID + "/tests/" + created.ID + "/authorings",
			token: b.token, body: map[string]string{"device_id": b.device}}, http.StatusAccepted, &authoring)
		if done := awaitAuthoring(api, "/projects/"+b.shop.ID+"/authorings/"+authoring.ID, b.token); done.Status !=
			"succeeded" {
			api.t.Fatalf("authoring of %s = %+v", title, done)
		}
		b.tests = append(b.tests, created.ID)
	}
}

// awaitRun polls a run until it satisfies the condition.
func awaitRun(api *api, path, token string, condition func(runBody) bool) runBody {
	api.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var found runBody
		api.do(call{method: http.MethodGet, path: path, token: token}, http.StatusOK, &found)
		if condition(found) {
			return found
		}
		if time.Now().After(deadline) {
			api.t.Fatalf("run never matched, last %+v", found)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func finishedRun(r runBody) bool {
	return r.Status != "queued" && r.Status != "running"
}

func TestRunReplaysScriptsOnHubDevice(t *testing.T) {
	api := newAPI(t)
	ada := register(api)
	bob := registerAs(api, "bob@example.com")
	phone := &lab{}
	b := onlineDevice(api, ada.AccessToken, phone)
	api.do(call{method: http.MethodPost, path: "/projects/" + b.shop.ID + "/environments", token: ada.AccessToken,
		body: map[string]any{"name": "Staging", "is_default": true, "variables": []map[string]string{
			{"name": "PHONE", "type": "phone", "value": "+77010000000"},
		}}}, http.StatusCreated, nil)
	api.do(call{method: http.MethodPost, path: "/projects/" + b.shop.ID + "/members", token: ada.AccessToken,
		body: map[string]string{"email": "bob@example.com", "role": "viewer"}}, http.StatusCreated, nil)
	api.startRuns()
	addScriptedTests(api, &b)
	runs := "/projects/" + b.shop.ID + "/runs"
	request := map[string]any{"test_ids": b.tests, "device_ids": []string{b.device}, "retries": 1}
	api.do(call{method: http.MethodPost, path: runs, token: bob.AccessToken, body: request}, http.StatusForbidden, nil)
	phone.replayFrom(map[int]string{2: "Pay"}, false)

	var queued runBody
	api.do(call{method: http.MethodPost, path: runs, token: ada.AccessToken, body: request}, http.StatusAccepted, &queued)
	done := awaitRun(api, runs+"/"+queued.ID, bob.AccessToken, finishedRun)

	if done.Status != "passed" || done.EnvironmentName != "Staging" || done.Summary.Passed != 1 ||
		done.Summary.Flaky != 1 || len(done.Results) != 2 {
		t.Fatalf("run = %+v", done)
	}
	pay := done.Results[1]
	if pay.Title != "Pay" || pay.Status != "flaky" || pay.Attempts != 2 {
		t.Fatalf("pay result = %+v", pay)
	}
	var attempts []attemptBody
	api.do(call{method: http.MethodGet, path: runs + "/" + done.ID + "/results/" + pay.ID + "/attempts",
		token: bob.AccessToken}, http.StatusOK, &attempts)
	if len(attempts) != 2 || attempts[0].Status != "failed" ||
		attempts[0].Error != `no element with text="Pay" appeared within 100ms` || attempts[1].Status != "passed" ||
		attempts[1].Video == nil {
		t.Fatalf("attempts = %+v", attempts)
	}
	video := api.send(call{method: http.MethodGet, path: runs + "/" + done.ID + "/results/" + pay.ID + "/attempts/2/video",
		token: bob.AccessToken}, rawBody{}, http.StatusOK, nil)
	if string(video) != film+"R58M1" {
		t.Fatalf("video = %q", video)
	}
}

func TestRunInstallsTheCurrentBuildOnce(t *testing.T) {
	api := newAPI(t)
	ada := register(api)
	phone := &lab{}
	b := onlineDevice(api, ada.AccessToken, phone)
	api.startRuns()
	addScriptedTests(api, &b)
	builds := "/projects/" + b.shop.ID + "/builds"
	content := apk(t, map[string]string{"classes.dex": "v1"})
	uploaded := uploadBuild(api, buildUpload{path: builds, token: ada.AccessToken, version: "1.0", content: content},
		http.StatusCreated)
	api.do(call{method: http.MethodPost, path: builds + "/" + uploaded.ID + "/current", token: ada.AccessToken},
		http.StatusOK, nil)
	api.do(call{method: http.MethodGet, path: hubws.BuildsPath + uploaded.ID, token: ada.AccessToken},
		http.StatusUnauthorized, nil)
	runs := "/projects/" + b.shop.ID + "/runs"

	for range 2 {
		var queued runBody
		api.do(call{method: http.MethodPost, path: runs, token: ada.AccessToken, body: map[string]any{
			"test_ids": b.tests, "device_ids": []string{b.device},
		}}, http.StatusAccepted, &queued)
		if done := awaitRun(api, runs+"/"+queued.ID, ada.AccessToken, finishedRun); done.Status != "passed" {
			t.Fatalf("run = %+v", done)
		}
	}

	installs := phone.installed()
	if len(installs) != 1 || !bytes.Equal(installs[0], content) {
		t.Fatalf("installed %d packages", len(installs))
	}
}

func TestRunNeedsScriptedTests(t *testing.T) {
	api := newAPI(t)
	ada := register(api)
	b := onlineDevice(api, ada.AccessToken, &lab{})
	var created testBody
	api.do(call{method: http.MethodPost, path: "/projects/" + b.shop.ID + "/tests", token: ada.AccessToken,
		body: map[string]any{"platform": "android", "title": "Refund", "steps": []stepBody{{Action: "tap", Target: "Go"}}}},
		http.StatusCreated, &created)

	api.do(call{method: http.MethodPost, path: "/projects/" + b.shop.ID + "/runs", token: ada.AccessToken,
		body: map[string]any{"test_ids": []string{created.ID}, "device_ids": []string{b.device}}},
		http.StatusConflict, nil)
}

func TestCancelRunStopsHubExecution(t *testing.T) {
	api := newAPI(t)
	ada := register(api)
	phone := &lab{stopped: make(chan error, 1)}
	b := onlineDevice(api, ada.AccessToken, phone)
	api.startRuns()
	addScriptedTests(api, &b)
	phone.replayFrom(nil, true)
	runs := "/projects/" + b.shop.ID + "/runs"

	var queued runBody
	api.do(call{method: http.MethodPost, path: runs, token: ada.AccessToken, body: map[string]any{
		"test_ids": b.tests, "device_ids": []string{b.device},
	}}, http.StatusAccepted, &queued)
	awaitRun(api, runs+"/"+queued.ID, ada.AccessToken, func(r runBody) bool { return r.Status == "running" })
	var cancelled runBody
	api.do(call{method: http.MethodPost, path: runs + "/" + queued.ID + "/cancel", token: ada.AccessToken},
		http.StatusOK, &cancelled)

	if cancelled.Status != "cancelled" {
		t.Fatalf("cancelled = %+v", cancelled)
	}
	select {
	case <-phone.stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the hub kept running the cancelled execution")
	}
	api.do(call{method: http.MethodPost, path: runs + "/" + queued.ID + "/cancel", token: ada.AccessToken},
		http.StatusConflict, nil)
}

func TestRunNeedsOnlineDevice(t *testing.T) {
	api := newAPI(t)
	ada := register(api)
	b := onlineDevice(api, ada.AccessToken, &lab{})
	api.startRuns()
	addScriptedTests(api, &b)
	b.running.stop()
	await(api, call{method: http.MethodGet, path: "/projects/" + b.shop.ID + "/devices", token: ada.AccessToken},
		func(d []deviceBody) bool { return len(d) == 1 && d[0].Status == "offline" })

	api.do(call{method: http.MethodPost, path: "/projects/" + b.shop.ID + "/runs", token: ada.AccessToken,
		body: map[string]any{"test_ids": b.tests, "device_ids": []string{b.device}}}, http.StatusConflict, nil)
}
