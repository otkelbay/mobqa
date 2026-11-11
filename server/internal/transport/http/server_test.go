package http

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakePinger struct {
	err error
}

func (f fakePinger) Ping(context.Context) error {
	return f.err
}

type fakeHubLink struct {
	http.Handler
}

func (fakeHubLink) Shutdown(context.Context) error {
	return nil
}

func newTestServer(pingErr error, origins []string) http.Handler {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := NewServer(
		Config{Address: ":0", CORSOrigins: origins},
		Dependencies{Logger: logger, Health: fakePinger{err: pingErr}, HubLink: fakeHubLink{}},
	)
	return server.Handler()
}

func TestHealth(t *testing.T) {
	tests := []struct {
		name       string
		pingErr    error
		wantStatus int
		wantBody   string
	}{
		{name: "database reachable", wantStatus: http.StatusOK, wantBody: `{"status":"ok"}`},
		{
			name:       "database down",
			pingErr:    errors.New("connection refused"),
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   `{"status":"unavailable"}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			newTestServer(tc.pingErr, nil).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))

			if recorder.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, tc.wantStatus)
			}
			if body := strings.TrimSpace(recorder.Body.String()); body != tc.wantBody {
				t.Fatalf("body = %s, want %s", body, tc.wantBody)
			}
		})
	}
}

func TestRequestIDIsEchoedOrGenerated(t *testing.T) {
	handler := newTestServer(nil, nil)

	echoed := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set("X-Request-ID", "abc123")
	handler.ServeHTTP(echoed, request)
	if got := echoed.Header().Get("X-Request-ID"); got != "abc123" {
		t.Fatalf("echoed request id = %q, want abc123", got)
	}

	generated := httptest.NewRecorder()
	handler.ServeHTTP(generated, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if got := generated.Header().Get("X-Request-ID"); len(got) != 16 {
		t.Fatalf("generated request id = %q, want 16 hex chars", got)
	}
}

func TestCORS(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		origin     string
		wantAllow  string
		wantStatus int
	}{
		{
			name:       "allowed origin",
			method:     http.MethodGet,
			origin:     "http://localhost:5173",
			wantAllow:  "http://localhost:5173",
			wantStatus: http.StatusOK,
		},
		{name: "unknown origin", method: http.MethodGet, origin: "https://evil.example", wantStatus: http.StatusOK},
		{
			name:       "preflight",
			method:     http.MethodOptions,
			origin:     "http://localhost:5173",
			wantAllow:  "http://localhost:5173",
			wantStatus: http.StatusNoContent,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(tc.method, "/healthz", nil)
			request.Header.Set("Origin", tc.origin)
			newTestServer(nil, []string{"http://localhost:5173"}).ServeHTTP(recorder, request)

			if recorder.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, tc.wantStatus)
			}
			if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != tc.wantAllow {
				t.Fatalf("allow origin = %q, want %q", got, tc.wantAllow)
			}
		})
	}
}

func TestRecoverTurnsPanicIntoInternalError(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	panicking := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })
	handler := chain(panicking, withRecover(logger), withRequestID())

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
	if body := strings.TrimSpace(recorder.Body.String()); body != `{"error":"internal error"}` {
		t.Fatalf("body = %s", body)
	}
}
