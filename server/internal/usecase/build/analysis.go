package build

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/worker"
)

const (
	kilobyte = 1 << 10
	megabyte = 1 << 20
)

type entry struct {
	crc  uint32
	size uint64
}

// Analyze handles an analysis job: it compares the build with its predecessor and marks it ready. A
// build deleted before its analysis ran needs nothing.
func (s *Service) Analyze(ctx context.Context, job worker.Job) error {
	var payload analysisPayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return worker.Permanent(fmt.Errorf("decode analysis payload: %w", err))
	}
	current, err := s.store.Build(ctx, payload.ProjectID, payload.BuildID)
	if errors.Is(err, fault.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load build: %w", err)
	}
	analysis := Analysis{Changes: []string{}}
	previous, err := s.store.PreviousBuild(ctx, current)
	switch {
	case errors.Is(err, fault.ErrNotFound):
	case err != nil:
		return fmt.Errorf("load previous build: %w", err)
	default:
		analysis.PreviousID = previous.ID
		if analysis.Changes, err = s.compare(ctx, previous, current); err != nil {
			return err
		}
	}
	if err := s.store.SetAnalysis(ctx, current.ID, analysis); err != nil && !errors.Is(err, fault.ErrNotFound) {
		return fmt.Errorf("store analysis: %w", err)
	}
	return nil
}

func (s *Service) compare(ctx context.Context, previous, current Build) ([]string, error) {
	changes := metadataChanges(previous, current)
	if previous.SHA256 == current.SHA256 {
		return changes, nil
	}
	before, err := s.entries(ctx, previous)
	if errors.Is(err, fault.ErrNotFound) {
		return changes, nil
	}
	if err != nil {
		return nil, err
	}
	after, err := s.entries(ctx, current)
	if err != nil {
		return nil, err
	}
	if summary := diffEntries(before, after); summary != "" {
		changes = append(changes, summary)
	}
	return changes, nil
}

func metadataChanges(previous, current Build) []string {
	changes := []string{}
	if previous.Version != current.Version {
		changes = append(changes, fmt.Sprintf("version %s → %s", previous.Version, current.Version))
	}
	if previous.BuildNumber != current.BuildNumber {
		changes = append(changes, fmt.Sprintf("build number %s → %s", previous.BuildNumber, current.BuildNumber))
	}
	if delta := current.Size - previous.Size; delta != 0 {
		changes = append(changes, "size "+formatDelta(delta))
	}
	return changes
}

func (s *Service) entries(ctx context.Context, b Build) (map[string]entry, error) {
	file, err := s.blobs.Open(ctx, b.BlobKey)
	if err != nil {
		return nil, fmt.Errorf("open package of build %s: %w", b.ID, err)
	}
	defer func() { _ = file.Close() }()
	archive, err := zip.NewReader(file, b.Size)
	if err != nil {
		return nil, worker.Permanent(fmt.Errorf("read package of build %s: %w", b.ID, err))
	}
	entries := make(map[string]entry, len(archive.File))
	for _, f := range archive.File {
		if !f.FileInfo().IsDir() {
			entries[f.Name] = entry{crc: f.CRC32, size: f.UncompressedSize64}
		}
	}
	return entries, nil
}

func diffEntries(before, after map[string]entry) string {
	var added, removed, changed int
	for name, now := range after {
		was, ok := before[name]
		switch {
		case !ok:
			added++
		case was != now:
			changed++
		}
	}
	for name := range before {
		if _, ok := after[name]; !ok {
			removed++
		}
	}
	parts := []string{}
	for _, count := range []struct {
		n     int
		label string
	}{{added, "added"}, {removed, "removed"}, {changed, "changed"}} {
		if count.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", count.n, count.label))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "files: " + strings.Join(parts, ", ")
}

func formatDelta(delta int64) string {
	sign := "+"
	if delta < 0 {
		sign, delta = "-", -delta
	}
	switch {
	case delta >= megabyte:
		return fmt.Sprintf("%s%.1f MB", sign, float64(delta)/megabyte)
	case delta >= kilobyte:
		return fmt.Sprintf("%s%.1f KB", sign, float64(delta)/kilobyte)
	default:
		return fmt.Sprintf("%s%d B", sign, delta)
	}
}
