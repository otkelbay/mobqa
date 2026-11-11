package hubws_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/transport/hubws"
	"github.com/mobqa/backend/internal/usecase/action"
	"github.com/mobqa/backend/internal/usecase/device"
	"github.com/mobqa/backend/internal/usecase/hub"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/run"
	"github.com/mobqa/backend/internal/usecase/test"
	"github.com/mobqa/backend/internal/usecase/ui"
)

const waitLimit = 5 * time.Second

type fakeHubs struct {
	inventories chan hub.Inventory
	disconnects chan string
}

func newFakeHubs() *fakeHubs {
	return &fakeHubs{inventories: make(chan hub.Inventory, 16), disconnects: make(chan string, 16)}
}

func (f *fakeHubs) Authenticate(_ context.Context, token string) (hub.Hub, error) {
	if token != "mqh_good" {
		return hub.Hub{}, fault.WithDetail(fault.ErrUnauthorized, "invalid hub token")
	}
	return hub.Hub{ID: "hub-1"}, nil
}

func (f *fakeHubs) ReportInventory(_ context.Context, _ string, inventory hub.Inventory) error {
	if err := inventory.Validate(); err != nil {
		return err
	}
	f.inventories <- inventory
	return nil
}

func (f *fakeHubs) Heartbeat(context.Context, string) error {
	return nil
}

func (f *fakeHubs) Disconnect(_ context.Context, hubID string) error {
	f.disconnects <- hubID
	return nil
}

type fakeAgent struct {
	mu       sync.Mutex
	serials  []string
	commands []ui.Command
	// execute answers executions; nil refuses them.
	execute func(ctx context.Context, execution run.Execution) (run.Outcome, error)
}

func (a *fakeAgent) Execute(ctx context.Context, execution run.Execution) (run.Outcome, error) {
	if a.execute == nil {
		return run.Outcome{}, errors.New("this hub cannot run tests yet")
	}
	return a.execute(ctx, execution)
}

func (a *fakeAgent) Observe(_ context.Context, target ui.Target) (ui.Screen, error) {
	return ui.Screen{App: "com.shop.app", Width: 1080, Height: 2400, Keyboard: true, Elements: []ui.Element{{
		ID: target.Serial, Text: "Sign in", Class: "Button", Bounds: ui.Rect{X: 40, Y: 1000, Width: 1000, Height: 120},
		Clickable: true, Enabled: true,
	}}}, nil
}

func (a *fakeAgent) Act(_ context.Context, command ui.Command) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if command.Kind == ui.KindType {
		return errors.New("adb can type only ASCII text")
	}
	a.commands = append(a.commands, command)
	return nil
}

func (a *fakeAgent) attach(serials ...string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.serials = serials
}

func (a *fakeAgent) Inventory(context.Context) (hub.Inventory, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	inventory := hub.Inventory{Version: "test"}
	for _, serial := range a.serials {
		inventory.Devices = append(inventory.Devices, hub.ReportedDevice{
			Serial: serial, Platform: project.PlatformAndroid, State: device.StateConnected,
		})
	}
	return inventory, nil
}

func discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(waitLimit):
		t.Fatal("timed out waiting")
		panic("unreachable")
	}
}

func startServer(t *testing.T) (*hubws.Server, *fakeHubs, string) {
	t.Helper()
	hubs := newFakeHubs()
	server := hubws.NewServer(hubs, discard())
	mux := http.NewServeMux()
	mux.Handle("GET "+hubws.Path, server)
	ts := httptest.NewServer(mux)
	t.Cleanup(func() {
		_ = server.Shutdown(context.Background())
		ts.Close()
	})
	return server, hubs, ts.URL
}

func startClient(t *testing.T, url, token string, agent hubws.Agent) <-chan error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	client := hubws.NewClient(hubws.ClientConfig{URL: url, Token: token, PollInterval: 10 * time.Millisecond},
		agent, discard())
	done, stopped := make(chan error, 1), make(chan struct{})
	go func() {
		defer close(stopped)
		done <- client.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-stopped
	})
	return done
}

