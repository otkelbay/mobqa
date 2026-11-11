//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mobqa/backend/internal/usecase/ui"
)

type authoringBody struct {
	ID              string     `json:"id"`
	Status          string     `json:"status"`
	EnvironmentName string     `json:"environment_name"`
	Failure         string     `json:"failure"`
	ReviewedAt      *time.Time `json:"reviewed_at"`
	Script          []struct {
		Step     int    `json:"step"`
		Kind     string `json:"kind"`
		Text     string `json:"text"`
		Selector *struct {
			ID   string `json:"id"`
			Text string `json:"text"`
		} `json:"selector"`
	} `json:"script"`
	Log []struct {
		Action string `json:"action"`
	} `json:"log"`
}

// phone stands in for adb: it shows a welcome screen with a Sign in button, then a sign-in form once the
// button is tapped, and echoes what is typed into the phone field.
type phone struct {
	mu       sync.Mutex
	signedIn bool
	typed    string
	received []ui.Command
}

func (p *phone) Observe(context.Context, string) (ui.Screen, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	screen := ui.Screen{App: "com.example.shop", Width: 1080, Height: 2400}
	if !p.signedIn {
		screen.Elements = []ui.Element{{
			ID: "com.example.shop:id/sign_in", Text: "Sign in", Class: "android.widget.Button", Clickable: true,
			Enabled: true, Bounds: ui.Rect{X: 100, Y: 1800, Width: 880, Height: 150},
		}}
		return screen, nil
	}
	screen.Elements = []ui.Element{
		{
			ID: "com.example.shop:id/phone", Text: p.typed, Label: "Phone", Class: "android.widget.EditText",
			Editable: true, Clickable: true, Enabled: true, Bounds: ui.Rect{X: 100, Y: 600, Width: 880, Height: 150},
		},
		{Text: "Welcome back", Class: "android.widget.TextView", Enabled: true,
			Bounds: ui.Rect{X: 100, Y: 300, Width: 880, Height: 100}},
	}
	return screen, nil
}

func (p *phone) Act(_ context.Context, command ui.Command) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.received = append(p.received, command)
	switch {
	case command.Kind == ui.KindTap && !p.signedIn:
		p.signedIn = true
	case command.Kind == ui.KindType:
		p.typed += command.Text
	}
	return nil
}

// shot is what the phone's screen looks like to a person watching it from the browser.
const shot = "\xff\xd8 a JPEG of the welcome screen"

func (p *phone) Capture(context.Context, string) ([]byte, error) {
	return []byte(shot), nil
}

func (p *phone) commands() []ui.Command {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]ui.Command(nil), p.received...)
}

