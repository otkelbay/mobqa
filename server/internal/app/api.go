// Package app wires components together and owns their lifecycle. It holds no business logic.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/mobqa/backend/internal/clock"
	"github.com/mobqa/backend/internal/config"
	"github.com/mobqa/backend/internal/secret"
	"github.com/mobqa/backend/internal/store/blob"
	"github.com/mobqa/backend/internal/store/postgres"
	transporthttp "github.com/mobqa/backend/internal/transport/http"
	"github.com/mobqa/backend/internal/transport/hubws"
	"github.com/mobqa/backend/internal/usecase/action"
	"github.com/mobqa/backend/internal/usecase/auth"
	"github.com/mobqa/backend/internal/usecase/authoring"
	"github.com/mobqa/backend/internal/usecase/build"
	"github.com/mobqa/backend/internal/usecase/device"
	"github.com/mobqa/backend/internal/usecase/environment"
	"github.com/mobqa/backend/internal/usecase/hub"
	"github.com/mobqa/backend/internal/usecase/knowledge"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/run"
	"github.com/mobqa/backend/internal/usecase/test"
	"github.com/mobqa/backend/internal/usecase/user"
	"github.com/mobqa/backend/internal/worker"
)

// RunAPI serves the API until ctx ends. The database is closed only after the server has stopped.
func RunAPI(ctx context.Context, cfg config.API, logger *slog.Logger) error {
	now := clock.System{}
	db, err := postgres.Open(ctx, cfg.DatabaseURL, postgres.Options{Clock: now})
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	if err := db.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}
	blobs, err := blob.OpenDir(cfg.BlobDir)
	if err != nil {
		return err
	}
	defer func() { _ = blobs.Close() }()
	model, err := newModel(cfg.LLM)
	if err != nil {
		return err
	}
	server, err := NewAPI(cfg, APIDependencies{DB: db, Blobs: blobs, Model: model, Clock: now, Logger: logger})
	if err != nil {
		return err
	}
	return server.Run(ctx, cfg.ShutdownTimeout)
}

type APIDependencies struct {
	DB     *postgres.DB
	Blobs  *blob.Dir
	Model  Model
	Clock  clock.Clock
	Logger *slog.Logger
	// AuthOptions tune the auth usecase; tests lower the password hashing cost.
	AuthOptions []auth.Option
}

// API is the HTTP server together with the jobs that need its hub connections: only the process a hub is
// connected to can drive that hub's devices.
type API struct {
	server *transporthttp.Server
	pools  []*worker.Pool
}

func (a *API) Handler() http.Handler {
	return a.server.Handler()
}

// RunJobs runs the jobs the API hosts until ctx ends and they have drained.
func (a *API) RunJobs(ctx context.Context) error {
	return RunPools(ctx, a.pools)
}

// Run serves HTTP and runs the hosted jobs until ctx ends. The jobs drain before the server stops,
// because the hub connections they use close with it.
func (a *API) Run(ctx context.Context, shutdownTimeout time.Duration) error {
	serverCtx, stopServer := context.WithCancel(context.WithoutCancel(ctx))
	defer stopServer()
	jobsCtx, stopJobs := context.WithCancel(ctx)
	defer stopJobs()
	jobsDone := make(chan error, 1)
	go func() {
		err := a.RunJobs(jobsCtx)
		stopServer()
		jobsDone <- err
	}()
	serveErr := a.server.Run(serverCtx, shutdownTimeout)
	stopJobs()
	return errors.Join(serveErr, <-jobsDone)
}

