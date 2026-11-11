// Package action keeps a project's API actions: HTTP requests that tests call to prepare data or to read
// what the app sent, such as a one-time code. Actions are templates; {{NAME}} placeholders are filled from
// the run's variables, so secrets stay in environments. Hubs perform the requests, next to the app's
// backend, never the API.
package action

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/ui"
)

type Method string

const (
	MethodGet    Method = "GET"
	MethodPost   Method = "POST"
	MethodPut    Method = "PUT"
	MethodPatch  Method = "PATCH"
	MethodDelete Method = "DELETE"
)

const (
	maxNameLength        = 60
	maxDescriptionLength = 500
	maxURLLength         = 2000
	maxHeaders           = 20
	maxHeaderValue       = 1000
	maxBodyLength        = 20000
	maxExtractions       = 5
	maxPatternLength     = 500
	defaultStatus        = 200
)

var headerName = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]+$")

type Header struct {
	Name  string
	Value string
}

// Extraction keeps part of the response as a variable for the rest of the test: the first match of
// Pattern's only capturing group in the response body.
type Extraction struct {
	Variable string
	Pattern  string
}

// Request is what an action sends and what it expects back.
type Request struct {
	Method         Method
	URL            string
	Headers        []Header
	Body           string
	ExpectedStatus int
	Extract        []Extraction
}

