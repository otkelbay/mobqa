package authoring

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/action"
	"github.com/mobqa/backend/internal/usecase/chat"
	"github.com/mobqa/backend/internal/usecase/environment"
	"github.com/mobqa/backend/internal/usecase/knowledge"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/test"
	"github.com/mobqa/backend/internal/usecase/ui"
	"github.com/mobqa/backend/internal/worker"
)

const (
	// maxStepTurns lets the agent recover from a few missteps within one step, such as a field that
	// needed scrolling, without looping on a step it cannot do.
	maxStepTurns = 12
	maxTurns     = 150
	// turnMaxTokens fits one decision with a short thought.
	turnMaxTokens  = 500
	maxThought     = 300
	timeout        = 20 * time.Minute
	knowledgeLimit = 3000

	problemEnvironmentDeleted = "the authoring's environment was deleted"
	problemNotConfigured      = "the agent is not configured"
	problemUnreachable        = "the device's hub or the model stayed unreachable"
	problemTurns              = "the agent used all of its turns"
)

type work interface {
	// Work reports ErrNotFound when the authoring is gone.
	Work(ctx context.Context, id string) (Work, error)
	// StartAuthoring marks the authoring running and reports false when it has already finished.
	StartAuthoring(ctx context.Context, id string) (bool, error)
	// FinishAuthoring queues the jobs in the same transaction. It reports ErrConflict when the authoring
	// has already finished.
	FinishAuthoring(ctx context.Context, id string, finish Finish, jobs ...worker.EnqueueInput) error
}

type environments interface {
	Resolve(ctx context.Context, projectID, environmentID string) (environment.Environment, error)
}

type projects interface {
	Project(ctx context.Context, id string) (project.Project, error)
}

type actions interface {
	Actions(ctx context.Context, projectID string) ([]action.Action, error)
}

type retriever interface {
	Retrieve(ctx context.Context, query knowledge.Query) (knowledge.Context, error)
}

type model interface {
	Chat(ctx context.Context, request chat.Request) (chat.Response, error)
}

// Devices reaches devices through their hubs. It reports ErrUnavailable when the hub cannot be reached
// and a *ui.DeviceError when the device could not do what was asked or an API action failed.
type Devices interface {
	Observe(ctx context.Context, hubID string, target ui.Target) (ui.Screen, error)
	Act(ctx context.Context, hubID string, command ui.Command) error
	// Perform sends an API action's request from the hub and returns the variables it extracted.
	Perform(ctx context.Context, hubID string, request action.Request, variables map[string]string,
	) (map[string]string, error)
}

type locks interface {
	Lock(ctx context.Context, key string) (func(), error)
}

type AuthorConfig struct {
	// Settle is how long the app gets to react to an action before the screen is read again.
	Settle time.Duration
}

type AuthorDependencies struct {
	Store        work
	Environments environments
	Projects     projects
	Actions      actions
	Knowledge    retriever
	Model        model
	Devices      Devices
	Locks        locks
}

// Author fulfils authoring jobs.
type Author struct {
	cfg  AuthorConfig
	deps AuthorDependencies
}

func NewAuthor(cfg AuthorConfig, deps AuthorDependencies) *Author {
	return &Author{cfg: cfg, deps: deps}
}

