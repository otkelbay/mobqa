// Package http is the REST transport. It owns request and response types, decodes and validates
// them, calls exactly one usecase method per request and maps results and errors back.
package http

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/mobqa/backend/internal/transport/hubws"
)

const (
	readHeaderTimeout = 10 * time.Second
	// idleTimeout keeps browser keep-alive connections without holding sockets for idle tabs.
	idleTimeout = 120 * time.Second
)

// Pinger reports whether the storage behind the API is reachable.
type Pinger interface {
	Ping(ctx context.Context) error
}

// HubLink accepts hub connections, which outlive ordinary requests and so are shut down separately.
type HubLink interface {
	http.Handler
	Shutdown(ctx context.Context) error
}

type Config struct {
	Address     string
	CORSOrigins []string
	// ICEServers are the STUN and TURN URLs browsers use to reach the hubs that stream device screens.
	ICEServers []string
}

type Server struct {
	server  *http.Server
	hubLink HubLink
	logger  *slog.Logger
}

type Dependencies struct {
	Logger       *slog.Logger
	Health       Pinger
	Sessions     Sessions
	Profiles     Profiles
	Projects     Projects
	Environments Environments
	Actions      Actions
	Builds       Builds
	Tests        Tests
	Editor       Editor
	Knowledge    Knowledge
	Runs         Runs
	Videos       Videos
	Authorings   Authorings
	Hubs         Hubs
	HubTokens    HubTokens
	Devices      Devices
	Control      Control
	Streams      Streams
	HubLink      HubLink
}

func NewServer(cfg Config, deps Dependencies) *Server {
	handler := chain(routes(cfg, deps),
		withCORS(cfg.CORSOrigins),
		withAccessLog(deps.Logger),
		withRecover(deps.Logger),
		withRequestID(),
	)
	return &Server{
		server: &http.Server{
			Addr:              cfg.Address,
			Handler:           handler,
			ReadHeaderTimeout: readHeaderTimeout,
			IdleTimeout:       idleTimeout,
		},
		hubLink: deps.HubLink,
		logger:  deps.Logger,
	}
}

func routes(cfg Config, deps Dependencies) *http.ServeMux {
	mux := http.NewServeMux()
	health := healthHandler{pinger: deps.Health}
	mux.HandleFunc("GET /healthz", health.serve)
	mux.Handle("GET "+hubws.Path, deps.HubLink)

	auth := authHandler{sessions: deps.Sessions, logger: deps.Logger}
	mux.HandleFunc("POST /auth/register", auth.register)
	mux.HandleFunc("POST /auth/login", auth.login)
	mux.HandleFunc("POST /auth/refresh", auth.refresh)
	mux.HandleFunc("POST /auth/logout", auth.logout)

	users := userHandler{profiles: deps.Profiles, logger: deps.Logger}
	mux.Handle("GET /user/profile", auth.authenticated(users.profile))
	mux.Handle("PATCH /user/profile", auth.authenticated(users.updateProfile))

	projects := projectHandler{projects: deps.Projects, logger: deps.Logger}
	mux.Handle("GET /projects", auth.authenticated(projects.list))
	mux.Handle("POST /projects", auth.authenticated(projects.create))
	mux.Handle("GET /projects/{projectID}", auth.authenticated(projects.get))
	mux.Handle("PATCH /projects/{projectID}", auth.authenticated(projects.update))
	mux.Handle("DELETE /projects/{projectID}", auth.authenticated(projects.archive))
	mux.Handle("GET /projects/{projectID}/auth-profile", auth.authenticated(projects.authProfile))
	mux.Handle("PUT /projects/{projectID}/auth-profile", auth.authenticated(projects.setAuthProfile))
	mux.Handle("POST /projects/{projectID}/members", auth.authenticated(projects.addMember))
	mux.Handle("PATCH /projects/{projectID}/members/{memberID}", auth.authenticated(projects.setMemberRole))
	mux.Handle("DELETE /projects/{projectID}/members/{memberID}", auth.authenticated(projects.removeMember))

	environments := environmentHandler{environments: deps.Environments, logger: deps.Logger}
	mux.Handle("GET /projects/{projectID}/environments", auth.authenticated(environments.list))
	mux.Handle("POST /projects/{projectID}/environments", auth.authenticated(environments.create))
	mux.Handle("PATCH /projects/{projectID}/environments/{environmentID}", auth.authenticated(environments.update))
	mux.Handle("DELETE /projects/{projectID}/environments/{environmentID}", auth.authenticated(environments.remove))

	actions := actionHandler{actions: deps.Actions, logger: deps.Logger}
	mux.Handle("GET /projects/{projectID}/actions", auth.authenticated(actions.list))
	mux.Handle("POST /projects/{projectID}/actions", auth.authenticated(actions.create))
	mux.Handle("GET /projects/{projectID}/actions/{actionID}", auth.authenticated(actions.get))
	mux.Handle("PUT /projects/{projectID}/actions/{actionID}", auth.authenticated(actions.replace))
	mux.Handle("DELETE /projects/{projectID}/actions/{actionID}", auth.authenticated(actions.remove))

	builds := buildHandler{builds: deps.Builds, hubs: deps.HubTokens, logger: deps.Logger}
	mux.HandleFunc("GET "+hubws.BuildsPath+"{buildID}", builds.hubDownload)
	mux.Handle("GET /projects/{projectID}/builds", auth.authenticated(builds.list))
	mux.Handle("POST /projects/{projectID}/builds", auth.authenticated(builds.upload))
	mux.Handle("GET /projects/{projectID}/builds/{buildID}", auth.authenticated(builds.get))
	mux.Handle("GET /projects/{projectID}/builds/{buildID}/download", auth.authenticated(builds.download))
	mux.Handle("POST /projects/{projectID}/builds/{buildID}/current", auth.authenticated(builds.setCurrent))
	mux.Handle("DELETE /projects/{projectID}/builds/{buildID}", auth.authenticated(builds.remove))

	testRoutes(mux, auth, deps)
	knowledgeRoutes(mux, auth, deps)
	runRoutes(mux, auth, deps)
	authoringRoutes(mux, auth, deps)
	hubRoutes(mux, auth, deps)
	deviceRoutes(mux, auth, cfg, deps)
	return mux
}