func dial(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	conn, resp, err := websocket.Dial(context.Background(), url+hubws.Path, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer mqh_good"}},
	})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

func send(t *testing.T, conn *websocket.Conn, env hubws.Envelope) {
	t.Helper()
	if err := wsjson.Write(context.Background(), conn, env); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// closeCode reads until the server closes the connection and returns its close code.
func closeCode(t *testing.T, conn *websocket.Conn) websocket.StatusCode {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	for {
		if _, _, err := conn.Read(ctx); err != nil {
			return websocket.CloseStatus(err)
		}
	}
}

func TestClientReportsInventoryOnConnectAndOnChange(t *testing.T) {
	_, hubs, url := startServer(t)
	agent := &fakeAgent{}
	agent.attach("pixel")

	startClient(t, url, "mqh_good", agent)

	if first := receive(t, hubs.inventories); len(first.Devices) != 1 || first.Devices[0].Serial != "pixel" {
		t.Fatalf("first inventory = %+v", first)
	}
	agent.attach("pixel", "galaxy")
	if second := receive(t, hubs.inventories); len(second.Devices) != 2 {
		t.Fatalf("second inventory = %+v", second)
	}
}

func TestClientStopsWhenTokenIsRejected(t *testing.T) {
	_, _, url := startServer(t)

	done := startClient(t, url, "mqh_revoked", &fakeAgent{})

	if err := receive(t, done); !errors.Is(err, hubws.ErrRejected) {
		t.Fatalf("Run error = %v, want ErrRejected", err)
	}
}

func TestFirstMessageMustBeInventory(t *testing.T) {
	_, hubs, url := startServer(t)
	conn := dial(t, url)

	send(t, conn, hubws.Envelope{Type: hubws.TypeHeartbeat})

	if code := closeCode(t, conn); code != websocket.StatusPolicyViolation {
		t.Fatalf("close code = %v, want policy violation", code)
	}
	if hubID := receive(t, hubs.disconnects); hubID != "hub-1" {
		t.Fatalf("disconnected %q", hubID)
	}
}

func TestInvalidInventoryClosesLink(t *testing.T) {
	_, _, url := startServer(t)
	conn := dial(t, url)

	send(t, conn, hubws.Envelope{Type: hubws.TypeInventory,
		Payload: []byte(`{"version":"1","devices":[{"serial":"","platform":"android","state":"connected"}]}`)})

	if code := closeCode(t, conn); code != websocket.StatusPolicyViolation {
		t.Fatalf("close code = %v, want policy violation", code)
	}
}

func TestReconnectReplacesPreviousConnection(t *testing.T) {
	_, hubs, url := startServer(t)
	inventory := hubws.Envelope{Type: hubws.TypeInventory, Payload: []byte(`{"version":"1","devices":[]}`)}
	first := dial(t, url)
	send(t, first, inventory)
	receive(t, hubs.inventories)

	second := dial(t, url)
	send(t, second, inventory)
	receive(t, hubs.inventories)

	if code := closeCode(t, first); code != websocket.StatusGoingAway {
		t.Fatalf("first connection close code = %v, want going away", code)
	}
	select {
	case hubID := <-hubs.disconnects:
		t.Fatalf("replacing the connection marked %q offline", hubID)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestCallRoutesToConnectedHub(t *testing.T) {
	server, hubs, url := startServer(t)
	ctx := context.Background()
	if _, err := server.Call(ctx, "hub-1", "device.screen", struct{}{}); !errors.Is(err, fault.ErrUnavailable) {
		t.Fatalf("Call before connect error = %v, want ErrUnavailable", err)
	}
	agent := &fakeAgent{}
	startClient(t, url, "mqh_good", agent)
	receive(t, hubs.inventories)

	_, err := server.Call(ctx, "hub-1", "device.screen", struct{}{})

	var remote *hubws.RemoteError
	if !errors.As(err, &remote) || remote.Message != "the hub does not handle this request" {
		t.Fatalf("Call error = %v, want the hub's refusal", err)
	}
}

func TestShutdownClosesSessions(t *testing.T) {
	server, hubs, url := startServer(t)
	conn := dial(t, url)
	send(t, conn, hubws.Envelope{Type: hubws.TypeInventory, Payload: []byte(`{"version":"1","devices":[]}`)})
	receive(t, hubs.inventories)

	codes := make(chan websocket.StatusCode, 1)
	go func() { codes <- closeCode(t, conn) }()
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	if code := receive(t, codes); code != websocket.StatusGoingAway {
		t.Fatalf("close code = %v, want going away", code)
	}
	if hubID := receive(t, hubs.disconnects); hubID != "hub-1" {
		t.Fatalf("disconnected %q", hubID)
	}
}

func connectedHub(t *testing.T, agent *fakeAgent) *hubws.Server {
	t.Helper()
	server, hubs, url := startServer(t)
	startClient(t, url, "mqh_good", agent)
	receive(t, hubs.inventories)
	return server
}

func TestExecuteRoundTrip(t *testing.T) {
	sent := make(chan run.Execution, 1)
	server := connectedHub(t, &fakeAgent{execute: func(_ context.Context, e run.Execution) (run.Outcome, error) {
		sent <- e
		return run.Outcome{Status: run.AttemptFailed, FailedStep: 1, Error: "no button", Steps: []run.StepOutcome{
			{Status: run.StepFailed, Error: "no button", Duration: 1200 * time.Millisecond},
		}, Video: &run.Video{Duration: 9 * time.Second}}, nil
	}})
	execution := run.Execution{
		ID: "result-1/1", Serial: "R58", Platform: project.PlatformAndroid, Title: "Sign in",
		Package: &run.Package{BuildID: "apk", SHA256: "cafe", Size: 42},
		Steps:   []test.Step{{Action: test.ActionTap, Target: "Sign in"}},
		Script: []ui.Op{
			{Step: 1, Kind: ui.KindTap, Selector: &ui.Selector{ID: "app:id/sign_in", Index: 1}},
			{Step: 1, Kind: ui.KindSwipe, Direction: ui.DirectionUp},
		},
		Variables: map[string]string{"PHONE": "+77001234567"}, Timeout: time.Minute,
		Actions: map[string]action.Request{"Fetch OTP": {
			Method: action.MethodGet, URL: "{{API_URL}}/otp", ExpectedStatus: 200, Headers: []action.Header{},
			Extract: []action.Extraction{{Variable: "OTP", Pattern: `(\d+)`}},
		}},
	}

	outcome, err := server.Execute(context.Background(), "hub-1", execution)

	if err != nil || outcome.Status != run.AttemptFailed || outcome.FailedStep != 1 ||
		outcome.Steps[0].Duration != 1200*time.Millisecond || outcome.Video == nil ||
		outcome.Video.Duration != 9*time.Second {
		t.Fatalf("Execute = %+v, %v", outcome, err)
	}
	got := receive(t, sent)
	if got.ID != execution.ID || got.Package.SHA256 != "cafe" || got.Steps[0] != execution.Steps[0] ||
		got.Variables["PHONE"] != "+77001234567" || got.Timeout != time.Minute ||
		*got.Script[0].Selector != *execution.Script[0].Selector || got.Script[1].Direction != ui.DirectionUp ||
		!reflect.DeepEqual(got.Actions, execution.Actions) {
		t.Fatalf("hub received %+v", got)
	}
}

func TestExecuteRefusedByHubIsHubError(t *testing.T) {
	server := connectedHub(t, &fakeAgent{})

	_, err := server.Execute(context.Background(), "hub-1", run.Execution{ID: "result-1/1"})

	var refused *run.HubError
	if !errors.As(err, &refused) || refused.Message != "this hub cannot run tests yet" {
		t.Fatalf("Execute error = %v, want the hub's refusal", err)
	}
}

func TestExecuteOnDisconnectedHubIsUnavailable(t *testing.T) {
	server, _, _ := startServer(t)

	if _, err := server.Execute(context.Background(), "hub-1", run.Execution{}); !errors.Is(err, fault.ErrUnavailable) {
		t.Fatalf("Execute error = %v, want ErrUnavailable", err)
	}
}

func TestAbandonedExecuteIsCancelledOnHub(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan error, 1)
	server := connectedHub(t, &fakeAgent{execute: func(ctx context.Context, _ run.Execution) (run.Outcome, error) {
		close(started)
		<-ctx.Done()
		stopped <- ctx.Err()
		return run.Outcome{}, ctx.Err()
	}})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel()
	}()

	_, err := server.Execute(ctx, "hub-1", run.Execution{ID: "result-1/1"})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute error = %v, want canceled", err)
	}
	if hubErr := receive(t, stopped); !errors.Is(hubErr, context.Canceled) {
		t.Fatalf("hub stopped with %v", hubErr)
	}
}

