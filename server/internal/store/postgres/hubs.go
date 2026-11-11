package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/hub"
)

const hubColumns = `id, project_id, name, version, created_by, created_at, last_seen_at, online_until`

type hubRow struct {
	id          string
	projectID   string
	name        string
	version     string
	createdBy   string
	createdAt   time.Time
	lastSeenAt  *time.Time
	onlineUntil *time.Time
}

func (r *hubRow) targets() []any {
	return []any{&r.id, &r.projectID, &r.name, &r.version, &r.createdBy, &r.createdAt, &r.lastSeenAt, &r.onlineUntil}
}

func (r hubRow) hub() hub.Hub {
	return hub.Hub{
		ID: r.id, ProjectID: r.projectID, Name: r.name, Version: r.version, CreatedBy: r.createdBy,
		CreatedAt: r.createdAt, LastSeenAt: r.lastSeenAt, OnlineUntil: r.onlineUntil,
	}
}

type Hubs struct {
	db *DB
}

func NewHubs(db *DB) *Hubs {
	return &Hubs{db: db}
}

func (s *Hubs) CreateHub(ctx context.Context, h hub.Hub, tokenHash []byte) (hub.Hub, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var row hubRow
	err := s.db.pool.QueryRow(ctx, `
		INSERT INTO hubs (id, project_id, name, token_hash, version, created_by, created_at)
		VALUES ($1, $2, $3, $4, '', $5, $6)
		RETURNING `+hubColumns,
		h.ID, h.ProjectID, h.Name, tokenHash, h.CreatedBy, s.db.clock.Now(),
	).Scan(row.targets()...)
	if isUniqueViolation(err) {
		return hub.Hub{}, fmt.Errorf("hub name taken: %w", fault.ErrConflict)
	}
	if err != nil {
		return hub.Hub{}, fmt.Errorf("insert hub: %w", err)
	}
	return row.hub(), nil
}

func (s *Hubs) Hubs(ctx context.Context, projectID string) ([]hub.Hub, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	rows, err := s.db.pool.Query(ctx,
		`SELECT `+hubColumns+` FROM hubs WHERE project_id = $1 ORDER BY name`, projectID)
	if err != nil {
		return nil, fmt.Errorf("select hubs: %w", err)
	}
	hubs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (hub.Hub, error) {
		var r hubRow
		err := row.Scan(r.targets()...)
		return r.hub(), err
	})
	if err != nil {
		return nil, fmt.Errorf("read hubs: %w", err)
	}
	return hubs, nil
}

func (s *Hubs) HubByToken(ctx context.Context, tokenHash []byte) (hub.Hub, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var row hubRow
	err := s.db.pool.QueryRow(ctx, `SELECT `+hubColumns+` FROM hubs WHERE token_hash = $1`, tokenHash).
		Scan(row.targets()...)
	if errors.Is(err, pgx.ErrNoRows) {
		return hub.Hub{}, fmt.Errorf("hub token: %w", fault.ErrNotFound)
	}
	if err != nil {
		return hub.Hub{}, fmt.Errorf("query hub by token: %w", err)
	}
	return row.hub(), nil
}

