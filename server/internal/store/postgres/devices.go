package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/device"
	"github.com/mobqa/backend/internal/usecase/project"
)

const deviceSelect = `
	SELECT d.id, h.project_id, d.hub_id, h.name, d.serial, d.name, d.model, d.platform, d.os_version, d.physical,
		d.state, h.online_until, d.last_seen_at
	FROM devices d JOIN hubs h ON h.id = d.hub_id`

type deviceRow struct {
	id             string
	projectID      string
	hubID          string
	hubName        string
	serial         string
	name           string
	model          string
	platform       string
	osVersion      string
	physical       bool
	state          string
	hubOnlineUntil *time.Time
	lastSeenAt     time.Time
}

func (r *deviceRow) targets() []any {
	return []any{
		&r.id, &r.projectID, &r.hubID, &r.hubName, &r.serial, &r.name, &r.model, &r.platform, &r.osVersion,
		&r.physical, &r.state, &r.hubOnlineUntil, &r.lastSeenAt,
	}
}

func (r deviceRow) device() device.Device {
	return device.Device{
		ID: r.id, ProjectID: r.projectID, HubID: r.hubID, HubName: r.hubName, Serial: r.serial, Name: r.name,
		Model: r.model, Platform: project.Platform(r.platform), OSVersion: r.osVersion, Physical: r.physical,
		State: device.State(r.state), HubOnlineUntil: r.hubOnlineUntil, LastSeenAt: r.lastSeenAt,
	}
}

type Devices struct {
	db *DB
}

func NewDevices(db *DB) *Devices {
	return &Devices{db: db}
}

func (s *Devices) Devices(ctx context.Context, query device.Query) ([]device.Device, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	rows, err := s.db.pool.Query(ctx, deviceSelect+`
		WHERE h.project_id = $1 AND ($2 = '' OR d.platform = $2)
		ORDER BY h.name, d.name, d.serial`,
		query.ProjectID, string(query.Platform))
	if err != nil {
		return nil, fmt.Errorf("select devices: %w", err)
	}
	devices, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (device.Device, error) {
		var r deviceRow
		err := row.Scan(r.targets()...)
		return r.device(), err
	})
	if err != nil {
		return nil, fmt.Errorf("read devices: %w", err)
	}
	return devices, nil
}

func (s *Devices) Device(ctx context.Context, projectID, id string) (device.Device, error) {
	if !validID(id) {
		return device.Device{}, fmt.Errorf("device %s: %w", id, fault.ErrNotFound)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var row deviceRow
	err := s.db.pool.QueryRow(ctx, deviceSelect+` WHERE h.project_id = $1 AND d.id = $2`, projectID, id).
		Scan(row.targets()...)
	if errors.Is(err, pgx.ErrNoRows) {
		return device.Device{}, fmt.Errorf("device %s: %w", id, fault.ErrNotFound)
	}
	if err != nil {
		return device.Device{}, fmt.Errorf("query device: %w", err)
	}
	return row.device(), nil
}

func (s *Devices) SetStreamKeyHash(ctx context.Context, projectID, id string, hash []byte) (device.Device, error) {
	if !validID(id) {
		return device.Device{}, fmt.Errorf("device %s: %w", id, fault.ErrNotFound)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var row deviceRow
	err := s.db.pool.QueryRow(ctx, `
		WITH updated AS (
			UPDATE devices d SET stream_key_hash = $3 FROM hubs h
			WHERE h.id = d.hub_id AND h.project_id = $1 AND d.id = $2
			RETURNING d.id
		)`+deviceSelect+` JOIN updated u ON u.id = d.id`,
		projectID, id, hash,
	).Scan(row.targets()...)
	if errors.Is(err, pgx.ErrNoRows) {
		return device.Device{}, fmt.Errorf("device %s: %w", id, fault.ErrNotFound)
	}
	if err != nil {
		return device.Device{}, fmt.Errorf("update device stream key: %w", err)
	}
	return row.device(), nil
}

func (s *Devices) DeviceByStreamKey(ctx context.Context, hash []byte) (device.Device, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var row deviceRow
	err := s.db.pool.QueryRow(ctx, deviceSelect+` WHERE d.stream_key_hash = $1`, hash).Scan(row.targets()...)
	if errors.Is(err, pgx.ErrNoRows) {
		return device.Device{}, fmt.Errorf("device by stream key: %w", fault.ErrNotFound)
	}
	if err != nil {
		return device.Device{}, fmt.Errorf("query device by stream key: %w", err)
	}
	return row.device(), nil
}
