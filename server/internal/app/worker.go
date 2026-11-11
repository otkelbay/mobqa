package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/mobqa/backend/internal/clock"
	"github.com/mobqa/backend/internal/config"
	"github.com/mobqa/backend/internal/llm"
	"github.com/mobqa/backend/internal/store/blob"
	"github.com/mobqa/backend/internal/store/postgres"
	"github.com/mobqa/backend/internal/usecase/authoring"
	"github.com/mobqa/backend/internal/usecase/build"
	"github.com/mobqa/backend/internal/usecase/chat"
	"github.com/mobqa/backend/internal/usecase/knowledge"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/test"
	"github.com/mobqa/backend/internal/worker"
)

const (
	retryBase = 5 * time.Second
	retryMax  = 10 * time.Minute
)

// RunWorker processes background jobs until ctx ends and every pool has drained.
func RunWorker(ctx context.Context, cfg config.Worker, logger *slog.Logger) error {
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
	pools, err := NewPools(cfg, WorkerDependencies{DB: db, Blobs: blobs, Model: model, Clock: now, Logger: logger})
	if err != nil {
		return err
	}
	logger.Info("worker started", "component", "worker")
	return RunPools(ctx, pools)
}

// Model is the language model behind generation, retrieval and the agent.
type Model interface {
	Chat(ctx context.Context, request chat.Request) (chat.Response, error)
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

func newModel(cfg config.LLM) (*llm.Client, error) {
	return llm.New(llm.Config{
		BaseURL: cfg.BaseURL, APIKey: cfg.APIKey, Model: cfg.Model, EmbeddingModel: cfg.EmbeddingModel,
	})
}

type WorkerDependencies struct {
	DB     *postgres.DB
	Blobs  *blob.Dir
	Model  Model
	Clock  clock.Clock
	Logger *slog.Logger
}

// NewPools builds one pool per job kind over an open, migrated database.
func NewPools(cfg config.Worker, deps WorkerDependencies) ([]*worker.Pool, error) {
	projects := postgres.NewProjects(deps.DB)
	builds := build.NewService(build.Config{}, build.Dependencies{
		Store:  postgres.NewBuilds(deps.DB),
		Blobs:  deps.Blobs,
		Access: project.NewAccess(projects),
		Clock:  deps.Clock,
	})
	generator := test.NewGenerator(postgres.NewTests(deps.DB), projects, deps.Model)
	indexer := knowledge.NewIndexer(postgres.NewKnowledge(deps.DB), deps.Model)
	reviewer := authoring.NewReviewer(postgres.NewAuthorings(deps.DB), projects, deps.Model)
	settings := poolSettings{
		owner: cfg.ID, lease: cfg.Lease, pollInterval: cfg.PollInterval, drainTimeout: cfg.DrainTimeout,
	}
	handlers := []jobHandler{
		{kind: build.AnalysisJobKind, concurrency: cfg.AnalysisWorkers, handler: builds.Analyze},
		{kind: test.GenerationJobKind, concurrency: cfg.GenerationWorkers, handler: generator.Run},
		{kind: knowledge.IndexJobKind, concurrency: cfg.IndexWorkers, handler: indexer.Run},
		{kind: authoring.ReviewJobKind, concurrency: cfg.ReviewWorkers, handler: reviewer.Run},
	}
	return newPools(deps.DB, deps.Logger, settings, handlers)
}

type poolSettings struct {
	owner        string
	lease        time.Duration
	pollInterval time.Duration
	drainTimeout time.Duration
}

type jobHandler struct {
	kind        string
	concurrency int
	handler     worker.HandlerFunc
}

func newPools(db *postgres.DB, logger *slog.Logger, settings poolSettings, handlers []jobHandler,
) ([]*worker.Pool, error) {
	pools := make([]*worker.Pool, len(handlers))
	for i, h := range handlers {
		pool, err := newPool(db, logger, settings, h)
		if err != nil {
			return nil, err
		}
		pools[i] = pool
	}
	return pools, nil
}

func newPool(db *postgres.DB, logger *slog.Logger, settings poolSettings, h jobHandler) (*worker.Pool, error) {
	pool, err := worker.NewPool(worker.Config{
		Kind:         h.kind,
		Owner:        settings.owner,
		Concurrency:  h.concurrency,
		Lease:        settings.lease,
		PollInterval: settings.pollInterval,
		DrainTimeout: settings.drainTimeout,
		RetryBase:    retryBase,
		RetryMax:     retryMax,
	}, worker.Dependencies{
		Store:   postgres.NewJobs(db),
		Signals: db,
		Handler: h.handler,
		Logger:  logger,
	})
	if err != nil {
		return nil, fmt.Errorf("build %s pool: %w", h.kind, err)
	}
	return pool, nil
}

// RunPools runs every pool until ctx ends and returns once all of them have drained.
func RunPools(ctx context.Context, pools []*worker.Pool) error {
	errs := make([]error, len(pools))
	var wg sync.WaitGroup
	for i, pool := range pools {
		wg.Go(func() { errs[i] = pool.Run(ctx) })
	}
	wg.Wait()
	return errors.Join(errs...)
}