func (a *fakeAgent) Capture(_ context.Context, target ui.Target) ([]byte, error) {
	if target.Platform != project.PlatformAndroid {
		return nil, errors.New("this hub cannot drive ios devices")
	}
	return []byte("jpeg of " + target.Serial), nil
}

// Perform extracts the phone it was given as OTP, and refuses a request without an expected status.
func (a *fakeAgent) Perform(_ context.Context, request action.Request, variables map[string]string,
) (map[string]string, error) {
	if request.ExpectedStatus == 0 {
		return nil, errors.New("expected status 200, got 0")
	}
	return map[string]string{request.Extract[0].Variable: variables["PHONE"] + request.Headers[0].Value}, nil
}

// Publish and Watch answer with what they were asked, and refuse a device the hub does not stream.
func (a *fakeAgent) Publish(_ context.Context, target ui.Target, offer string) (string, error) {
	if target.Platform != project.PlatformIOS {
		return "", errors.New("this hub cannot stream screens")
	}
	return "phone answer to " + offer, nil
}

func (a *fakeAgent) Watch(_ context.Context, target ui.Target, offer string) (string, error) {
	return "viewer answer to " + offer + " for " + target.Serial, nil
}

func TestStreamOffersRoundTripAndRefusal(t *testing.T) {
	server := connectedHub(t, &fakeAgent{})
	iphone := ui.Target{Serial: "00008", Platform: project.PlatformIOS}

	published, err := server.Publish(context.Background(), "hub-1", iphone, "v=0 phone")
	if err != nil || published != "phone answer to v=0 phone" {
		t.Fatalf("Publish = %q, %v", published, err)
	}
	watched, err := server.Watch(context.Background(), "hub-1", iphone, "v=0 browser")
	if err != nil || watched != "viewer answer to v=0 browser for 00008" {
		t.Fatalf("Watch = %q, %v", watched, err)
	}
	_, err = server.Publish(context.Background(), "hub-1", ui.Target{Serial: "R58", Platform: project.PlatformAndroid},
		"v=0")

	var refused *ui.DeviceError
	if !errors.As(err, &refused) || refused.Message != "this hub cannot stream screens" {
		t.Fatalf("Publish error = %v, want the hub's refusal", err)
	}
}

