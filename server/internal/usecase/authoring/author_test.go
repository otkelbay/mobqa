package authoring_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/action"
	"github.com/mobqa/backend/internal/usecase/authoring"
	"github.com/mobqa/backend/internal/usecase/chat"
	"github.com/mobqa/backend/internal/usecase/environment"
	"github.com/mobqa/backend/internal/usecase/knowledge"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/test"
	"github.com/mobqa/backend/internal/usecase/ui"
	"github.com/mobqa/backend/internal/worker"
)

const phone = "+77001234567"

type fakeWork struct {
	work     authoring.Work
	started  bool
	finishes []authoring.Finish
	jobs     []worker.EnqueueInput
}

func (f *fakeWork) Work(_ context.Context, id string) (authoring.Work, error) {
	if id != f.work.Authoring.ID {
		return authoring.Work{}, fault.ErrNotFound
	}
	return f.work, nil
}

func (f *fakeWork) StartAuthoring(context.Context, string) (bool, error) {
	f.started = true
	return true, nil
}

func (f *fakeWork) FinishAuthoring(_ context.Context, _ string, finish authoring.Finish,
	jobs ...worker.EnqueueInput,
) error {
	f.finishes = append(f.finishes, finish)
	f.jobs = append(f.jobs, jobs...)
	return nil
}

func signIn() *fakeWork {
	return &fakeWork{work: authoring.Work{
		Authoring: authoring.Authoring{
			ID: "authoring-1", ProjectID: projectID, DeviceID: "device-1", Platform: project.PlatformAndroid,
			EnvironmentID: "env-1", EnvironmentName: "Staging", Status: authoring.StatusQueued,
		},
		Title: "Sign in",
		Steps: []test.Step{
			{Action: test.ActionInput, Target: "Phone"},
			{Action: test.ActionTap, Target: "Continue", ExpectedResult: "Home opens"},
		},
		HubID: "hub-1", Serial: "R58",
	}}
}

type fakeEnvironments struct {
	missing bool
}

func (f fakeEnvironments) Resolve(context.Context, string, string) (environment.Environment, error) {
	if f.missing {
		return environment.Environment{}, fault.ErrNotFound
	}
	return environment.Environment{Variables: []environment.Variable{
		{Name: "PHONE", Label: "Phone", Rule: "+7 and ten digits", Value: phone},
	}}, nil
}

type fakeProjects struct {
	profile project.AuthProfile
}

func (f fakeProjects) Project(context.Context, string) (project.Project, error) {
	return project.Project{ID: projectID, PackageName: "com.shop.app", AuthProfile: f.profile}, nil
}

type fakeActions struct{}

func (fakeActions) Actions(context.Context, string) ([]action.Action, error) {
	return []action.Action{{ID: "action-1", Name: "Fetch OTP", Request: action.Request{
		Method: action.MethodGet, URL: "{{API_URL}}/otp?phone={{PHONE}}", ExpectedStatus: 200,
		Extract: []action.Extraction{{Variable: "OTP", Pattern: `"code":"(\d+)"`}},
	}}}, nil
}

type fakeKnowledge struct{}

func (fakeKnowledge) Retrieve(context.Context, knowledge.Query) (knowledge.Context, error) {
	return knowledge.Context{Items: []knowledge.Item{
		{NoteID: "note-1", Kind: knowledge.KindGotcha, Title: "Keyboard", Text: "The keyboard covers Continue."},
	}}, nil
}

// fakeModel answers each request with the next scripted reply, or fails with err when it is set.
type fakeModel struct {
	replies  []string
	err      error
	requests []chat.Request
}

func (m *fakeModel) Chat(_ context.Context, request chat.Request) (chat.Response, error) {
	m.requests = append(m.requests, request)
	if m.err != nil {
		return chat.Response{}, m.err
	}
	if len(m.replies) == 0 {
		return chat.Response{Text: `{"action":"wait"}`}, nil
	}
	reply := m.replies[0]
	m.replies = m.replies[1:]
	return chat.Response{Text: reply}, nil
}

// fakeDevices shows the screens in order, keeping the last, and refuses commands of the kinds in refuse
// once each. Its hub performs API actions by extracting a code, or fails them with failCall.
type fakeDevices struct {
	screens   []ui.Screen
	refuse    map[ui.Kind]string
	err       error
	commands  []ui.Command
	failCall  string
	performed []map[string]string
}