type Action struct {
	ID          string
	ProjectID   string
	Name        string
	Description string
	Request     Request
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Definition is everything about an action that people write.
type Definition struct {
	Name        string
	Description string
	Request     Request
}

func (d Definition) validate() error {
	if d.Name == "" || strings.TrimSpace(d.Name) != d.Name || utf8.RuneCountInString(d.Name) > maxNameLength {
		return fault.WithDetail(fault.ErrInvalidInput, "name must be 1 to 60 characters without surrounding spaces")
	}
	if utf8.RuneCountInString(d.Description) > maxDescriptionLength {
		return fault.WithDetail(fault.ErrInvalidInput, "description must be at most 500 characters")
	}
	return d.Request.validate()
}

func (r Request) validate() error {
	if !slices.Contains([]Method{MethodGet, MethodPost, MethodPut, MethodPatch, MethodDelete}, r.Method) {
		return fault.WithDetail(fault.ErrInvalidInput, "method must be GET, POST, PUT, PATCH or DELETE")
	}
	if len(r.URL) > maxURLLength || (!strings.HasPrefix(r.URL, "https://") && !strings.HasPrefix(r.URL, "http://") &&
		!strings.HasPrefix(r.URL, "{{")) {
		return fault.WithDetail(fault.ErrInvalidInput,
			"url must start with http://, https:// or a {{VARIABLE}} holding the base URL, within 2000 characters")
	}
	if len(r.Headers) > maxHeaders {
		return fault.WithDetail(fault.ErrInvalidInput, "an action sends at most 20 headers")
	}
	for _, header := range r.Headers {
		if !headerName.MatchString(header.Name) || len(header.Value) > maxHeaderValue ||
			strings.ContainsAny(header.Value, "\r\n") {
			return fault.WithDetail(fault.ErrInvalidInput, "headers need a valid name and a one-line value of at "+
				"most 1000 bytes")
		}
	}
	if len(r.Body) > maxBodyLength {
		return fault.WithDetail(fault.ErrInvalidInput, "body must be at most 20000 bytes")
	}
	if r.ExpectedStatus < 100 || r.ExpectedStatus > 599 {
		return fault.WithDetail(fault.ErrInvalidInput, "expected status must be an HTTP status code")
	}
	return validateExtractions(r.Extract)
}

func validateExtractions(extractions []Extraction) error {
	if len(extractions) > maxExtractions {
		return fault.WithDetail(fault.ErrInvalidInput, "an action extracts at most 5 variables")
	}
	for i, extraction := range extractions {
		if !project.ValidVariableName(extraction.Variable) {
			return fault.WithDetail(fault.ErrInvalidInput, "variable names must be letters, digits and underscores")
		}
		if slices.ContainsFunc(extractions[:i], func(other Extraction) bool {
			return other.Variable == extraction.Variable
		}) {
			return fault.WithDetail(fault.ErrInvalidInput, "an action extracts each variable once")
		}
		pattern, err := regexp.Compile(extraction.Pattern)
		if err != nil || len(extraction.Pattern) > maxPatternLength || pattern.NumSubexp() != 1 {
			return fault.WithDetail(fault.ErrInvalidInput,
				"a pattern must be a regular expression of at most 500 bytes with exactly one capturing group")
		}
	}
	return nil
}

// normalized trims the description and fills in the default expected status.
func (d Definition) normalized() Definition {
	d.Description = strings.TrimSpace(d.Description)
	if d.Request.ExpectedStatus == 0 {
		d.Request.ExpectedStatus = defaultStatus
	}
	if d.Request.Headers == nil {
		d.Request.Headers = []Header{}
	}
	if d.Request.Extract == nil {
		d.Request.Extract = []Extraction{}
	}
	return d
}

// store reports ErrConflict when the name is taken in the project and ErrNotFound for an action outside
// it. Listings are ordered by name.
type store interface {
	Actions(ctx context.Context, projectID string) ([]Action, error)
	Action(ctx context.Context, projectID, id string) (Action, error)
	CreateAction(ctx context.Context, projectID string, definition Definition) (Action, error)
	UpdateAction(ctx context.Context, projectID, id string, definition Definition) (Action, error)
	DeleteAction(ctx context.Context, projectID, id string) error
}

type access interface {
	Require(ctx context.Context, projectID, userID string, need project.Role) (project.Role, error)
}

type Service struct {
	store  store
	access access
}

func NewService(actions store, projects access) *Service {
	return &Service{store: actions, access: projects}
}

func (s *Service) List(ctx context.Context, userID, projectID string) ([]Action, error) {
	if _, err := s.access.Require(ctx, projectID, userID, project.RoleViewer); err != nil {
		return nil, err
	}
	actions, err := s.store.Actions(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list actions: %w", err)
	}
	return actions, nil
}

type Ref struct {
	UserID    string
	ProjectID string
	ActionID  string
}

func (s *Service) Get(ctx context.Context, ref Ref) (Action, error) {
	if _, err := s.access.Require(ctx, ref.ProjectID, ref.UserID, project.RoleViewer); err != nil {
		return Action{}, err
	}
	found, err := s.store.Action(ctx, ref.ProjectID, ref.ActionID)
	if errors.Is(err, fault.ErrNotFound) {
		return Action{}, fault.WithDetail(fault.ErrNotFound, "action not found")
	}
	if err != nil {
		return Action{}, fmt.Errorf("load action: %w", err)
	}
	return found, nil
}

type CreateInput struct {
	UserID     string
	ProjectID  string
	Definition Definition
}

func (in CreateInput) Validate() error {
	return in.Definition.normalized().validate()
}

func (s *Service) Create(ctx context.Context, in CreateInput) (Action, error) {
	if err := in.Validate(); err != nil {
		return Action{}, err
	}
	if _, err := s.access.Require(ctx, in.ProjectID, in.UserID, project.RoleEngineer); err != nil {
		return Action{}, err
	}
	created, err := s.store.CreateAction(ctx, in.ProjectID, in.Definition.normalized())
	if errors.Is(err, fault.ErrConflict) {
		return Action{}, fault.WithDetail(fault.ErrConflict, "another action already has this name")
	}
	if err != nil {
		return Action{}, fmt.Errorf("create action: %w", err)
	}
	return created, nil
}

type ReplaceInput struct {
	Ref        Ref
	Definition Definition
}

func (in ReplaceInput) Validate() error {
	return in.Definition.normalized().validate()
}

// Replace swaps the whole definition of an action; tests that call it by name use the new one.
func (s *Service) Replace(ctx context.Context, in ReplaceInput) (Action, error) {
	if err := in.Validate(); err != nil {
		return Action{}, err
	}
	if _, err := s.access.Require(ctx, in.Ref.ProjectID, in.Ref.UserID, project.RoleEngineer); err != nil {
		return Action{}, err
	}
	updated, err := s.store.UpdateAction(ctx, in.Ref.ProjectID, in.Ref.ActionID, in.Definition.normalized())
	switch {
	case errors.Is(err, fault.ErrConflict):
		return Action{}, fault.WithDetail(fault.ErrConflict, "another action already has this name")
	case errors.Is(err, fault.ErrNotFound):
		return Action{}, fault.WithDetail(fault.ErrNotFound, "action not found")
	case err != nil:
		return Action{}, fmt.Errorf("update action: %w", err)
	}
	return updated, nil
}

func (s *Service) Delete(ctx context.Context, ref Ref) error {
	if _, err := s.access.Require(ctx, ref.ProjectID, ref.UserID, project.RoleEngineer); err != nil {
		return err
	}
	err := s.store.DeleteAction(ctx, ref.ProjectID, ref.ActionID)
	if errors.Is(err, fault.ErrNotFound) {
		return fault.WithDetail(fault.ErrNotFound, "action not found")
	}
	if err != nil {
		return fmt.Errorf("delete action: %w", err)
	}
	return nil
}

// Call is a request with its variables filled in, ready to send.
type Call struct {
	Method  Method
	URL     string
	Headers []Header
	Body    string
}

// Reply is what came back for a call, whatever its status.
type Reply struct {
	Status int
	Body   string
}

// Sender sends calls over HTTP.
type Sender interface {
	Send(ctx context.Context, call Call) (Reply, error)
}

// Perform fills the request's placeholders from variables, sends it and returns the variables it
// extracts from the reply. Every error is the reason the call failed, worded for the person reading the
// result.
func Perform(ctx context.Context, sender Sender, request Request, variables map[string]string,
) (map[string]string, error) {
	call := Call{Method: request.Method, Headers: make([]Header, len(request.Headers))}
	var err error
	if call.URL, err = expandURL(request.URL, variables); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(call.URL, "https://") && !strings.HasPrefix(call.URL, "http://") {
		return nil, errors.New("the url is not http or https once its variables are filled in")
	}
	for i, header := range request.Headers {
		value, err := ui.Expand(header.Value, variables)
		if err != nil {
			return nil, err
		}
		call.Headers[i] = Header{Name: header.Name, Value: value}
	}
	if call.Body, err = ui.Expand(request.Body, variables); err != nil {
		return nil, err
	}
	reply, err := sender.Send(ctx, call)
	if err != nil {
		return nil, err
	}
	if reply.Status != request.ExpectedStatus {
		return nil, fmt.Errorf("expected status %d, got %d", request.ExpectedStatus, reply.Status)
	}
	extracted := make(map[string]string, len(request.Extract))
	for _, extraction := range request.Extract {
		pattern, err := regexp.Compile(extraction.Pattern)
		if err != nil {
			return nil, fmt.Errorf("the pattern for %s: %w", extraction.Variable, err)
		}
		match := pattern.FindStringSubmatch(reply.Body)
		if match == nil {
			return nil, fmt.Errorf("the reply has nothing that matches the pattern for %s", extraction.Variable)
		}
		extracted[extraction.Variable] = match[1]
	}
	return extracted, nil
}

// expandURL fills a url's placeholders with percent-encoded values, so that a phone number keeps its plus
// sign in a query. A leading placeholder holds the base URL and is filled as it is.
func expandURL(template string, variables map[string]string) (string, error) {
	var base string
	if end := strings.Index(template, "}}"); strings.HasPrefix(template, "{{") && end > 0 {
		base, template = template[:end+2], template[end+2:]
	}
	head, err := ui.Expand(base, variables)
	if err != nil {
		return "", err
	}
	escaped := make(map[string]string, len(variables))
	for name, value := range variables {
		escaped[name] = strings.ReplaceAll(url.QueryEscape(value), "+", "%20")
	}
	tail, err := ui.Expand(template, escaped)
	if err != nil {
		return "", err
	}
	return head + tail, nil
}
