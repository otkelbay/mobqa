package agent

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"
	"time"

	"github.com/mobqa/backend/internal/clock"
	"github.com/mobqa/backend/internal/usecase/action"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/run"
	"github.com/mobqa/backend/internal/usecase/ui"
)

// Packages fetches builds from the API and keeps them on the hub's disk.
type Packages interface {
	Fetch(ctx context.Context, platform project.Platform, pkg run.Package) (string, error)
}

// Installer puts the package file at path on a device, replacing the installed app.
type Installer interface {
	Install(ctx context.Context, serial, path string) error
}

type PlayerConfig struct {
	// Wait is how long an op looks for its element before its step fails.
	Wait time.Duration
	// Settle is the pause after an action and between looks at the screen; it must be positive.
	Settle time.Duration
}

type PlayerDependencies struct {
	Controllers map[project.Platform]Controller
	Installers  map[project.Platform]Installer
	Packages    Packages
	Sender      action.Sender
	Clock       clock.Clock
}

// Player runs executions by replaying their scripts: each op waits for the element its selector names,
// acts on it and lets the app settle. A call op sends its API action's request and keeps the variables it
// extracts for the ops after it. The execution's build is installed first, unless the device already has
// it; ops of step 0 then prepare the app before the first step.
type Player struct {
	cfg  PlayerConfig
	deps PlayerDependencies

	mu sync.Mutex
	// installed is the SHA-256 of the build each device got last from this hub.
	installed map[string]string
}

func NewPlayer(cfg PlayerConfig, deps PlayerDependencies) *Player {
	return &Player{cfg: cfg, deps: deps, installed: map[string]string{}}
}

// stepFailure is why the device did not do what the script asked: an element that never appeared, a
// command it could not carry out, an API action that failed or a variable the run does not define.
type stepFailure struct {
	reason string
}

func (f *stepFailure) Error() string {
	return f.reason
}

// Run replays the execution's script. A failed step skips the rest; a build that cannot be installed or
// an app that cannot be prepared is an error outcome, because no step of the test was tried.
func (p *Player) Run(ctx context.Context, execution run.Execution) (run.Outcome, error) {
	controller, ok := p.deps.Controllers[execution.Platform]
	if !ok {
		return run.Outcome{}, &UnsupportedError{Platform: execution.Platform}
	}
	if err := ui.ValidateScript(execution.Script, len(execution.Steps)); err != nil {
		return run.Outcome{}, err
	}
	err := p.install(ctx, execution)
	var failure *stepFailure
	if errors.As(err, &failure) {
		return run.Outcome{Status: run.AttemptError, Error: "the build could not be installed: " + failure.reason}, nil
	}
	if err != nil {
		return run.Outcome{}, err
	}
	r := replay{
		player: p, controller: controller, variables: maps.Clone(execution.Variables), actions: execution.Actions,
		target: ui.Target{Serial: execution.Serial, Platform: execution.Platform},
	}
	if r.variables == nil {
		r.variables = map[string]string{}
	}
	err = r.step(ctx, execution.Script, 0)
	if errors.As(err, &failure) {
		return run.Outcome{Status: run.AttemptError, Error: "the app could not be prepared: " + failure.reason}, nil
	}
	if err != nil {
		return run.Outcome{}, err
	}
	outcome := run.Outcome{Status: run.AttemptPassed, Steps: make([]run.StepOutcome, 0, len(execution.Steps))}
	for number := 1; number <= len(execution.Steps); number++ {
		if outcome.Status == run.AttemptFailed {
			outcome.Steps = append(outcome.Steps, run.StepOutcome{Status: run.StepSkipped})
			continue
		}
		started := p.deps.Clock.Now()
		err := r.step(ctx, execution.Script, number)
		step := run.StepOutcome{Status: run.StepPassed, Duration: p.deps.Clock.Now().Sub(started)}
		switch {
		case errors.As(err, &failure):
			step.Status, step.Error = run.StepFailed, failure.reason
			outcome.Status, outcome.FailedStep, outcome.Error = run.AttemptFailed, number, failure.reason
		case err != nil:
			return run.Outcome{}, err
		}
		outcome.Steps = append(outcome.Steps, step)
	}
	return outcome, nil
}