func (d *fakeDevices) Perform(_ context.Context, _ string, request action.Request, variables map[string]string,
) (map[string]string, error) {
	d.performed = append(d.performed, variables)
	if d.failCall != "" {
		return nil, &ui.DeviceError{Message: d.failCall}
	}
	return map[string]string{request.Extract[0].Variable: "4821"}, nil
}

func (d *fakeDevices) Observe(_ context.Context, hubID string, target ui.Target) (ui.Screen, error) {
	if d.err != nil {
		return ui.Screen{}, d.err
	}
	if hubID != "hub-1" || target.Serial != "R58" {
		return ui.Screen{}, &ui.DeviceError{Message: "no such device"}
	}
	screen := d.screens[0]
	if len(d.screens) > 1 {
		d.screens = d.screens[1:]
	}
	return screen, nil
}

func (d *fakeDevices) Act(_ context.Context, _ string, command ui.Command) error {
	if d.err != nil {
		return d.err
	}
	if message, ok := d.refuse[command.Kind]; ok {
		delete(d.refuse, command.Kind)
		return &ui.DeviceError{Message: message}
	}
	d.commands = append(d.commands, command)
	return nil
}

type fakeLocks struct {
	keys []string
	held bool
}

func (l *fakeLocks) Lock(_ context.Context, key string) (func(), error) {
	l.keys, l.held = append(l.keys, key), true
	return func() { l.held = false }, nil
}

func loginScreen() ui.Screen {
	return ui.Screen{App: "com.shop.app", Width: 1000, Height: 2000, Elements: []ui.Element{
		{ID: "app:id/phone", Label: "Phone", Class: "EditText", Bounds: ui.Rect{X: 0, Y: 400, Width: 1000, Height: 100},
			Editable: true, Enabled: true},
		{Text: "Continue", Class: "Button", Bounds: ui.Rect{X: 0, Y: 600, Width: 1000, Height: 100}, Clickable: true},
	}}
}

func typedScreen() ui.Screen {
	screen := loginScreen()
	screen.Elements[0].Text = phone
	return screen
}

func homeScreen() ui.Screen {
	return ui.Screen{App: "com.shop.app", Width: 1000, Height: 2000, Elements: []ui.Element{
		{Text: "Home", Class: "TextView", Bounds: ui.Rect{Width: 1000, Height: 100}},
	}}
}

type harness struct {
	store   *fakeWork
	model   *fakeModel
	devices *fakeDevices
	locks   *fakeLocks
	envs    fakeEnvironments
	profile project.AuthProfile
}

func newHarness(replies ...string) *harness {
	return &harness{
		store:   signIn(),
		model:   &fakeModel{replies: replies},
		devices: &fakeDevices{screens: []ui.Screen{loginScreen()}, refuse: map[ui.Kind]string{}},
		locks:   &fakeLocks{},
	}
}

func (h *harness) run(attempt int) error {
	author := authoring.NewAuthor(authoring.AuthorConfig{}, authoring.AuthorDependencies{
		Store: h.store, Environments: h.envs, Projects: fakeProjects{profile: h.profile}, Actions: fakeActions{},
		Knowledge: fakeKnowledge{}, Model: h.model, Devices: h.devices, Locks: h.locks,
	})
	return author.Run(context.Background(), worker.Job{
		Kind: authoring.JobKind, Payload: []byte(`{"authoring_id":"authoring-1"}`), Attempt: attempt, MaxAttempts: 3,
	})
}

func (h *harness) finish(t *testing.T) authoring.Finish {
	t.Helper()
	if len(h.store.finishes) != 1 {
		t.Fatalf("finishes = %+v, want one", h.store.finishes)
	}
	return h.store.finishes[0]
}