func knowledgeRoutes(mux *http.ServeMux, auth authHandler, deps Dependencies) {
	notes := knowledgeHandler{knowledge: deps.Knowledge, logger: deps.Logger}
	mux.Handle("GET /projects/{projectID}/knowledge", auth.authenticated(notes.list))
	mux.Handle("POST /projects/{projectID}/knowledge", auth.authenticated(notes.create))
	mux.Handle("GET /projects/{projectID}/knowledge/search", auth.authenticated(notes.search))
	mux.Handle("GET /projects/{projectID}/knowledge/{noteID}", auth.authenticated(notes.get))
	mux.Handle("PATCH /projects/{projectID}/knowledge/{noteID}", auth.authenticated(notes.update))
	mux.Handle("DELETE /projects/{projectID}/knowledge/{noteID}", auth.authenticated(notes.remove))
}

func runRoutes(mux *http.ServeMux, auth authHandler, deps Dependencies) {
	runs := runHandler{runs: deps.Runs, videos: deps.Videos, hubs: deps.HubTokens, logger: deps.Logger}
	mux.HandleFunc("PUT "+hubws.VideosPath+"{resultID}/{number}", runs.saveVideo)
	mux.Handle("GET /projects/{projectID}/runs", auth.authenticated(runs.list))
	mux.Handle("POST /projects/{projectID}/runs", auth.authenticated(runs.create))
	mux.Handle("GET /projects/{projectID}/runs/{runID}", auth.authenticated(runs.get))
	mux.Handle("POST /projects/{projectID}/runs/{runID}/cancel", auth.authenticated(runs.cancel))
	mux.Handle("GET /projects/{projectID}/runs/{runID}/results/{resultID}/attempts",
		auth.authenticated(runs.attempts))
	mux.Handle("GET /projects/{projectID}/runs/{runID}/results/{resultID}/attempts/{number}/video",
		auth.authenticated(runs.video))
}

func authoringRoutes(mux *http.ServeMux, auth authHandler, deps Dependencies) {
	authorings := authoringHandler{authorings: deps.Authorings, logger: deps.Logger}
	mux.Handle("GET /projects/{projectID}/tests/{testID}/authorings", auth.authenticated(authorings.list))
	mux.Handle("POST /projects/{projectID}/tests/{testID}/authorings", auth.authenticated(authorings.start))
	mux.Handle("GET /projects/{projectID}/authorings/{authoringID}", auth.authenticated(authorings.get))
	mux.Handle("POST /projects/{projectID}/authorings/{authoringID}/cancel", auth.authenticated(authorings.cancel))
}