// Run handles an authoring job. An unreachable hub or model is retried from the start until the last
// attempt; anything the agent or the device gets wrong fails the authoring with the script so far.
func (a *Author) Run(ctx context.Context, job worker.Job) error {
	var payload jobPayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return worker.Permanent(fmt.Errorf("decode authoring payload: %w", err))
	}
	w, err := a.deps.Store.Work(ctx, payload.AuthoringID)
	if errors.Is(err, fault.ErrNotFound) || (err == nil && w.Authoring.Status.Finished()) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load authoring: %w", err)
	}
	started, err := a.deps.Store.StartAuthoring(ctx, w.Authoring.ID)
	if err != nil || !started {
		return err
	}
	finish, err := a.author(ctx, w)
	switch {
	case ctx.Err() != nil:
		return context.Cause(ctx)
	case errors.Is(err, chat.ErrNotConfigured):
		finish = Finish{Status: StatusFailed, Failure: problemNotConfigured}
	case errors.Is(err, fault.ErrUnavailable) && job.Attempt < job.MaxAttempts:
		return err
	case errors.Is(err, fault.ErrUnavailable):
		finish = Finish{Status: StatusFailed, Failure: problemUnreachable}
	case err != nil:
		return err
	}
	var jobs []worker.EnqueueInput
	if len(finish.Log) > 0 {
		review, err := reviewJob(w.Authoring.ID)
		if err != nil {
			return err
		}
		jobs = append(jobs, review)
	}
	if err := a.deps.Store.FinishAuthoring(ctx, w.Authoring.ID, finish, jobs...); err != nil &&
		!errors.Is(err, fault.ErrConflict) {
		return fmt.Errorf("finish authoring: %w", err)
	}
	return nil
}

func (a *Author) author(ctx context.Context, w Work) (Finish, error) {
	app, err := a.deps.Projects.Project(ctx, w.Authoring.ProjectID)
	if err != nil {
		return Finish{}, fmt.Errorf("load project: %w", err)
	}
	variables, problem, err := a.variables(ctx, w.Authoring)
	if err != nil || problem != "" {
		return Finish{Status: StatusFailed, Failure: problem}, err
	}
	unlock, err := a.deps.Locks.Lock(ctx, w.Authoring.DeviceID)
	if err != nil {
		return Finish{}, err
	}
	defer unlock()
	known, err := a.deps.Knowledge.Retrieve(ctx, knowledge.Query{
		ProjectID: w.Authoring.ProjectID, Text: w.Title + "\n" + stepsText(w), Budget: knowledgeLimit,
	})
	if err != nil {
		return Finish{}, fmt.Errorf("retrieve knowledge: %w", err)
	}
	library, err := a.deps.Actions.Actions(ctx, w.Authoring.ProjectID)
	if err != nil {
		return Finish{}, fmt.Errorf("load actions: %w", err)
	}
	s := newSession(a, w, variables)
	s.system = systemPrompt(w.Authoring.Platform, signInBrief(app.AuthProfile, library), known.Render())
	s.app = app.PackageName
	for _, a := range library {
		s.actions[a.Name] = a.Request
	}
	sessionCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	failure, err := s.run(sessionCtx)
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		failure, err = "the authoring did not finish within "+timeout.String(), nil
	}
	if err != nil {
		return Finish{}, err
	}
	finish := Finish{Status: StatusSucceeded, Script: s.ops, Log: s.log, Knowledge: known.NoteIDs()}
	if failure != "" {
		finish.Status, finish.Failure = StatusFailed, failure
	}
	return finish, nil
}

// variables are what the agent may type: the environment's, as for a run.
func (a *Author) variables(ctx context.Context, authoring Authoring) ([]environment.Variable, string, error) {
	if authoring.EnvironmentName == "" {
		return nil, "", nil
	}
	if authoring.EnvironmentID == "" {
		return nil, problemEnvironmentDeleted, nil
	}
	env, err := a.deps.Environments.Resolve(ctx, authoring.ProjectID, authoring.EnvironmentID)
	if errors.Is(err, fault.ErrNotFound) {
		return nil, problemEnvironmentDeleted, nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("resolve environment: %w", err)
	}
	return env.Variables, "", nil
}

func stepsText(w Work) string {
	lines := make([]string, len(w.Steps))
	for i, step := range w.Steps {
		lines[i] = describeStep(step)
	}
	return strings.Join(lines, "\n")
}

// session is one pass of the agent over a test.
type session struct {
	author    *Author
	work      Work
	target    ui.Target
	app       string
	system    string
	variables map[string]string
	// hints lists the variables for the agent by name, label and rule, without their values.
	hints   []string
	actions map[string]action.Request
	ops     []ui.Op
	log     []Turn
	turns   int
}