func awaitAuthoring(api *api, path, token string) authoringBody {
	api.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var found authoringBody
		api.do(call{method: http.MethodGet, path: path, token: token}, http.StatusOK, &found)
		if found.Status != "queued" && found.Status != "running" {
			return found
		}
		if time.Now().After(deadline) {
			api.t.Fatalf("authoring never finished, last %+v", found)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAuthoringScriptsATestOnAHubDevice(t *testing.T) {
	api := newAPI(t)
	ada := register(api)
	device := &phone{}
	b := onlineDevice(api, ada.AccessToken, device)
	shop, deviceID := b.shop, b.device
	api.do(call{method: http.MethodPost, path: "/projects/" + shop.ID + "/environments", token: ada.AccessToken,
		body: map[string]any{"name": "Staging", "is_default": true, "variables": []map[string]string{
			{"name": "PHONE", "type": "phone", "value": "+77010000000"},
		}}}, http.StatusCreated, nil)
	var created testBody
	api.do(call{method: http.MethodPost, path: "/projects/" + shop.ID + "/tests", token: ada.AccessToken,
		body: map[string]any{"platform": "android", "title": "Sign in", "steps": []stepBody{
			{Action: "tap", Target: "Sign in"}, {Action: "input", Target: "Phone"},
		}}}, http.StatusCreated, &created)
	api.model.queue(
		`{"thought":"open the form","action":"tap","element":0}`,
		`{"thought":"the form is shown","action":"assert","element":1}`,
		`{"action":"done"}`,
		`{"thought":"the phone field","action":"type","element":0,"text":"{{PHONE}}"}`,
		`{"action":"done"}`,
		`{"lessons":[{"kind":"flow","title":"Sign-in form","content":"Tap Sign in on the welcome screen first."}]}`,
	)
	api.startRuns()

	var queued authoringBody
	api.do(call{method: http.MethodPost, path: "/projects/" + shop.ID + "/tests/" + created.ID + "/authorings",
		token: ada.AccessToken, body: map[string]string{"device_id": deviceID}}, http.StatusAccepted, &queued)
	done := awaitAuthoring(api, "/projects/"+shop.ID+"/authorings/"+queued.ID, ada.AccessToken)

	kinds := make([]string, len(done.Script))
	for i, op := range done.Script {
		kinds[i] = op.Kind
	}
	if done.Status != "succeeded" || done.EnvironmentName != "Staging" || len(done.Log) != 5 ||
		strings.Join(kinds, ",") != "stop,launch,tap,assert,tap,type" ||
		done.Script[2].Selector.ID != "com.example.shop:id/sign_in" || done.Script[3].Selector.Text != "Welcome back" ||
		done.Script[5].Step != 2 || done.Script[5].Text != "{{PHONE}}" {
		t.Fatalf("authoring = %+v", done)
	}
	sent := device.commands()
	if typed := sent[len(sent)-1]; typed.Kind != ui.KindType || typed.Text != "+77010000000" {
		t.Fatalf("last command = %+v", typed)
	}
	api.startWorker()
	reviewed := await(api, call{method: http.MethodGet, path: "/projects/" + shop.ID + "/authorings/" + queued.ID,
		token: ada.AccessToken}, func(a authoringBody) bool { return a.ReviewedAt != nil })
	if reviewed.Status != "succeeded" {
		t.Fatalf("reviewed = %+v", reviewed)
	}
	notes := await(api, call{method: http.MethodGet, path: "/projects/" + shop.ID + "/knowledge", token: ada.AccessToken},
		func(p struct{ Items []noteBody }) bool { return len(p.Items) == 1 && p.Items[0].Indexed })
	if notes.Items[0].Title != "Sign-in form" || notes.Items[0].Source != "agent" {
		t.Fatalf("notes = %+v", notes.Items)
	}
	for _, request := range api.model.allRequests() {
		for _, message := range request.Messages {
			if strings.Contains(message.Text, "+77010000000") {
				t.Fatalf("the model saw the phone number: %s", message.Text)
			}
		}
	}
	var listed []authoringBody
	api.do(call{method: http.MethodGet, path: "/projects/" + shop.ID + "/tests/" + created.ID + "/authorings",
		token: ada.AccessToken}, http.StatusOK, &listed)
	if len(listed) != 1 || listed[0].ID != queued.ID || listed[0].Script != nil {
		t.Fatalf("listed = %+v", listed)
	}
}

func TestAuthoringNeedsAnOnlineDeviceOfTheTestsPlatform(t *testing.T) {
	api := newAPI(t)
	ada := register(api)
	b := onlineDevice(api, ada.AccessToken, &phone{})
	shop, deviceID := b.shop, b.device
	var created testBody
	api.do(call{method: http.MethodPost, path: "/projects/" + shop.ID + "/tests", token: ada.AccessToken,
		body: map[string]any{"platform": "ios", "title": "Sign in", "steps": []stepBody{{Action: "tap", Target: "Go"}}}},
		http.StatusCreated, &created)

	api.do(call{method: http.MethodPost, path: "/projects/" + shop.ID + "/tests/" + created.ID + "/authorings",
		token: ada.AccessToken, body: map[string]string{"device_id": deviceID}}, http.StatusBadRequest, nil)
}

// staging stands in for the app's staging backend: it tells which code it sent to a phone.
func staging(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var asked atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/test/otp" || r.URL.Query().Get("phone") != "+77010000000" ||
			r.Header.Get("Authorization") != "Bearer staging-token" {
			http.NotFound(w, r)
			return
		}
		asked.Add(1)
		_, _ = w.Write([]byte(`{"phone":"+77010000000","code":"4821"}`))
	}))
	t.Cleanup(server.Close)
	return server, &asked
}