// NewAPI builds the HTTP server and its run jobs over an open, migrated database.
func NewAPI(cfg config.API, deps APIDependencies) (*API, error) {
	users := postgres.NewUsers(deps.DB)
	sessions, box, err := newAPISecrets(cfg, deps, users)
	if err != nil {
		return nil, err
	}
	projectStore := postgres.NewProjects(deps.DB)
	access := project.NewAccess(projectStore)
	hubs := hub.NewService(postgres.NewHubs(deps.DB), access, deps.Clock)
	hubLink := hubws.NewServer(hubs, deps.Logger.With("component", "hubws"))
	environments := environment.NewService(postgres.NewEnvironments(deps.DB, box), access)
	testStore := postgres.NewTests(deps.DB)
	notes := knowledge.NewService(postgres.NewKnowledge(deps.DB), access, deps.Model)
	runStore := postgres.NewRuns(deps.DB)
	authoringStore := postgres.NewAuthorings(deps.DB)
	actionStore := postgres.NewActions(deps.DB)
	deviceStore, locks := postgres.NewDevices(deps.DB), device.NewLocks()
	pools, err := newAPIPools(cfg, deps, newAPIJobs(cfg, deps, jobDependencies{
		runs: runStore, authorings: authoringStore, environments: environments, projects: projectStore,
		actions: actionStore, knowledge: notes, hubLink: hubLink, locks: locks,
	}))
	if err != nil {
		return nil, err
	}
	server := transporthttp.NewServer(
		transporthttp.Config{Address: cfg.HTTPAddress, CORSOrigins: cfg.CORSOrigins, ICEServers: cfg.ICEServers},
		transporthttp.Dependencies{
			Logger:       deps.Logger.With("component", "http"),
			Health:       deps.DB,
			Sessions:     sessions,
			Profiles:     user.NewService(users),
			Projects:     project.NewService(projectStore),
			Environments: environments,
			Actions:      action.NewService(actionStore, access),
			Builds: build.NewService(build.Config{MaxSize: cfg.MaxBuildSize}, build.Dependencies{
				Store: postgres.NewBuilds(deps.DB), Blobs: deps.Blobs, Access: access, Clock: deps.Clock,
			}),
			Tests: test.NewService(testStore, access),
			Editor: test.NewEditor(test.EditorDependencies{
				Tests: testStore, Access: access, Knowledge: notes, Model: deps.Model,
			}),
			Knowledge:  notes,
			Runs:       run.NewService(runStore, access, deps.Clock),
			Videos:     run.NewVideos(run.VideoDependencies{Store: runStore, Blobs: deps.Blobs, Access: access}),
			Authorings: authoring.NewService(authoringStore, access, deps.Clock),
			Hubs:       hubs,
			HubTokens:  hubs,
			Devices:    device.NewService(deviceStore, access, deps.Clock),
			Control: device.NewControl(device.ControlDependencies{
				Store: deviceStore, Access: access, Hubs: hubLink, Locks: locks, Clock: deps.Clock,
			}),
			Streams: device.NewStreams(device.StreamDependencies{
				Store: deviceStore, Access: access, Relay: hubLink, Clock: deps.Clock,
			}),
			HubLink: hubLink,
		},
	)
	return &API{server: server, pools: pools}, nil
}

// newAPISecrets builds what signs sessions and what seals stored secrets, both keyed by the config.
func newAPISecrets(cfg config.API, deps APIDependencies, users *postgres.Users) (*auth.Service, *secret.Box, error) {
	sessions, err := auth.NewService(
		auth.Config{Secret: []byte(cfg.TokenSecret), AccessTTL: cfg.AccessTTL, RefreshTTL: cfg.RefreshTTL},
		users, deps.Clock, deps.AuthOptions...,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("build auth: %w", err)
	}
	box, err := secret.NewBox(cfg.SecretKey)
	if err != nil {
		return nil, nil, fmt.Errorf("build secret box: %w", err)
	}
	return sessions, box, nil
}

// apiJobs are the jobs that drive devices, which only the process holding the hub links can do.
type apiJobs struct {
	runs       *run.Executor
	authorings *authoring.Author
}

// jobDependencies are what the device jobs share with the HTTP server.
type jobDependencies struct {
	runs         *postgres.Runs
	authorings   *postgres.Authorings
	environments *environment.Service
	projects     *postgres.Projects
	actions      *postgres.Actions
	knowledge    *knowledge.Service
	hubLink      *hubws.Server
	// locks keeps each device to one holder at a time: a run, an authoring or a person driving it.
	locks *device.Locks
}

func newAPIJobs(cfg config.API, deps APIDependencies, shared jobDependencies) apiJobs {
	return apiJobs{
		runs: run.NewExecutor(run.ExecutorDependencies{
			Store: shared.runs, Environments: shared.environments, Actions: shared.actions,
			Hubs: shared.hubLink, Locks: shared.locks, Clock: deps.Clock,
		}),
		authorings: authoring.NewAuthor(authoring.AuthorConfig{Settle: cfg.AuthoringSettle}, authoring.AuthorDependencies{
			Store: shared.authorings, Environments: shared.environments, Projects: shared.projects, Actions: shared.actions,
			Knowledge: shared.knowledge, Model: deps.Model, Devices: shared.hubLink, Locks: shared.locks,
		}),
	}
}

func newAPIPools(cfg config.API, deps APIDependencies, jobs apiJobs) ([]*worker.Pool, error) {
	settings := poolSettings{
		owner: cfg.ID, lease: cfg.Lease, pollInterval: cfg.PollInterval, drainTimeout: cfg.ShutdownTimeout,
	}
	handlers := []jobHandler{
		{kind: run.DeviceJobKind, concurrency: cfg.RunWorkers, handler: jobs.runs.Run},
		{kind: authoring.JobKind, concurrency: cfg.AuthoringWorkers, handler: jobs.authorings.Run},
	}
	return newPools(deps.DB, deps.Logger, settings, handlers)
}
