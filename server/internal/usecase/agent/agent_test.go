package agent_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/agent"
	"github.com/mobqa/backend/internal/usecase/hub"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/run"
	"github.com/mobqa/backend/internal/usecase/ui"
)

type fakeDiscoverer struct {
	serials []string
	err     error
}

func (f fakeDiscoverer) Discover(context.Context) ([]hub.ReportedDevice, error) {
	devices := make([]hub.ReportedDevice, len(f.serials))
	for i, serial := range f.serials {
		devices[i] = hub.ReportedDevice{Serial: serial}
	}
	return devices, f.err
}

func TestInventoryMergesAndSortsDiscoverers(t *testing.T) {
	android := fakeDiscoverer{serials: []string{"r58m", "emulator-5554"}}
	a := agent.New(agent.Config{Version: "1.0.0", Discoverers: []agent.Discoverer{
		android, fakeDiscoverer{serials: []string{"00008120"}},
	}})

	inventory, err := a.Inventory(context.Background())

	if err != nil || inventory.Version != "1.0.0" || len(inventory.Devices) != 3 {
		t.Fatalf("Inventory = %+v, %v", inventory, err)
	}
	for i, want := range []string{"00008120", "emulator-5554", "r58m"} {
		if inventory.Devices[i].Serial != want {
			t.Fatalf("device %d = %q, want %q", i, inventory.Devices[i].Serial, want)
		}
	}
}

func TestInventoryFailsWhenAnyDiscovererFails(t *testing.T) {
	boom := errors.New("adb not responding")
	a := agent.New(agent.Config{Version: "1.0.0", Discoverers: []agent.Discoverer{
		fakeDiscoverer{serials: []string{"r58m"}}, fakeDiscoverer{err: boom},
	}})

	if _, err := a.Inventory(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("Inventory error = %v, want %v", err, boom)
	}
}

func TestInventoryWithoutDiscoverersIsEmpty(t *testing.T) {
	inventory, err := agent.New(agent.Config{Version: "1.0.0"}).Inventory(context.Background())

	if err != nil || inventory.Devices == nil || len(inventory.Devices) != 0 {
		t.Fatalf("Inventory = %+v, %v", inventory, err)
	}
}

// fakeRunner records how many executions overlap on each device.
type fakeRunner struct {
	mu      sync.Mutex
	active  map[string]int
	overlap bool
}

func (r *fakeRunner) Run(_ context.Context, execution run.Execution) (run.Outcome, error) {
	r.mu.Lock()
	r.active[execution.Serial]++
	r.overlap = r.overlap || r.active[execution.Serial] > 1
	r.mu.Unlock()
	time.Sleep(5 * time.Millisecond)
	r.mu.Lock()
	r.active[execution.Serial]--
	r.mu.Unlock()
	return run.Outcome{Status: run.AttemptPassed}, nil
}

func TestExecuteRunsOneExecutionPerDeviceAtATime(t *testing.T) {
	runner := &fakeRunner{active: map[string]int{}}
	a := agent.New(agent.Config{Version: "1.0.0", Runner: runner})
	var wg sync.WaitGroup

	for i := range 6 {
		serial := []string{"r58m", "00008120"}[i%2]
		wg.Go(func() {
			if _, err := a.Execute(context.Background(), run.Execution{Serial: serial}); err != nil {
				t.Errorf("Execute: %v", err)
			}
		})
	}
	wg.Wait()

	if runner.overlap {
		t.Fatal("two executions ran on one device at once")
	}
}

func TestExecuteWaitingForBusyDeviceStopsWithContext(t *testing.T) {
	block := make(chan struct{})
	a := agent.New(agent.Config{Version: "1.0.0", Runner: blockingRunner(block)})
	go func() { _, _ = a.Execute(context.Background(), run.Execution{Serial: "r58m"}) }()
	time.Sleep(10 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := a.Execute(ctx, run.Execution{Serial: "r58m"})

	close(block)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Execute error = %v, want deadline exceeded", err)
	}
}

type blockingRunner chan struct{}

func (b blockingRunner) Run(context.Context, run.Execution) (run.Outcome, error) {
	<-b
	return run.Outcome{Status: run.AttemptPassed}, nil
}

func TestExecuteWithoutRunnerIsRefused(t *testing.T) {
	_, err := agent.New(agent.Config{Version: "1.0.0"}).Execute(context.Background(), run.Execution{Serial: "r58m"})

	if err == nil || err.Error() != "this hub cannot run tests yet" {
		t.Fatalf("Execute error = %v", err)
	}
}

type fakeController struct {
	commands []ui.Command
}

func (c *fakeController) Observe(_ context.Context, serial string) (ui.Screen, error) {
	return ui.Screen{App: "com.shop.app", Elements: []ui.Element{{ID: serial}}}, nil
}

func (c *fakeController) Act(_ context.Context, command ui.Command) error {
	c.commands = append(c.commands, command)
	return nil
}

func (c *fakeController) Capture(_ context.Context, serial string) ([]byte, error) {
	return []byte("jpeg of " + serial), nil
}