func TestObserveRoundTrip(t *testing.T) {
	server := connectedHub(t, &fakeAgent{})

	screen, err := server.Observe(context.Background(), "hub-1",
		ui.Target{Serial: "R58", Platform: project.PlatformAndroid})

	want := ui.Element{
		ID: "R58", Text: "Sign in", Class: "Button", Bounds: ui.Rect{X: 40, Y: 1000, Width: 1000, Height: 120},
		Clickable: true, Enabled: true,
	}
	if err != nil || screen.App != "com.shop.app" || screen.Height != 2400 || !screen.Keyboard ||
		len(screen.Elements) != 1 || screen.Elements[0] != want {
		t.Fatalf("Observe = %+v, %v", screen, err)
	}
}

func TestActRoundTripAndRefusal(t *testing.T) {
	agent := &fakeAgent{}
	server := connectedHub(t, agent)
	target := ui.Target{Serial: "R58", Platform: project.PlatformAndroid}
	swipe := ui.Command{
		Target: target, Kind: ui.KindSwipe, Point: ui.Point{X: 500, Y: 1800}, To: ui.Point{X: 500, Y: 400},
		Duration: 300 * time.Millisecond,
	}

	if err := server.Act(context.Background(), "hub-1", swipe); err != nil {
		t.Fatalf("Act: %v", err)
	}
	err := server.Act(context.Background(), "hub-1", ui.Command{Target: target, Kind: ui.KindType, Text: "Привет"})

	var refused *ui.DeviceError
	if !errors.As(err, &refused) || refused.Message != "adb can type only ASCII text" {
		t.Fatalf("Act error = %v, want the hub's refusal", err)
	}
	agent.mu.Lock()
	defer agent.mu.Unlock()
	if len(agent.commands) != 1 || agent.commands[0] != swipe {
		t.Fatalf("hub received %+v", agent.commands)
	}
}

