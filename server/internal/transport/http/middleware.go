package http

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"time"
)

const requestIDHeader = "X-Request-ID"

type middleware func(http.Handler) http.Handler

type requestIDKey struct{}

// chain applies middlewares so that the last one listed runs first.
func chain(handler http.Handler, middlewares ...middleware) http.Handler {
	for _, m := range middlewares {
		handler = m(handler)
	}
	return handler
}

func withRequestID() middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(requestIDHeader)
			if id == "" {
				id = newRequestID()
			}
			w.Header().Set(requestIDHeader, id)
			ctx := context.WithValue(r.Context(), requestIDKey{}, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func requestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

func newRequestID() string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

func withRecover(logger *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer recoverPanic(r.Context(), w, logger)
			next.ServeHTTP(w, r)
		})
	}
}

func recoverPanic(ctx context.Context, w http.ResponseWriter, logger *slog.Logger) {
	if recovered := recover(); recovered == nil {
		return
	}
	logger.ErrorContext(ctx, "handler panicked", "request_id", requestID(ctx), "code", "panic")
	writeError(w, http.StatusInternalServerError, "internal error")
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(status int) {
	s.status = status
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter {
	return s.ResponseWriter
}

// Hijack lets a WebSocket upgrade take over the connection while the request is still access-logged.
func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(s.ResponseWriter).Hijack()
	if err != nil {
		return nil, nil, fmt.Errorf("hijack: %w", err)
	}
	s.status = http.StatusSwitchingProtocols
	return conn, rw, nil
}

func withAccessLog(logger *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(recorder, r)
			logger.Info("http request",
				"request_id", requestID(r.Context()),
				"method", r.Method,
				"route", r.Pattern,
				"status", recorder.status,
				"duration_ms", time.Since(started).Milliseconds(),
			)
		})
	}
}

func withCORS(origins []string) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && slices.Contains(origins, origin) {
				header := w.Header()
				header.Set("Access-Control-Allow-Origin", origin)
				header.Set("Access-Control-Allow-Credentials", "true")
				header.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, "+requestIDHeader)
				header.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				header.Add("Vary", "Origin")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