func TestCaptureUsesThePlatformsCapturer(t *testing.T) {
	a := agent.New(agent.Config{Capturers: map[project.Platform]agent.Capturer{
		project.PlatformAndroid: &fakeController{},
	}})
	var unsupported *agent.UnsupportedError

	shot, err := a.Capture(context.Background(), ui.Target{Serial: "r58m", Platform: project.PlatformAndroid})
	if err != nil || string(shot) != "jpeg of r58m" {
		t.Fatalf("Capture = %q, %v", shot, err)
	}
	if _, err := a.Capture(context.Background(), ui.Target{Serial: "0008", Platform: project.PlatformIOS}); !errors.As(err,
		&unsupported) {
		t.Fatalf("Capture error = %v, want unsupported", err)
	}
}

func TestObserveAndActUseThePlatformsController(t *testing.T) {
	android := &fakeController{}
	a := agent.New(agent.Config{Version: "1.0.0", Controllers: map[project.Platform]agent.Controller{
		project.PlatformAndroid: android,
	}})
	target := ui.Target{Serial: "r58m", Platform: project.PlatformAndroid}

	screen, err := a.Observe(context.Background(), target)
	if err != nil || screen.Elements[0].ID != "r58m" {
		t.Fatalf("Observe = %+v, %v", screen, err)
	}
	tap := ui.Command{Target: target, Kind: ui.KindTap, Point: ui.Point{X: 3, Y: 4}}
	if err := a.Act(context.Background(), tap); err != nil || len(android.commands) != 1 || android.commands[0] != tap {
		t.Fatalf("Act = %v, commands %+v", err, android.commands)
	}
}

func TestObserveAndActRefuseWhatTheHubCannotDrive(t *testing.T) {
	a := agent.New(agent.Config{Version: "1.0.0", Controllers: map[project.Platform]agent.Controller{
		project.PlatformAndroid: &fakeController{},
	}})
	iphone := ui.Target{Serial: "00008120", Platform: project.PlatformIOS}
	var unsupported *agent.UnsupportedError

	if _, err := a.Observe(context.Background(), iphone); !errors.As(err, &unsupported) {
		t.Fatalf("Observe error = %v, want unsupported", err)
	}
	if err := a.Act(context.Background(), ui.Command{Target: iphone, Kind: ui.KindBack}); !errors.As(err, &unsupported) {
		t.Fatalf("Act error = %v, want unsupported", err)
	}
	invalid := ui.Command{Target: ui.Target{Serial: "r58m", Platform: project.PlatformAndroid}, Kind: ui.KindType}
	if err := a.Act(context.Background(), invalid); !errors.Is(err, fault.ErrInvalidInput) {
		t.Fatalf("Act error = %v, want invalid input", err)
	}
}

func TestActWaitsForARunningExecution(t *testing.T) {
	block := make(chan struct{})
	a := agent.New(agent.Config{Version: "1.0.0", Runner: blockingRunner(block),
		Controllers: map[project.Platform]agent.Controller{project.PlatformAndroid: &fakeController{}}})
	go func() { _, _ = a.Execute(context.Background(), run.Execution{Serial: "r58m"}) }()
	time.Sleep(10 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err := a.Act(ctx, ui.Command{Target: ui.Target{Serial: "r58m", Platform: project.PlatformAndroid}, Kind: ui.KindBack})

	close(block)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Act error = %v, want deadline exceeded", err)
	}
}

func TestPerformSendsTheActionsRequest(t *testing.T) {
	api := &backend{status: 200}
	hub := agent.New(agent.Config{Sender: api})
	request := signIn().Actions["Fetch OTP"]

	extracted, err := hub.Perform(context.Background(), request, signIn().Variables)

	if err != nil || extracted["OTP"] != "4821" || len(api.calls) != 1 {
		t.Fatalf("Perform = %v, %v", extracted, err)
	}
}

type fakeStreamer struct{}

func (fakeStreamer) Publish(_ context.Context, serial, offer string) (string, error) {
	return "publish answer to " + offer + " from " + serial, nil
}

func (fakeStreamer) Watch(_ context.Context, serial, offer string) (string, error) {
	return "watch answer to " + offer + " from " + serial, nil
}

func TestStreamsGoToTheStreamer(t *testing.T) {
	a := agent.New(agent.Config{Streamer: fakeStreamer{}})
	target := ui.Target{Serial: "00008120", Platform: project.PlatformIOS}

	published, err := a.Publish(context.Background(), target, "v=0 phone")
	if err != nil || published != "publish answer to v=0 phone from 00008120" {
		t.Fatalf("Publish = %q, %v", published, err)
	}
	watched, err := a.Watch(context.Background(), target, "v=0 browser")
	if err != nil || watched != "watch answer to v=0 browser from 00008120" {
		t.Fatalf("Watch = %q, %v", watched, err)
	}
	if _, err := a.Watch(context.Background(), ui.Target{Platform: project.PlatformIOS}, "v=0"); !errors.Is(err,
		fault.ErrInvalidInput) {
		t.Fatalf("Watch without a serial error = %v, want invalid input", err)
	}
}

func TestStreamsWithoutStreamerAreRefused(t *testing.T) {
	_, err := agent.New(agent.Config{}).Watch(context.Background(),
		ui.Target{Serial: "r58m", Platform: project.PlatformAndroid}, "v=0")

	if err == nil || err.Error() != "this hub cannot stream screens" {
		t.Fatalf("Watch error = %v", err)
	}
}