func TestCaptureRoundTripAndRefusal(t *testing.T) {
	server := connectedHub(t, &fakeAgent{})

	shot, err := server.Capture(context.Background(), "hub-1", ui.Target{Serial: "R58", Platform: project.PlatformAndroid})
	if err != nil || string(shot) != "jpeg of R58" {
		t.Fatalf("Capture = %q, %v", shot, err)
	}
	_, err = server.Capture(context.Background(), "hub-1", ui.Target{Serial: "00008", Platform: project.PlatformIOS})

	var refused *ui.DeviceError
	if !errors.As(err, &refused) || refused.Message != "this hub cannot drive ios devices" {
		t.Fatalf("Capture error = %v, want the hub's refusal", err)
	}
}

func TestPerformRoundTripAndRefusal(t *testing.T) {
	server := connectedHub(t, &fakeAgent{})
	request := action.Request{
		Method: action.MethodGet, URL: "{{API_URL}}/otp", ExpectedStatus: 200,
		Headers: []action.Header{{Name: "X-Suffix", Value: "-1"}},
		Extract: []action.Extraction{{Variable: "OTP", Pattern: `(\d+)`}},
	}

	extracted, err := server.Perform(context.Background(), "hub-1", request, map[string]string{"PHONE": "7701"})
	if err != nil || extracted["OTP"] != "7701-1" {
		t.Fatalf("Perform = %v, %v", extracted, err)
	}
	request.ExpectedStatus = 0
	_, err = server.Perform(context.Background(), "hub-1", request, nil)

	var refused *ui.DeviceError
	if !errors.As(err, &refused) || refused.Message != "expected status 200, got 0" {
		t.Fatalf("Perform error = %v, want the hub's refusal", err)
	}
}

// buildServer serves one package to the hub token and counts downloads.
func buildServer(t *testing.T, content []byte) (string, *atomic.Int32) {
	t.Helper()
	var downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mqh_good" || r.URL.Path != hubws.BuildsPath+"build-1" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		downloads.Add(1)
		_, _ = w.Write(content)
	}))
	t.Cleanup(server.Close)
	return server.URL, &downloads
}

func packageOf(content []byte) run.Package {
	sum := sha256.Sum256(content)
	return run.Package{BuildID: "build-1", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(content))}
}