// install puts the execution's build on the device unless this hub already installed it there.
func (p *Player) install(ctx context.Context, execution run.Execution) error {
	pkg := execution.Package
	if pkg == nil || p.wasInstalled(execution.Serial, pkg.SHA256) {
		return nil
	}
	installer, ok := p.deps.Installers[execution.Platform]
	if !ok || p.deps.Packages == nil {
		return fmt.Errorf("this hub cannot install %s builds", execution.Platform)
	}
	path, err := p.deps.Packages.Fetch(ctx, execution.Platform, *pkg)
	if err != nil {
		return fmt.Errorf("fetch build: %w", err)
	}
	if err := installer.Install(ctx, execution.Serial, path); err != nil {
		return refusal(ctx, err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.installed[execution.Serial] = pkg.SHA256
	return nil
}

func (p *Player) wasInstalled(serial, sha string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.installed[serial] == sha
}

type replay struct {
	player     *Player
	controller Controller
	target     ui.Target
	variables  map[string]string
	actions    map[string]action.Request
}

func (r replay) step(ctx context.Context, script []ui.Op, number int) error {
	for _, op := range script {
		if op.Step != number {
			continue
		}
		if err := r.op(ctx, op); err != nil {
			return err
		}
	}
	return nil
}

func (r replay) op(ctx context.Context, op ui.Op) error {
	if op.Kind == ui.KindCall {
		return r.call(ctx, op.Text)
	}
	text, err := ui.Expand(op.Text, r.variables)
	if err != nil {
		return &stepFailure{reason: err.Error()}
	}
	command := ui.Command{Target: r.target, Kind: op.Kind, Text: text}
	if op.Selector != nil || op.Kind == ui.KindSwipe {
		if command.Point, command.To, err = r.aim(ctx, op); err != nil {
			return err
		}
	}
	if op.Kind == ui.KindAssert {
		return nil
	}
	if err := r.controller.Act(ctx, command); err != nil {
		return refusal(ctx, err)
	}
	return sleep(ctx, r.player.cfg.Settle)
}

// call performs the API action named and adds the variables it extracts to the replay's.
func (r replay) call(ctx context.Context, name string) error {
	request, ok := r.actions[name]
	if !ok {
		return &stepFailure{reason: fmt.Sprintf("the project has no API action named %q", name)}
	}
	extracted, err := action.Perform(ctx, r.player.deps.Sender, request, r.variables)
	if err != nil {
		return refusal(ctx, fmt.Errorf("API action %q: %w", name, err))
	}
	maps.Copy(r.variables, extracted)
	return nil
}

// aim finds where an op acts: the centre of its element, or a swipe across the element or the screen.
func (r replay) aim(ctx context.Context, op ui.Op) (ui.Point, ui.Point, error) {
	area, err := r.find(ctx, op.Selector)
	if err != nil {
		return ui.Point{}, ui.Point{}, err
	}
	if op.Kind == ui.KindSwipe {
		return ui.SwipePoints(area, op.Direction)
	}
	return area.Center(), ui.Point{}, nil
}

// find waits for the selected element and returns where it is; without a selector it returns the screen.
// A screen that cannot be read is read again until the wait ends, as a dump often fails while the app
// animates.
func (r replay) find(ctx context.Context, selector *ui.Selector) (ui.Rect, error) {
	waitCtx, cancel := context.WithTimeout(ctx, r.player.cfg.Wait)
	defer cancel()
	missing := fmt.Sprintf("no element with %s appeared within %s", selector, r.player.cfg.Wait)
	var reason string
	for {
		screen, err := r.controller.Observe(waitCtx, r.target.Serial)
		switch {
		case ctx.Err() != nil:
			return ui.Rect{}, context.Cause(ctx)
		case err != nil:
			reason = "the screen could not be read: " + err.Error()
		case selector == nil:
			return ui.Rect{Width: screen.Width, Height: screen.Height}, nil
		default:
			if element, ok := selector.Find(screen); ok {
				return element.Bounds, nil
			}
			reason = missing
		}
		if sleep(waitCtx, r.player.cfg.Settle) != nil {
			if ctx.Err() != nil {
				return ui.Rect{}, context.Cause(ctx)
			}
			return ui.Rect{}, &stepFailure{reason: reason}
		}
	}
}

// refusal turns what a device could not do into a step failure; an execution that was stopped stays
// an error.
func refusal(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	return &stepFailure{reason: err.Error()}
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}