func newSession(a *Author, w Work, variables []environment.Variable) *session {
	s := &session{
		author: a, work: w, target: ui.Target{Serial: w.Serial, Platform: w.Authoring.Platform},
		variables: map[string]string{}, actions: map[string]action.Request{}, ops: []ui.Op{}, log: []Turn{},
	}
	for _, v := range variables {
		s.variables[v.Name] = v.Value
		hint := "{{" + v.Name + "}}"
		about := slices.DeleteFunc([]string{v.Label, v.Rule}, func(part string) bool { return part == "" })
		if len(about) > 0 {
			hint += " - " + strings.Join(about, "; ")
		}
		s.hints = append(s.hints, hint)
	}
	return s
}

// run starts the app afresh and works through the steps. A failure is the reason the test could not be
// scripted; an error is a problem reaching the hub or the model.
func (s *session) run(ctx context.Context) (string, error) {
	if s.app != "" {
		for _, kind := range []ui.Kind{ui.KindStop, ui.KindLaunch} {
			command := ui.Command{Target: s.target, Kind: kind, Text: s.app}
			if err := s.author.deps.Devices.Act(ctx, s.work.HubID, command); err != nil {
				return refusal(err, "start the app")
			}
			s.ops = append(s.ops, ui.Op{Kind: kind, Text: s.app})
		}
		if err := sleep(ctx, s.author.cfg.Settle); err != nil {
			return "", err
		}
	}
	for i := range s.work.Steps {
		step := s.step
		if s.work.Steps[i].Action == test.ActionCall {
			step = s.call
		}
		failure, err := step(ctx, i+1)
		if err != nil || failure != "" {
			return failure, err
		}
	}
	return "", nil
}

type progress int

const (
	going progress = iota
	done
	failed
)

func (s *session) step(ctx context.Context, number int) (string, error) {
	first := len(s.log)
	for range maxStepTurns {
		if s.turns == maxTurns {
			return problemTurns, nil
		}
		s.turns++
		screen, err := s.author.deps.Devices.Observe(ctx, s.work.HubID, s.target)
		if err != nil {
			return refusal(err, "read the screen")
		}
		recent := s.log[max(first, len(s.log)-recentTurns):]
		turn, state, err := s.turn(ctx, number, screen, recent)
		if err != nil {
			return "", err
		}
		s.log = append(s.log, turn)
		switch state {
		case done:
			return "", nil
		case failed:
			return fmt.Sprintf("step %d: %s", number, cmp.Or(turn.Text, "the agent gave up")), nil
		}
	}
	return fmt.Sprintf("step %d was not finished within %d turns", number, maxStepTurns), nil
}

// call performs the API action a call step names, without the model: there is nothing on the screen to
// decide. The variables it extracts are the agent's to type from then on, and are redacted like the rest.
func (s *session) call(ctx context.Context, number int) (string, error) {
	name := s.work.Steps[number-1].Target
	turn := Turn{Step: number, Action: string(test.ActionCall), Text: name}
	request, ok := s.actions[name]
	if !ok {
		turn.Error = "the project has no API action with this name"
		s.log = append(s.log, turn)
		return fmt.Sprintf("step %d: the project has no API action named %q", number, name), nil
	}
	extracted, err := s.author.deps.Devices.Perform(ctx, s.work.HubID, request, s.variables)
	var refused *ui.DeviceError
	if errors.As(err, &refused) {
		turn.Error = refused.Message
		s.log = append(s.log, turn)
		return fmt.Sprintf("step %d: API action %q failed: %s", number, name, refused.Message), nil
	}
	if err != nil {
		return "", err
	}
	for _, variable := range slices.Sorted(maps.Keys(extracted)) {
		if _, known := s.variables[variable]; !known {
			s.hints = append(s.hints, fmt.Sprintf("{{%s}} - extracted by the API action %s", variable, name))
		}
		s.variables[variable] = extracted[variable]
	}
	s.ops = append(s.ops, ui.Op{Step: number, Kind: ui.KindCall, Text: name})
	s.log = append(s.log, turn)
	return "", nil
}