func testRoutes(mux *http.ServeMux, auth authHandler, deps Dependencies) {
	tests := testHandler{tests: deps.Tests, editor: deps.Editor, logger: deps.Logger}
	mux.Handle("GET /projects/{projectID}/tests", auth.authenticated(tests.list))
	mux.Handle("POST /projects/{projectID}/tests", auth.authenticated(tests.create))
	mux.Handle("GET /projects/{projectID}/tests/{testID}", auth.authenticated(tests.get))
	mux.Handle("PATCH /projects/{projectID}/tests/{testID}", auth.authenticated(tests.update))
	mux.Handle("DELETE /projects/{projectID}/tests/{testID}", auth.authenticated(tests.remove))
	mux.Handle("POST /projects/{projectID}/tests/{testID}/duplicate", auth.authenticated(tests.duplicate))
	mux.Handle("GET /projects/{projectID}/tests/{testID}/versions", auth.authenticated(tests.versions))
	mux.Handle("GET /projects/{projectID}/tests/{testID}/versions/{version}", auth.authenticated(tests.version))
	mux.Handle("POST /projects/{projectID}/tests/{testID}/versions/{version}/restore",
		auth.authenticated(tests.restore))
	mux.Handle("POST /projects/{projectID}/tests/{testID}/agent-edit", auth.authenticated(tests.agentEdit))
	mux.Handle("POST /projects/{projectID}/test-generations", auth.authenticated(tests.generate))
	mux.Handle("GET /projects/{projectID}/test-generations/{generationID}", auth.authenticated(tests.generation))
}

func hubRoutes(mux *http.ServeMux, auth authHandler, deps Dependencies) {
	hubs := hubHandler{hubs: deps.Hubs, logger: deps.Logger}
	mux.Handle("GET /projects/{projectID}/hubs", auth.authenticated(hubs.list))
	mux.Handle("POST /projects/{projectID}/hubs", auth.authenticated(hubs.register))
	mux.Handle("POST /projects/{projectID}/hubs/{hubID}/token", auth.authenticated(hubs.rotateToken))
	mux.Handle("DELETE /projects/{projectID}/hubs/{hubID}", auth.authenticated(hubs.remove))
}

func deviceRoutes(mux *http.ServeMux, auth authHandler, cfg Config, deps Dependencies) {
	devices := deviceHandler{
		devices: deps.Devices, control: deps.Control, streams: deps.Streams, iceServers: cfg.ICEServers,
		logger: deps.Logger,
	}
	mux.HandleFunc("POST /streams/publish", devices.publish)
	mux.Handle("GET /ice-servers", auth.authenticated(devices.listICEServers))
	mux.Handle("GET /projects/{projectID}/devices", auth.authenticated(devices.list))
	mux.Handle("GET /projects/{projectID}/devices/{deviceID}", auth.authenticated(devices.get))
	mux.Handle("GET /projects/{projectID}/devices/{deviceID}/screen", auth.authenticated(devices.screen))
	mux.Handle("POST /projects/{projectID}/devices/{deviceID}/commands", auth.authenticated(devices.act))
	mux.Handle("POST /projects/{projectID}/devices/{deviceID}/stream", auth.authenticated(devices.watch))
	mux.Handle("POST /projects/{projectID}/devices/{deviceID}/stream-key", auth.authenticated(devices.rotateStreamKey))
}

func (s *Server) Handler() http.Handler {
	return s.server.Handler
}

// Run serves until ctx ends, then shuts down gracefully within shutdownTimeout.
func (s *Server) Run(ctx context.Context, shutdownTimeout time.Duration) error {
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- s.server.ListenAndServe()
	}()
	s.logger.Info("http server started", "address", s.server.Addr)

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	// The request context is already cancelled; shutdown needs its own deadline.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	// Hijacked hub connections are invisible to http.Server.Shutdown, so they are closed explicitly.
	err := errors.Join(s.server.Shutdown(shutdownCtx), s.hubLink.Shutdown(shutdownCtx))
	if err != nil {
		return err
	}
	<-serveErr
	return nil
}