func TestAuthorScriptsTheTestStepByStep(t *testing.T) {
	h := newHarness(
		`{"thought":"Enter the phone","action":"type","element":0,"text":"{{PHONE}}"}`,
		`{"action":"done"}`,
		`{"action":"tap","element":1}`,
		`{"action":"assert","element":0}`,
		`{"action":"done"}`,
	)
	h.devices.screens = []ui.Screen{loginScreen(), typedScreen(), typedScreen(), homeScreen()}

	if err := h.run(1); err != nil {
		t.Fatalf("Run: %v", err)
	}

	finish := h.finish(t)
	if finish.Status != authoring.StatusSucceeded || finish.Failure != "" || len(finish.Log) != 5 ||
		len(finish.Knowledge) != 1 || finish.Knowledge[0] != "note-1" {
		t.Fatalf("finish = %+v", finish)
	}
	want := []ui.Op{
		{Kind: ui.KindStop, Text: "com.shop.app"},
		{Kind: ui.KindLaunch, Text: "com.shop.app"},
		{Step: 1, Kind: ui.KindTap, Selector: &ui.Selector{ID: "app:id/phone"}},
		{Step: 1, Kind: ui.KindType, Text: "{{PHONE}}"},
		{Step: 2, Kind: ui.KindTap, Selector: &ui.Selector{Text: "Continue"}},
		{Step: 2, Kind: ui.KindAssert, Selector: &ui.Selector{Text: "Home"}},
	}
	requireScript(t, finish.Script, want)
	if typed := h.devices.commands[3]; typed.Kind != ui.KindType || typed.Text != phone {
		t.Fatalf("typed command = %+v, want the phone's value", typed)
	}
	if !h.store.started || len(h.locks.keys) != 1 || h.locks.keys[0] != "device-1" || h.locks.held {
		t.Fatalf("started %t, locks %+v", h.store.started, h.locks)
	}
	if len(h.store.jobs) != 1 || h.store.jobs[0].Kind != authoring.ReviewJobKind ||
		string(h.store.jobs[0].Payload) != `{"authoring_id":"authoring-1"}` {
		t.Fatalf("jobs = %+v, want the review", h.store.jobs)
	}
}

func requireScript(t *testing.T, got, want []ui.Op) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("script = %+v, want %d ops", got, len(want))
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.Step != w.Step || g.Kind != w.Kind || g.Text != w.Text || g.Direction != w.Direction ||
			(g.Selector == nil) != (w.Selector == nil) || (g.Selector != nil && *g.Selector != *w.Selector) {
			t.Fatalf("op %d = %+v (selector %+v), want %+v", i, g, g.Selector, w)
		}
	}
}

func TestAuthorKeepsVariableValuesFromTheModel(t *testing.T) {
	h := newHarness(`{"action":"done"}`, `{"action":"fail","text":"Continue is disabled"}`)
	h.devices.screens = []ui.Screen{typedScreen()}

	if err := h.run(1); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, request := range h.model.requests {
		for _, message := range request.Messages {
			if strings.Contains(message.Text, phone) {
				t.Fatalf("the model saw the phone number: %s", message.Text)
			}
		}
	}
	system, prompt := h.model.requests[0].Messages[0].Text, h.model.requests[0].Messages[1].Text
	if !strings.Contains(system, "The keyboard covers Continue.") ||
		!strings.Contains(prompt, `{{PHONE}} - Phone; +7 and ten digits`) || !strings.Contains(prompt, `"{{PHONE}}"`) ||
		!strings.Contains(prompt, "-> 1. input - Phone") {
		t.Fatalf("system prompt:\n%s\nturn prompt:\n%s", system, prompt)
	}
}

func TestAuthorTellsTheAgentHowToSignIn(t *testing.T) {
	tests := []struct {
		name    string
		profile project.AuthProfile
		want    string
	}{
		{name: "nothing to say", profile: project.AuthProfile{Method: project.AuthNone}},
		{
			name:    "password",
			profile: project.AuthProfile{Method: project.AuthPassword, LoginVar: "EMAIL", PasswordVar: "PASSWORD"},
			want:    "Signing in:\n- Sign in with a login and password, the login is {{EMAIL}}, the password is {{PASSWORD}}.",
		},
		{
			name: "code from an API action",
			profile: project.AuthProfile{Method: project.AuthPhoneOTP, LoginVar: "PHONE",
				OTP: &project.OTPSource{ActionID: "action-1"}, Notes: "Skip the promo carousel."},
			want: "Signing in:\n- Sign in with a one-time code sent to the phone number, {{PHONE}}.\n" +
				`- The API action "Fetch OTP" reads the code the backend sent; a call step runs it and the code is ` +
				"then {{OTP}}.\n- Notes: Skip the promo carousel.",
		},
		{
			name:    "fixed test code",
			profile: project.AuthProfile{Method: project.AuthEmailOTP, OTP: &project.OTPSource{Override: "0000"}},
			want: "Signing in:\n- Sign in with a one-time code sent to the email address.\n" +
				"- Test builds accept the one-time code 0000.",
		},
		{
			name: "single sign-on",
			profile: project.AuthProfile{Method: project.AuthAppleSSO,
				SSO: &project.SSO{Provider: project.SSOApple, AccountVar: "APPLE_ID"}},
			want: "Signing in:\n- Sign in with Apple through the system account picker, choosing the account " +
				"{{APPLE_ID}}; never type into a web sign-in page.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(`{"action":"fail","text":"stop here"}`)
			h.profile = tt.profile

			if err := h.run(1); err != nil {
				t.Fatalf("Run: %v", err)
			}

			system := h.model.requests[0].Messages[0].Text
			if strings.Contains(system, "Signing in:") != (tt.want != "") || !strings.Contains(system, tt.want) {
				t.Fatalf("system prompt:\n%s\nwant:\n%s", system, tt.want)
			}
		})
	}
}

