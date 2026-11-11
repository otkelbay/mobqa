//go:build e2e

// Package e2e drives the fully wired API over HTTP against a real database. Only the device, runner and
// LLM edges are faked.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"hash/fnv"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"golang.org/x/crypto/bcrypt"

	"github.com/mobqa/backend/internal/app"
	"github.com/mobqa/backend/internal/clock/clocktest"
	"github.com/mobqa/backend/internal/config"
	"github.com/mobqa/backend/internal/dbtest"
	"github.com/mobqa/backend/internal/store/blob"
	"github.com/mobqa/backend/internal/usecase/auth"
	"github.com/mobqa/backend/internal/usecase/chat"
	"github.com/mobqa/backend/internal/usecase/knowledge"
)

type api struct {
	t      *testing.T
	app    *app.API
	url    string
	client *http.Client
	clock  *clocktest.Manual
	model  *model
	deps   app.WorkerDependencies
}

func newAPI(t *testing.T) *api {
	t.Helper()
	now := clocktest.New(time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC))
	cfg := config.API{
		TokenSecret: "e2e-secret-e2e-secret-e2e-secret",
		AccessTTL:   15 * time.Minute,
		RefreshTTL:  24 * time.Hour,
		SecretKey:   bytes.Repeat([]byte{7}, 32),
		// MaxBuildSize is small so a test can exceed it cheaply.
		MaxBuildSize: 1 << 20,
		ID:           "e2e-api", RunWorkers: 2, AuthoringWorkers: 1, Lease: time.Minute, PollInterval: 50 * time.Millisecond,
		ShutdownTimeout: time.Second, ICEServers: []string{"stun:stun.example.com:3478"},
	}
	blobs, err := blob.OpenDir(t.TempDir())
	if err != nil {
		t.Fatalf("OpenDir: %v", err)
	}
	t.Cleanup(func() { _ = blobs.Close() })
	llm := &model{}
	deps := app.WorkerDependencies{
		DB: dbtest.New(t, now), Blobs: blobs, Model: llm, Clock: now,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	built, err := app.NewAPI(cfg, app.APIDependencies{
		DB:          deps.DB,
		Blobs:       blobs,
		Model:       llm,
		Clock:       now,
		Logger:      deps.Logger,
		AuthOptions: []auth.Option{auth.WithPasswordCost(bcrypt.MinCost)},
	})
	if err != nil {
		t.Fatalf("NewAPI: %v", err)
	}
	httpServer := httptest.NewServer(built.Handler())
	t.Cleanup(httpServer.Close)
	return &api{
		t: t, app: built, url: httpServer.URL, client: httpServer.Client(), clock: now, model: llm, deps: deps,
	}
}

// startRuns runs the jobs the API hosts until the test ends.
func (a *api) startRuns() {
	a.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.app.RunJobs(ctx) }()
	a.t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			a.t.Errorf("RunJobs: %v", err)
		}
	})
}

// startWorker runs the worker's pools against the API's database until the test ends.
func (a *api) startWorker() {
	a.t.Helper()
	pools, err := app.NewPools(config.Worker{
		ID: "e2e", Lease: time.Minute, PollInterval: 50 * time.Millisecond, DrainTimeout: time.Second,
		AnalysisWorkers: 1, GenerationWorkers: 1, IndexWorkers: 1, ReviewWorkers: 1,
	}, a.deps)
	if err != nil {
		a.t.Fatalf("NewPools: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.RunPools(ctx, pools) }()
	a.t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			a.t.Errorf("RunPools: %v", err)
		}
	})
}

// model stands in for the language model: it answers chats with the queued replies, then with the reply
// set by the test, keeps every request, and embeds text as a bag of hashed words, so texts that share
// words are close.
type model struct {
	mu       sync.Mutex
	reply    string
	queued   []string
	requests []chat.Request
}

func (m *model) answer(reply string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reply = reply
}

// queue sets the replies to the next chats, in order.
func (m *model) queue(replies ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queued = replies
}

func (m *model) Chat(_ context.Context, request chat.Request) (chat.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, request)
	if len(m.queued) > 0 {
		reply := m.queued[0]
		m.queued = m.queued[1:]
		return chat.Response{Text: reply}, nil
	}
	return chat.Response{Text: m.reply}, nil
}

func (m *model) lastRequest() chat.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.requests[len(m.requests)-1]
}

func (m *model) allRequests() []chat.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]chat.Request(nil), m.requests...)
}

func (m *model) Embed(_ context.Context, texts []string) ([][]float32, error) {
	vectors := make([][]float32, len(texts))
	for i, text := range texts {
		vectors[i] = make([]float32, knowledge.Dimensions)
		for _, word := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) }) {
			hash := fnv.New32a()
			_, _ = hash.Write([]byte(word))
			vectors[i][hash.Sum32()%knowledge.Dimensions]++
		}
	}
	return vectors, nil
}

type call struct {
	method string
	path   string
	token  string
	body   any
}

// do sends the call's body as JSON, checks the status and decodes the response into result.
func (a *api) do(c call, wantStatus int, result any) {
	a.t.Helper()
	var reader io.Reader
	if c.body != nil {
		encoded, err := json.Marshal(c.body)
		if err != nil {
			a.t.Fatalf("encode body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	a.send(c, rawBody{reader: reader, contentType: "application/json"}, wantStatus, result)
}

type rawBody struct {
	reader      io.Reader
	contentType string
}

// send issues the call with a prepared body and returns the raw response payload.
func (a *api) send(c call, body rawBody, wantStatus int, result any) []byte {
	a.t.Helper()
	request, err := http.NewRequestWithContext(a.t.Context(), c.method, a.url+c.path, body.reader)
	if err != nil {
		a.t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Content-Type", body.contentType)
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	response, err := a.client.Do(request)
	if err != nil {
		a.t.Fatalf("%s %s: %v", c.method, c.path, err)
	}
	defer func() { _ = response.Body.Close() }()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		a.t.Fatalf("read response: %v", err)
	}
	if response.StatusCode != wantStatus {
		a.t.Fatalf("%s %s = %d %s, want %d", c.method, c.path, response.StatusCode, payload, wantStatus)
	}
	if result != nil {
		if err := json.Unmarshal(payload, result); err != nil {
			a.t.Fatalf("decode %s: %v", payload, err)
		}
	}
	return payload
}