func (s *session) turn(ctx context.Context, number int, screen ui.Screen, recent []Turn) (Turn, progress, error) {
	response, err := s.author.deps.Model.Chat(ctx, chat.Request{
		Messages: []chat.Message{
			{Role: chat.RoleSystem, Text: s.system},
			{Role: chat.RoleUser, Text: s.prompt(number, recent, screen)},
		},
		MaxTokens: turnMaxTokens, JSON: true,
	})
	if err != nil {
		return Turn{}, going, fmt.Errorf("ask the model: %w", err)
	}
	d, err := parseDecision(response, screen)
	if errors.Is(err, errBadDecision) {
		return Turn{Step: number, Action: "reply", Error: err.Error()}, going, nil
	}
	if err != nil {
		return Turn{}, going, err
	}
	turn := Turn{Step: number, Action: d.Action, Text: d.Text, Thought: clip(d.Thought)}
	switch d.Action {
	case actionDone:
		return turn, done, nil
	case actionFail:
		return turn, failed, nil
	case actionWait:
		return turn, going, sleep(ctx, s.author.cfg.Settle)
	}
	err = s.perform(ctx, d, screen, &turn)
	var wrong *misstep
	if errors.As(err, &wrong) {
		turn.Error = wrong.reason
		return turn, going, nil
	}
	if err != nil || ui.Kind(d.Action) == ui.KindAssert {
		return turn, going, err
	}
	return turn, going, sleep(ctx, s.author.cfg.Settle)
}

// misstep is a mistake the agent can correct, such as an unknown variable or a command the device
// refused; the agent sees it next turn.
type misstep struct {
	reason string
}

func (m *misstep) Error() string {
	return m.reason
}

// perform carries out a decision for turn.Step and records its ops.
func (s *session) perform(ctx context.Context, d decision, screen ui.Screen, turn *Turn) error {
	text, err := ui.Expand(d.Text, s.variables)
	if err != nil {
		return &misstep{reason: err.Error()}
	}
	op := ui.Op{Step: turn.Step, Kind: ui.Kind(d.Action), Text: d.Text}
	area := ui.Rect{Width: screen.Width, Height: screen.Height}
	if d.Element != nil {
		area = screen.Elements[*d.Element].Bounds
		selector := ui.SelectorFor(screen, screen.Elements[*d.Element])
		op.Selector, turn.Element = &selector, selector.String()
	}
	command := ui.Command{Target: s.target, Kind: op.Kind, Point: area.Center(), Text: text}
	switch op.Kind {
	case ui.KindAssert:
		s.ops = append(s.ops, op)
		return nil
	case ui.KindSwipe:
		op.Direction = ui.Direction(d.Direction)
		if command.Point, command.To, err = ui.SwipePoints(area, op.Direction); err != nil {
			return &misstep{reason: "direction must be up, down, left or right"}
		}
	case ui.KindType, ui.KindClear:
		tap := ui.Command{Target: s.target, Kind: ui.KindTap, Point: command.Point}
		if err := s.do(ctx, tap, ui.Op{Step: turn.Step, Kind: ui.KindTap, Selector: op.Selector}); err != nil {
			return err
		}
		op.Selector = nil
	}
	return s.do(ctx, command, op)
}

// do sends a command to the device and records op once the device has carried it out. A command the
// device refused is a misstep.
func (s *session) do(ctx context.Context, command ui.Command, op ui.Op) error {
	err := s.author.deps.Devices.Act(ctx, s.work.HubID, command)
	var refused *ui.DeviceError
	if errors.As(err, &refused) {
		return &misstep{reason: refused.Message}
	}
	if err != nil {
		return err
	}
	s.ops = append(s.ops, op)
	return nil
}

// refusal turns a device's refusal into the reason the authoring failed and passes other errors on.
func refusal(err error, doing string) (string, error) {
	var refused *ui.DeviceError
	if errors.As(err, &refused) {
		return fmt.Sprintf("the device could not %s: %s", doing, refused.Message), nil
	}
	return "", err
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func clip(text string) string {
	text = strings.TrimSpace(text)
	if utf8.RuneCountInString(text) <= maxThought {
		return text
	}
	return string([]rune(text)[:maxThought])
}