func TestAuthorShowsMisstepsToTheAgent(t *testing.T) {
	h := newHarness(
		`not json`,
		`{"action":"type","element":0,"text":"{{PASSWORD}}"}`,
		`{"action":"swipe","direction":"diagonal"}`,
		`{"action":"tap","element":1}`,
		`{"action":"tap","element":1}`,
		`{"action":"done"}`,
		`{"action":"done"}`,
	)
	h.devices.refuse[ui.KindTap] = "the screen is locked"

	if err := h.run(1); err != nil {
		t.Fatalf("Run: %v", err)
	}

	finish := h.finish(t)
	if finish.Status != authoring.StatusSucceeded {
		t.Fatalf("finish = %+v", finish)
	}
	errorsSeen := []string{
		finish.Log[0].Error, finish.Log[1].Error, finish.Log[2].Error, finish.Log[3].Error, finish.Log[4].Error,
	}
	if !strings.HasPrefix(errorsSeen[0], "the reply was not a valid action") ||
		errorsSeen[1] != "unknown variable: PASSWORD" || errorsSeen[2] != "direction must be up, down, left or right" ||
		errorsSeen[3] != "the screen is locked" || errorsSeen[4] != "" {
		t.Fatalf("turn errors = %q", errorsSeen)
	}
	if prompt := h.model.requests[4].Messages[1].Text; !strings.Contains(prompt, "failed: the screen is locked") {
		t.Fatalf("the agent was not told about the refusal:\n%s", prompt)
	}
	requireScript(t, finish.Script, []ui.Op{
		{Kind: ui.KindStop, Text: "com.shop.app"},
		{Kind: ui.KindLaunch, Text: "com.shop.app"},
		{Step: 1, Kind: ui.KindTap, Selector: &ui.Selector{Text: "Continue"}},
	})
}

func TestAuthorFailsWithTheScriptSoFar(t *testing.T) {
	tests := []struct {
		name    string
		replies []string
		failure string
	}{
		{
			name:    "the agent gives up",
			replies: []string{`{"action":"tap","element":1}`, `{"action":"fail","text":"no phone field"}`},
			failure: "step 1: no phone field",
		},
		{
			name:    "the agent never finishes a step",
			replies: nil,
			failure: "step 1 was not finished within 12 turns",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(tt.replies...)

			if err := h.run(1); err != nil {
				t.Fatalf("Run: %v", err)
			}

			finish := h.finish(t)
			if finish.Status != authoring.StatusFailed || finish.Failure != tt.failure || len(finish.Script) < 2 {
				t.Fatalf("finish = %+v", finish)
			}
		})
	}
}

func TestAuthorRetriesUnreachableHubsUntilTheLastAttempt(t *testing.T) {
	h := newHarness()
	h.devices.err = errors.Join(fault.ErrUnavailable, errors.New("hub offline"))

	if err := h.run(1); !errors.Is(err, fault.ErrUnavailable) || len(h.store.finishes) != 0 {
		t.Fatalf("first attempt = %v, finishes %+v", err, h.store.finishes)
	}
	if err := h.run(3); err != nil {
		t.Fatalf("last attempt: %v", err)
	}

	if finish := h.finish(t); finish.Status != authoring.StatusFailed ||
		finish.Failure != "the device's hub or the model stayed unreachable" {
		t.Fatalf("finish = %+v", finish)
	}
}