func TestPackagesDownloadOnceAndKeepTheNewest(t *testing.T) {
	content := []byte("apk bytes")
	base, downloads := buildServer(t, content)
	dir := t.TempDir()
	for i := range 4 {
		old, used := filepath.Join(dir, strings.Repeat(strconv.Itoa(i), 64)+".apk"), time.Now().Add(-time.Hour)
		if err := errors.Join(os.WriteFile(old, nil, 0o600), os.Chtimes(old, used, used)); err != nil {
			t.Fatal(err)
		}
	}
	packages, err := hubws.NewPackages(hubws.PackagesConfig{
		URL: strings.Replace(base, "http://", "ws://", 1), Token: "mqh_good", Dir: dir,
	})
	if err != nil {
		t.Fatalf("NewPackages: %v", err)
	}
	pkg := packageOf(content)

	for range 2 {
		path, err := packages.Fetch(context.Background(), project.PlatformAndroid, pkg)
		if err != nil || path != filepath.Join(dir, pkg.SHA256+".apk") {
			t.Fatalf("Fetch = %q, %v", path, err)
		}
	}
	ipa, err := packages.Fetch(context.Background(), project.PlatformIOS, pkg)

	if err != nil || filepath.Ext(ipa) != ".ipa" || downloads.Load() != 2 {
		t.Fatalf("Fetch ipa = %q, %v after %d downloads", ipa, err, downloads.Load())
	}
	if got, err := os.ReadFile(ipa); err != nil || string(got) != string(content) {
		t.Fatalf("package = %q, %v", got, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 3 {
		t.Fatalf("kept %d packages, %v", len(entries), err)
	}
}

func TestPackagesRejectWhatDoesNotMatchTheBuild(t *testing.T) {
	content := []byte("apk bytes")
	base, _ := buildServer(t, content)
	dir := t.TempDir()
	tampered := packageOf([]byte("other bytes"))
	tampered.Size = int64(len(content))
	tests := []struct {
		name  string
		token string
		pkg   run.Package
	}{
		{name: "wrong digest", token: "mqh_good", pkg: tampered},
		{name: "short file", token: "mqh_good", pkg: run.Package{BuildID: "build-1", SHA256: tampered.SHA256, Size: 99}},
		{name: "rejected token", token: "mqh_bad", pkg: packageOf(content)},
		{name: "no digest", token: "mqh_good", pkg: run.Package{BuildID: "build-1", Size: 9}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			packages, err := hubws.NewPackages(hubws.PackagesConfig{URL: base, Token: tt.token, Dir: dir})
			if err != nil {
				t.Fatalf("NewPackages: %v", err)
			}

			if path, err := packages.Fetch(context.Background(), project.PlatformAndroid, tt.pkg); err == nil {
				t.Fatalf("Fetch = %q, want an error", path)
			}
			if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
				t.Fatalf("left %d files, %v", len(entries), err)
			}
		})
	}
}

func TestVideosAreUploadedWithTheHubToken(t *testing.T) {
	received := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.Header.Get("Authorization") != "Bearer mqh_good" ||
			r.URL.Path != hubws.VideosPath+"result-1/2" || r.ContentLength != 4 {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		received <- string(body)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	path := filepath.Join(t.TempDir(), "attempt.mp4")
	if err := os.WriteFile(path, []byte("ftyp"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		token     string
		execution string
		wantErr   bool
	}{
		{name: "uploaded", token: "mqh_good", execution: "result-1/2"},
		{name: "refused", token: "mqh_bad", execution: "result-1/2", wantErr: true},
		{name: "no attempt", token: "mqh_good", execution: "result-1", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			videos := hubws.NewVideos(hubws.VideosConfig{
				URL: strings.Replace(server.URL, "http://", "ws://", 1), Token: tt.token,
			})

			err := videos.Upload(context.Background(), tt.execution, path)

			if (err != nil) != tt.wantErr {
				t.Fatalf("Upload error = %v, want error %v", err, tt.wantErr)
			}
			if !tt.wantErr && receive(t, received) != "ftyp" {
				t.Fatal("the API received another video")
			}
		})
	}
}
