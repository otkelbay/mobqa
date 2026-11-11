package test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/worker"
)

// GenerationJobKind is the job that asks the model for tests and saves them.
const GenerationJobKind = "test_generation"

const (
	maxBriefLength = 4000
	// generationAttempts retries a model that was briefly unavailable.
	generationAttempts = 3
)

type GenerationStatus string

const (
	GenerationPending   GenerationStatus = "pending"
	GenerationSucceeded GenerationStatus = "succeeded"
	GenerationFailed    GenerationStatus = "failed"
)

// Generation is a request to draft tests from a brief. Once finished it lists the tests it created, or
// says why it created none.
type Generation struct {
	ID         string
	ProjectID  string
	Platform   project.Platform
	Brief      string
	Status     GenerationStatus
	TestIDs    []string
	Failure    string
	CreatedBy  string
	CreatedAt  time.Time
	FinishedAt *time.Time
}

type generationPayload struct {
	ProjectID    string `json:"project_id"`
	GenerationID string `json:"generation_id"`
}

type GenerateInput struct {
	UserID    string
	ProjectID string
	Platform  project.Platform
	Brief     string
}

func (in GenerateInput) Validate() error {
	if err := validatePlatform(in.Platform); err != nil {
		return err
	}
	if strings.TrimSpace(in.Brief) == "" || utf8.RuneCountInString(in.Brief) > maxBriefLength {
		return fault.WithDetail(fault.ErrInvalidInput, "brief must be 1 to 4000 characters")
	}
	return nil
}

// Generate records the request and queues the job that fulfils it; the result is read with Generation.
func (s *Service) Generate(ctx context.Context, in GenerateInput) (Generation, error) {
	if err := in.Validate(); err != nil {
		return Generation{}, err
	}
	if _, err := s.access.Require(ctx, in.ProjectID, in.UserID, project.RoleEngineer); err != nil {
		return Generation{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Generation{}, fmt.Errorf("generate generation id: %w", err)
	}
	payload, err := json.Marshal(generationPayload{ProjectID: in.ProjectID, GenerationID: id.String()})
	if err != nil {
		return Generation{}, fmt.Errorf("encode generation payload: %w", err)
	}
	created, err := s.store.CreateGeneration(ctx, Generation{
		ID: id.String(), ProjectID: in.ProjectID, Platform: in.Platform, Brief: strings.TrimSpace(in.Brief),
		Status: GenerationPending, CreatedBy: in.UserID,
	}, worker.EnqueueInput{Kind: GenerationJobKind, Payload: payload, MaxAttempts: generationAttempts})
	if err != nil {
		return Generation{}, fmt.Errorf("create generation: %w", err)
	}
	return created, nil
}

type GenerationRef struct {
	UserID       string
	ProjectID    string
	GenerationID string
}

func (s *Service) Generation(ctx context.Context, ref GenerationRef) (Generation, error) {
	if _, err := s.access.Require(ctx, ref.ProjectID, ref.UserID, project.RoleViewer); err != nil {
		return Generation{}, err
	}
	found, err := s.store.Generation(ctx, ref.ProjectID, ref.GenerationID)
	if errors.Is(err, fault.ErrNotFound) {
		return Generation{}, fault.WithDetail(fault.ErrNotFound, "generation not found")
	}
	if err != nil {
		return Generation{}, fmt.Errorf("load generation: %w", err)
	}
	return found, nil
}
