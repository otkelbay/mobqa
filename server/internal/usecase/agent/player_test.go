package agent_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mobqa/backend/internal/clock/clocktest"
	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/action"
	"github.com/mobqa/backend/internal/usecase/agent"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/run"
	"github.com/mobqa/backend/internal/usecase/test"
	"github.com/mobqa/backend/internal/usecase/ui"
)

// shop is a phone whose Pay button shows up only after the screen has been read a few times, as on a
// slow network. It can refuse commands of chosen kinds.
type shop struct {
	mu       sync.Mutex
	slow     int
	blind    int
	refuse   map[ui.Kind]string
	commands []ui.Command
	installs []string
}

func (s *shop) Observe(context.Context, string) (ui.Screen, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.blind > 0 {
		s.blind--
		return ui.Screen{}, errors.New("adb shell: exit status 137")
	}
	screen := ui.Screen{App: "com.shop.app", Width: 1000, Height: 2000, Elements: []ui.Element{
		{ID: "app:id/phone", Label: "Phone", Bounds: ui.Rect{X: 100, Y: 500, Width: 800, Height: 100}},
	}}
	if s.slow > 0 {
		s.slow--
		return screen, nil
	}
	screen.Elements = append(screen.Elements, ui.Element{Text: "Pay", Bounds: ui.Rect{X: 100, Y: 1700, Width: 800,
		Height: 200}})
	return screen, nil
}

func (s *shop) Act(_ context.Context, command ui.Command) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if reason, ok := s.refuse[command.Kind]; ok {
		return errors.New(reason)
	}
	s.commands = append(s.commands, command)
	return nil
}

func (s *shop) Install(_ context.Context, serial, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if reason, ok := s.refuse["install"]; ok {
		return errors.New(reason)
	}
	s.installs = append(s.installs, serial+" "+path)
	return nil
}

// cache stands in for the hub's build cache.
type cache struct {
	fetched []string
	fail    error
}

func (c *cache) Fetch(_ context.Context, _ project.Platform, pkg run.Package) (string, error) {
	if c.fail != nil {
		return "", c.fail
	}
	c.fetched = append(c.fetched, pkg.BuildID)
	return "/cache/" + pkg.SHA256 + ".pkg", nil
}

// backend stands in for the app's staging backend, which sends the last one-time code it issued.
type backend struct {
	status int
	calls  []action.Call
}

func (b *backend) Send(_ context.Context, call action.Call) (action.Reply, error) {
	b.calls = append(b.calls, call)
	return action.Reply{Status: b.status, Body: `{"code":"4821"}`}, nil
}

func (s *shop) sent() []ui.Command {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ui.Command(nil), s.commands...)
}

func player(phone *shop) *agent.Player {
	return playerWith(phone, &cache{})
}

func playerWith(phone *shop, builds *cache) *agent.Player {
	return playerOn(phone, builds, &backend{status: 200})
}

func playerOn(phone *shop, builds *cache, api *backend) *agent.Player {
	return agent.NewPlayer(agent.PlayerConfig{Wait: 50 * time.Millisecond, Settle: time.Millisecond},
		agent.PlayerDependencies{
			Controllers: map[project.Platform]agent.Controller{project.PlatformAndroid: phone},
			Installers:  map[project.Platform]agent.Installer{project.PlatformAndroid: phone},
			Packages:    builds,
			Sender:      api,
			Clock:       clocktest.New(time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)),
		})
}

// signIn calls the backend for the code it sent to the phone and types it.
func signIn() run.Execution {
	return run.Execution{
		Serial: "r58m", Platform: project.PlatformAndroid, Title: "Sign in",
		Steps:     []test.Step{{Action: test.ActionCall, Target: "Fetch OTP"}, {Action: test.ActionInput, Target: "Code"}},
		Variables: map[string]string{"PHONE": "7701", "API_URL": "https://staging.example.com"},
		Actions: map[string]action.Request{"Fetch OTP": {
			Method: action.MethodGet, URL: "{{API_URL}}/otp?phone={{PHONE}}", ExpectedStatus: 200,
			Extract: []action.Extraction{{Variable: "OTP", Pattern: `"code":"(\d+)"`}},
		}},
		Script: []ui.Op{
			{Step: 1, Kind: ui.KindCall, Text: "Fetch OTP"},
			{Step: 2, Kind: ui.KindType, Text: "{{OTP}}"},
		},
	}
}