func (s *Hubs) SetTokenHash(ctx context.Context, projectID, id string, tokenHash []byte) (hub.Hub, error) {
	if !validID(id) {
		return hub.Hub{}, fmt.Errorf("hub %s: %w", id, fault.ErrNotFound)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var row hubRow
	err := s.db.pool.QueryRow(ctx,
		`UPDATE hubs SET token_hash = $3 WHERE project_id = $1 AND id = $2 RETURNING `+hubColumns,
		projectID, id, tokenHash,
	).Scan(row.targets()...)
	if errors.Is(err, pgx.ErrNoRows) {
		return hub.Hub{}, fmt.Errorf("hub %s: %w", id, fault.ErrNotFound)
	}
	if err != nil {
		return hub.Hub{}, fmt.Errorf("update hub token: %w", err)
	}
	return row.hub(), nil
}

func (s *Hubs) DeleteHub(ctx context.Context, projectID, id string) error {
	if !validID(id) {
		return fmt.Errorf("hub %s: %w", id, fault.ErrNotFound)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	tag, err := s.db.pool.Exec(ctx, `DELETE FROM hubs WHERE project_id = $1 AND id = $2`, projectID, id)
	if err != nil {
		return fmt.Errorf("delete hub: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("hub %s: %w", id, fault.ErrNotFound)
	}
	return nil
}

func (s *Hubs) SetPresence(ctx context.Context, id string, presence hub.Presence) error {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	return setPresence(ctx, s.db.pool, id, presence)
}

type execer interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

func setPresence(ctx context.Context, db execer, id string, presence hub.Presence) error {
	tag, err := db.Exec(ctx, `UPDATE hubs SET last_seen_at = $2, online_until = $3 WHERE id = $1`,
		id, presence.SeenAt, presence.OnlineUntil)
	if err != nil {
		return fmt.Errorf("update hub presence: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("hub %s: %w", id, fault.ErrNotFound)
	}
	return nil
}

// ReplaceInventory upserts the reported devices by serial, so a device keeps its id across reconnects, and
// marks the hub's other devices disconnected.
func (s *Hubs) ReplaceInventory(ctx context.Context, id string, inventory hub.Inventory, presence hub.Presence) error {
	columns := newInventoryColumns(len(inventory.Devices))
	for _, d := range inventory.Devices {
		deviceID, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("generate device id: %w", err)
		}
		columns.add(deviceID.String(), d)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	return pgx.BeginFunc(ctx, s.db.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE hubs SET version = $2 WHERE id = $1`, id, inventory.Version); err != nil {
			return fmt.Errorf("update hub version: %w", err)
		}
		if err := setPresence(ctx, tx, id, presence); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO devices (id, hub_id, serial, name, model, platform, os_version, physical, state,
				first_seen_at, last_seen_at)
			SELECT d.id, $1, d.serial, d.name, d.model, d.platform, d.os_version, d.physical, d.state, $10, $10
			FROM unnest($2::uuid[], $3::text[], $4::text[], $5::text[], $6::text[], $7::text[], $8::boolean[],
				$9::text[]) AS d (id, serial, name, model, platform, os_version, physical, state)
			ON CONFLICT (hub_id, serial) DO UPDATE SET
				name = excluded.name, model = excluded.model, platform = excluded.platform,
				os_version = excluded.os_version, physical = excluded.physical, state = excluded.state,
				last_seen_at = excluded.last_seen_at`,
			id, columns.ids, columns.serials, columns.names, columns.models, columns.platforms,
			columns.osVersions, columns.physical, columns.states, presence.SeenAt)
		if err != nil {
			return fmt.Errorf("upsert devices: %w", err)
		}
		_, err = tx.Exec(ctx, `
			UPDATE devices SET state = 'disconnected'
			WHERE hub_id = $1 AND state <> 'disconnected' AND NOT serial = ANY ($2)`,
			id, columns.serials)
		if err != nil {
			return fmt.Errorf("disconnect missing devices: %w", err)
		}
		return nil
	})
}

// inventoryColumns spreads devices into one array per column for a single unnest insert.
type inventoryColumns struct {
	ids, serials, names, models, platforms, osVersions, states []string
	physical                                                   []bool
}

// newInventoryColumns starts with empty arrays rather than nil ones, which would be sent as NULL and make
// "serial = ANY" match nothing for an empty inventory.
func newInventoryColumns(n int) *inventoryColumns {
	return &inventoryColumns{
		ids: make([]string, 0, n), serials: make([]string, 0, n), names: make([]string, 0, n),
		models: make([]string, 0, n), platforms: make([]string, 0, n), osVersions: make([]string, 0, n),
		states: make([]string, 0, n), physical: make([]bool, 0, n),
	}
}

func (c *inventoryColumns) add(id string, d hub.ReportedDevice) {
	c.ids = append(c.ids, id)
	c.serials = append(c.serials, d.Serial)
	c.names = append(c.names, d.Name)
	c.models = append(c.models, d.Model)
	c.platforms = append(c.platforms, string(d.Platform))
	c.osVersions = append(c.osVersions, d.OSVersion)
	c.physical = append(c.physical, d.Physical)
	c.states = append(c.states, string(d.State))
}