func TestCallStepsTypeTheCodeTheBackendSent(t *testing.T) {
	api := newAPI(t)
	ada := register(api)
	device := &phone{}
	b := onlineDevice(api, ada.AccessToken, device)
	backend, asked := staging(t)
	project := "/projects/" + b.shop.ID
	api.do(call{method: http.MethodPost, path: project + "/environments", token: ada.AccessToken,
		body: map[string]any{"name": "Staging", "is_default": true, "variables": []map[string]string{
			{"name": "PHONE", "type": "phone", "value": "+77010000000"},
			{"name": "API_URL", "type": "url", "value": backend.URL},
			{"name": "API_TOKEN", "type": "secret", "value": "staging-token"},
		}}}, http.StatusCreated, nil)
	api.do(call{method: http.MethodPost, path: project + "/actions", token: ada.AccessToken, body: map[string]any{
		"name": "Fetch OTP", "request": map[string]any{
			"method": "GET", "url": "{{API_URL}}/test/otp?phone={{PHONE}}",
			"headers": []map[string]string{{"name": "Authorization", "value": "Bearer {{API_TOKEN}}"}},
			"extract": []map[string]string{{"variable": "OTP", "pattern": `"code":"(\d+)"`}},
		},
	}}, http.StatusCreated, nil)
	var created testBody
	api.do(call{method: http.MethodPost, path: project + "/tests", token: ada.AccessToken,
		body: map[string]any{"platform": "android", "title": "Sign in with a code", "steps": []stepBody{
			{Action: "tap", Target: "Sign in"}, {Action: "call", Target: "Fetch OTP"}, {Action: "input", Target: "Code"},
		}}}, http.StatusCreated, &created)
	api.model.queue(`{"action":"tap","element":0}`, `{"action":"done"}`,
		`{"action":"type","element":0,"text":"{{OTP}}"}`, `{"action":"done"}`)
	api.startRuns()

	var queued authoringBody
	api.do(call{method: http.MethodPost, path: project + "/tests/" + created.ID + "/authorings",
		token: ada.AccessToken, body: map[string]string{"device_id": b.device}}, http.StatusAccepted, &queued)
	done := awaitAuthoring(api, project+"/authorings/"+queued.ID, ada.AccessToken)

	if done.Status != "succeeded" || done.Script[3].Kind != "call" || done.Script[3].Text != "Fetch OTP" ||
		done.Script[5].Text != "{{OTP}}" {
		t.Fatalf("authoring = %+v", done)
	}
	device.mu.Lock()
	device.signedIn, device.typed = false, ""
	device.mu.Unlock()
	var run runBody
	api.do(call{method: http.MethodPost, path: project + "/runs", token: ada.AccessToken, body: map[string]any{
		"test_ids": []string{created.ID}, "device_ids": []string{b.device},
	}}, http.StatusAccepted, &run)
	run = awaitRun(api, project+"/runs/"+run.ID, ada.AccessToken, finishedRun)

	if run.Status != "passed" || asked.Load() != 2 {
		t.Fatalf("run = %+v, backend asked %d times", run, asked.Load())
	}
	sent := device.commands()
	if typed := sent[len(sent)-1]; typed.Kind != ui.KindType || typed.Text != "4821" {
		t.Fatalf("last command = %+v", typed)
	}
}