func checkout() run.Execution {
	return run.Execution{
		Serial: "r58m", Platform: project.PlatformAndroid, Title: "Pay",
		Steps:     []test.Step{{Action: test.ActionInput, Target: "Phone"}, {Action: test.ActionTap, Target: "Pay"}},
		Variables: map[string]string{"PHONE": "+77001234567"},
		Script: []ui.Op{
			{Kind: ui.KindStop, Text: "com.shop.app"},
			{Kind: ui.KindLaunch, Text: "com.shop.app"},
			{Step: 1, Kind: ui.KindTap, Selector: &ui.Selector{ID: "app:id/phone"}},
			{Step: 1, Kind: ui.KindType, Text: "{{PHONE}}"},
			{Step: 2, Kind: ui.KindSwipe, Direction: ui.DirectionUp},
			{Step: 2, Kind: ui.KindTap, Selector: &ui.Selector{Text: "Pay"}},
			{Step: 2, Kind: ui.KindAssert, Selector: &ui.Selector{Label: "Phone"}},
		},
	}
}

func TestPlayerReplaysTheScriptStepByStep(t *testing.T) {
	phone := &shop{slow: 3, blind: 2}

	outcome, err := player(phone).Run(context.Background(), checkout())

	if err != nil || outcome.Status != run.AttemptPassed || len(outcome.Steps) != 2 ||
		outcome.Steps[1].Status != run.StepPassed {
		t.Fatalf("Run = %+v, %v", outcome, err)
	}
	target := ui.Target{Serial: "r58m", Platform: project.PlatformAndroid}
	want := []ui.Command{
		{Target: target, Kind: ui.KindStop, Text: "com.shop.app"},
		{Target: target, Kind: ui.KindLaunch, Text: "com.shop.app"},
		{Target: target, Kind: ui.KindTap, Point: ui.Point{X: 500, Y: 550}},
		{Target: target, Kind: ui.KindType, Text: "+77001234567"},
		{Target: target, Kind: ui.KindSwipe, Point: ui.Point{X: 500, Y: 1600}, To: ui.Point{X: 500, Y: 400}},
		{Target: target, Kind: ui.KindTap, Point: ui.Point{X: 500, Y: 1800}},
	}
	sent := phone.sent()
	if len(sent) != len(want) {
		t.Fatalf("sent = %+v", sent)
	}
	for i := range want {
		if sent[i] != want[i] {
			t.Fatalf("command %d = %+v, want %+v", i, sent[i], want[i])
		}
	}
}

func TestPlayerFailsTheStepAndSkipsTheRest(t *testing.T) {
	tests := []struct {
		name   string
		phone  *shop
		vars   map[string]string
		failed int
		reason string
	}{
		{
			name: "missing element", phone: &shop{slow: 1000}, vars: map[string]string{"PHONE": "1"}, failed: 2,
			reason: `no element with text="Pay" appeared within 50ms`,
		},
		{
			name: "refused command", phone: &shop{refuse: map[ui.Kind]string{ui.KindType: "the keyboard is gone"}},
			vars: map[string]string{"PHONE": "1"}, failed: 1, reason: "the keyboard is gone",
		},
		{name: "unknown variable", phone: &shop{}, failed: 1, reason: "unknown variable: PHONE"},
		{
			name: "unreadable screen", phone: &shop{blind: 1000}, vars: map[string]string{"PHONE": "1"}, failed: 1,
			reason: "the screen could not be read: adb shell: exit status 137",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			execution := checkout()
			execution.Variables = tt.vars

			outcome, err := player(tt.phone).Run(context.Background(), execution)

			if err != nil || outcome.Status != run.AttemptFailed || outcome.FailedStep != tt.failed ||
				outcome.Error != tt.reason || len(outcome.Steps) != 2 ||
				outcome.Steps[tt.failed-1].Status != run.StepFailed || outcome.Steps[tt.failed-1].Error != tt.reason {
				t.Fatalf("Run = %+v, %v", outcome, err)
			}
			if tt.failed == 1 && outcome.Steps[1].Status != run.StepSkipped {
				t.Fatalf("second step = %+v, want skipped", outcome.Steps[1])
			}
		})
	}
}

func TestPlayerReportsAnAppThatCannotBePrepared(t *testing.T) {
	phone := &shop{refuse: map[ui.Kind]string{ui.KindLaunch: "com.shop.app is not installed"}}

	outcome, err := player(phone).Run(context.Background(), checkout())

	if err != nil || outcome.Status != run.AttemptError || len(outcome.Steps) != 0 ||
		outcome.Error != "the app could not be prepared: com.shop.app is not installed" {
		t.Fatalf("Run = %+v, %v", outcome, err)
	}
}