func TestAuthorFailsAtOnceWhenItCannotStart(t *testing.T) {
	tests := []struct {
		name               string
		modelErr           error
		environmentMissing bool
		refuseLaunch       string
		failure            string
	}{
		{
			name:     "the model is not configured",
			modelErr: errors.Join(fault.ErrUnavailable, chat.ErrNotConfigured),
			failure:  "the agent is not configured",
		},
		{
			name:               "the environment was deleted",
			environmentMissing: true,
			failure:            "the authoring's environment was deleted",
		},
		{
			name:         "the device cannot start the app",
			refuseLaunch: "package not installed",
			failure:      "the device could not start the app: package not installed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness()
			h.model.err, h.envs.missing = tt.modelErr, tt.environmentMissing
			if tt.refuseLaunch != "" {
				h.devices.refuse[ui.KindLaunch] = tt.refuseLaunch
			}

			if err := h.run(1); err != nil {
				t.Fatalf("Run: %v", err)
			}

			if finish := h.finish(t); finish.Status != authoring.StatusFailed || finish.Failure != tt.failure {
				t.Fatalf("finish = %+v", finish)
			}
			if len(h.store.jobs) != 0 {
				t.Fatalf("jobs = %+v, want no review of a session that never started", h.store.jobs)
			}
		})
	}
}

func TestAuthorSkipsFinishedOrMissingAuthorings(t *testing.T) {
	finished := newHarness()
	finished.store.work.Authoring.Status = authoring.StatusCancelled
	missing := newHarness()
	missing.store.work.Authoring.ID = "other"
	for name, h := range map[string]*harness{"finished": finished, "missing": missing} {
		t.Run(name, func(t *testing.T) {
			if err := h.run(1); err != nil || h.store.started || len(h.model.requests) != 0 {
				t.Fatalf("Run = %v, started %t, model asked %d times", err, h.store.started, len(h.model.requests))
			}
		})
	}
}

// otpSignIn fetches the code the backend sent and types it.
func otpSignIn(h *harness) {
	h.store.work.Steps = []test.Step{
		{Action: test.ActionCall, Target: "Fetch OTP"},
		{Action: test.ActionInput, Target: "Code"},
	}
}

func TestAuthorPerformsCallStepsWithoutTheModel(t *testing.T) {
	h := newHarness(`{"action":"type","element":0,"text":"{{OTP}}"}`, `{"action":"done"}`)
	otpSignIn(h)
	typed := loginScreen()
	typed.Elements[0].Text = "4821"
	h.devices.screens = []ui.Screen{loginScreen(), typed}

	if err := h.run(1); err != nil {
		t.Fatalf("Run: %v", err)
	}

	finish := h.finish(t)
	if finish.Status != authoring.StatusSucceeded || len(h.model.requests) != 2 ||
		h.devices.performed[0]["PHONE"] != phone {
		t.Fatalf("finish = %+v, model asked %d times", finish, len(h.model.requests))
	}
	requireScript(t, finish.Script[2:], []ui.Op{
		{Step: 1, Kind: ui.KindCall, Text: "Fetch OTP"},
		{Step: 2, Kind: ui.KindTap, Selector: &ui.Selector{ID: "app:id/phone"}},
		{Step: 2, Kind: ui.KindType, Text: "{{OTP}}"},
	})
	if finish.Log[0] != (authoring.Turn{Step: 1, Action: "call", Text: "Fetch OTP"}) {
		t.Fatalf("call turn = %+v", finish.Log[0])
	}
	if typed := h.devices.commands[3]; typed.Kind != ui.KindType || typed.Text != "4821" {
		t.Fatalf("typed command = %+v", typed)
	}
	prompt := h.model.requests[1].Messages[1].Text
	if !strings.Contains(prompt, "{{OTP}} - extracted by the API action Fetch OTP") || strings.Contains(prompt, "4821") {
		t.Fatalf("prompt:\n%s", prompt)
	}
}

func TestAuthorFailsOnACallThatFails(t *testing.T) {
	tests := []struct {
		name   string
		action string
		fail   string
		want   string
	}{
		{name: "refused", action: "Fetch OTP", fail: "expected status 200, got 503",
			want: `step 1: API action "Fetch OTP" failed: expected status 200, got 503`},
		{name: "unknown", action: "Seed user", want: `step 1: the project has no API action named "Seed user"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness()
			otpSignIn(h)
			h.store.work.Steps[0].Target = tt.action
			h.devices.failCall = tt.fail

			if err := h.run(1); err != nil {
				t.Fatalf("Run: %v", err)
			}

			finish := h.finish(t)
			if finish.Status != authoring.StatusFailed || finish.Failure != tt.want || len(finish.Log) != 1 ||
				finish.Log[0].Error == "" || len(h.model.requests) != 0 {
				t.Fatalf("finish = %+v", finish)
			}
		})
	}
}