func TestPlayerInstallsEachBuildOncePerDevice(t *testing.T) {
	phone, builds := &shop{}, &cache{}
	p := playerWith(phone, builds)
	execution := checkout()
	execution.Package = &run.Package{BuildID: "apk", SHA256: "cafe", Size: 42}

	for range 2 {
		if outcome, err := p.Run(context.Background(), execution); err != nil || outcome.Status != run.AttemptPassed {
			t.Fatalf("Run = %+v, %v", outcome, err)
		}
	}
	execution.Package = &run.Package{BuildID: "apk-2", SHA256: "beef", Size: 43}
	if _, err := p.Run(context.Background(), execution); err != nil {
		t.Fatalf("Run with a new build: %v", err)
	}

	if len(builds.fetched) != 2 || len(phone.installs) != 2 || phone.installs[1] != "r58m /cache/beef.pkg" {
		t.Fatalf("fetched %v, installed %v", builds.fetched, phone.installs)
	}
}

func TestPlayerReportsABuildThatCannotBeInstalled(t *testing.T) {
	execution := checkout()
	execution.Package = &run.Package{BuildID: "apk", SHA256: "cafe", Size: 42}
	phone := &shop{refuse: map[ui.Kind]string{"install": "INSTALL_FAILED_INSUFFICIENT_STORAGE"}}

	outcome, err := player(phone).Run(context.Background(), execution)
	if err != nil || outcome.Status != run.AttemptError ||
		outcome.Error != "the build could not be installed: INSTALL_FAILED_INSUFFICIENT_STORAGE" {
		t.Fatalf("Run = %+v, %v", outcome, err)
	}
	unreachable := errors.New("the API is unreachable")
	if _, err := playerWith(&shop{}, &cache{fail: unreachable}).Run(context.Background(), execution); !errors.Is(err,
		unreachable) {
		t.Fatalf("Run with a failing fetch = %v", err)
	}
}

func TestPlayerRefusesWhatItCannotRun(t *testing.T) {
	ios := checkout()
	ios.Platform = project.PlatformIOS
	invalid := checkout()
	invalid.Script = append(invalid.Script, ui.Op{Step: 3, Kind: ui.KindBack})
	var unsupported *agent.UnsupportedError

	if _, err := player(&shop{}).Run(context.Background(), ios); !errors.As(err, &unsupported) {
		t.Fatalf("ios error = %v", err)
	}
	if _, err := player(&shop{}).Run(context.Background(), invalid); !errors.Is(err, fault.ErrInvalidInput) {
		t.Fatalf("invalid error = %v", err)
	}
}

func TestPlayerStopsWithTheContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := player(&shop{slow: 1000}).Run(ctx, checkout())

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want canceled", err)
	}
}

func TestPlayerTypesWhatAnActionExtracted(t *testing.T) {
	phone, api := &shop{}, &backend{status: 200}
	execution := signIn()

	outcome, err := playerOn(phone, &cache{}, api).Run(context.Background(), execution)

	if err != nil || outcome.Status != run.AttemptPassed {
		t.Fatalf("Run = %+v, %v", outcome, err)
	}
	if len(api.calls) != 1 || api.calls[0].URL != "https://staging.example.com/otp?phone=7701" {
		t.Fatalf("calls = %+v", api.calls)
	}
	if sent := phone.sent(); len(sent) != 1 || sent[0].Kind != ui.KindType || sent[0].Text != "4821" {
		t.Fatalf("commands = %+v", sent)
	}
	if _, leaked := execution.Variables["OTP"]; leaked {
		t.Fatal("the extracted variable leaked into the execution")
	}
}

func TestPlayerFailsTheStepOfAFailedAction(t *testing.T) {
	missing := signIn()
	missing.Actions = nil
	tests := []struct {
		name      string
		execution run.Execution
		status    int
		want      string
	}{
		{name: "status", execution: signIn(), status: 503, want: `API action "Fetch OTP": expected status 200, got 503`},
		{name: "deleted action", execution: missing, status: 200,
			want: `the project has no API action named "Fetch OTP"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			phone := &shop{}

			outcome, err := playerOn(phone, &cache{}, &backend{status: tt.status}).Run(context.Background(), tt.execution)

			if err != nil || outcome.Status != run.AttemptFailed || outcome.FailedStep != 1 || outcome.Error != tt.want ||
				outcome.Steps[1].Status != run.StepSkipped || len(phone.sent()) != 0 {
				t.Fatalf("Run = %+v, %v", outcome, err)
			}
		})
	}
}
