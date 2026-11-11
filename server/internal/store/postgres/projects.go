package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/project"
)

const projectColumns = `p.id, p.name, p.description, p.platforms, p.package_name, p.initial, p.color, p.text_color,
	p.auth_profile, p.created_at, p.updated_at`

// memberProjectsFilter matches a user's live projects whose name contains the search text literally.
const memberProjectsFilter = `WHERE m.user_id = $1 AND p.archived_at IS NULL
	AND ($2 = '' OR p.name ILIKE '%' || replace(replace(replace($2, '\', '\\'), '%', '\%'), '_', '\_') || '%')`

type projectRow struct {
	id          string
	name        string
	description string
	platforms   []string
	packageName string
	initial     string
	color       string
	textColor   string
	authProfile authProfileDocument
	createdAt   time.Time
	updatedAt   time.Time
}

func (r *projectRow) targets() []any {
	return []any{
		&r.id, &r.name, &r.description, &r.platforms, &r.packageName, &r.initial, &r.color, &r.textColor,
		&r.authProfile, &r.createdAt, &r.updatedAt,
	}
}

func (r projectRow) project() project.Project {
	platforms := make([]project.Platform, len(r.platforms))
	for i, platform := range r.platforms {
		platforms[i] = project.Platform(platform)
	}
	return project.Project{
		ID:          r.id,
		Name:        r.name,
		Description: r.description,
		Platforms:   platforms,
		PackageName: r.packageName,
		Appearance:  project.Appearance{Initial: r.initial, Color: r.color, TextColor: r.textColor},
		AuthProfile: r.authProfile.profile(),
		CreatedAt:   r.createdAt,
		UpdatedAt:   r.updatedAt,
	}
}

// authProfileDocument is the stored JSON shape of a project's login recipe.
type authProfileDocument struct {
	Method      string       `json:"method"`
	LoginVar    string       `json:"login_var,omitempty"`
	PasswordVar string       `json:"password_var,omitempty"`
	OTP         *otpDocument `json:"otp,omitempty"`
	SSO         *ssoDocument `json:"sso,omitempty"`
	Notes       string       `json:"notes,omitempty"`
}

type otpDocument struct {
	Override string `json:"override,omitempty"`
	ActionID string `json:"action_id,omitempty"`
}

type ssoDocument struct {
	Provider   string `json:"provider"`
	AccountVar string `json:"account_var,omitempty"`
}

func newAuthProfileDocument(p project.AuthProfile) authProfileDocument {
	doc := authProfileDocument{
		Method: string(p.Method), LoginVar: p.LoginVar, PasswordVar: p.PasswordVar, Notes: p.Notes,
	}
	if p.OTP != nil {
		doc.OTP = &otpDocument{Override: p.OTP.Override, ActionID: p.OTP.ActionID}
	}
	if p.SSO != nil {
		doc.SSO = &ssoDocument{Provider: string(p.SSO.Provider), AccountVar: p.SSO.AccountVar}
	}
	return doc
}

func (d authProfileDocument) profile() project.AuthProfile {
	p := project.AuthProfile{
		Method: project.AuthMethod(d.Method), LoginVar: d.LoginVar, PasswordVar: d.PasswordVar, Notes: d.Notes,
	}
	if d.OTP != nil {
		p.OTP = &project.OTPSource{Override: d.OTP.Override, ActionID: d.OTP.ActionID}
	}
	if d.SSO != nil {
		p.SSO = &project.SSO{Provider: project.SSOProvider(d.SSO.Provider), AccountVar: d.SSO.AccountVar}
	}
	return p
}

type memberRow struct {
	userID    string
	email     string
	name      string
	avatarURL *string
	role      string
	joinedAt  time.Time
}

func (r *memberRow) targets() []any {
	return []any{&r.userID, &r.email, &r.name, &r.avatarURL, &r.role, &r.joinedAt}
}

func (r memberRow) member() project.Member {
	m := project.Member{
		UserID: r.userID, Email: r.email, Name: r.name, Role: project.Role(r.role), JoinedAt: r.joinedAt,
	}
	if r.avatarURL != nil {
		m.AvatarURL = *r.avatarURL
	}
	return m
}

func platformStrings(platforms []project.Platform) []string {
	if platforms == nil {
		return nil
	}
	values := make([]string, len(platforms))
	for i, platform := range platforms {
		values[i] = string(platform)
	}
	return values
}

// validID tells malformed ids from missing rows: ids come from URLs, and a non-UUID can match nothing.
func validID(id string) bool {
	return uuid.Validate(id) == nil
}

type Projects struct {
	db *DB
}

func NewProjects(db *DB) *Projects {
	return &Projects{db: db}
}

// Role returns the user's role in a live project; archived projects have no members.
func (p *Projects) Role(ctx context.Context, projectID, userID string) (project.Role, error) {
	if !validID(projectID) || !validID(userID) {
		return "", fmt.Errorf("project role: %w", fault.ErrNotFound)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var role string
	err := p.db.pool.QueryRow(ctx, `
		SELECT m.role FROM project_members m JOIN projects p ON p.id = m.project_id
		WHERE m.project_id = $1 AND m.user_id = $2 AND p.archived_at IS NULL`,
		projectID, userID,
	).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("project role: %w", fault.ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("select project role: %w", err)
	}
	return project.Role(role), nil
}

func (p *Projects) CreateProject(ctx context.Context, in project.NewProject) (project.Project, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return project.Project{}, fmt.Errorf("generate project id: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	now := p.db.clock.Now()
	var row projectRow
	err = pgx.BeginFunc(ctx, p.db.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			INSERT INTO projects AS p (id, name, description, platforms, package_name, initial, color, text_color,
				auth_profile, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10)
			RETURNING `+projectColumns,
			id, in.Name, in.Description, platformStrings(in.Platforms), in.PackageName,
			in.Appearance.Initial, in.Appearance.Color, in.Appearance.TextColor,
			newAuthProfileDocument(project.AuthProfile{Method: project.AuthNone}), now,
		).Scan(row.targets()...)
		if err != nil {
			return fmt.Errorf("insert project: %w", err)
		}
		_, err = tx.Exec(ctx,
			`INSERT INTO project_members (project_id, user_id, role, joined_at) VALUES ($1, $2, 'owner', $3)`,
			id, in.OwnerID, now)
		if err != nil {
			return fmt.Errorf("insert owner: %w", err)
		}
		return nil
	})
	if err != nil {
		return project.Project{}, err
	}
	return row.project(), nil
}

// Projects pages through the user's live projects, newest first, and counts all that match.
func (p *Projects) Projects(ctx context.Context, query project.Query) ([]project.Project, int, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	rows, err := p.db.pool.Query(ctx, `
		SELECT `+projectColumns+`, count(*) OVER ()
		FROM projects p JOIN project_members m ON m.project_id = p.id
		`+memberProjectsFilter+`
		ORDER BY p.created_at DESC, p.id DESC
		LIMIT $3 OFFSET $4`,
		query.UserID, query.Search, query.Limit, query.Offset)
	if err != nil {
		return nil, 0, fmt.Errorf("select projects: %w", err)
	}
	defer rows.Close()
	var (
		projects []project.Project
		total    int
	)
	for rows.Next() {
		var row projectRow
		if err := rows.Scan(append(row.targets(), &total)...); err != nil {
			return nil, 0, fmt.Errorf("scan project: %w", err)
		}
		projects = append(projects, row.project())
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("read projects: %w", err)
	}
	if len(projects) == 0 && query.Offset > 0 {
		total, err = p.countProjects(ctx, query)
		return nil, total, err
	}
	return projects, total, nil
}

// countProjects counts matches when the page is past the end, where the window count has no row to ride on.
func (p *Projects) countProjects(ctx context.Context, query project.Query) (int, error) {
	var total int
	err := p.db.pool.QueryRow(ctx, `
		SELECT count(*) FROM projects p JOIN project_members m ON m.project_id = p.id
		`+memberProjectsFilter,
		query.UserID, query.Search,
	).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("count projects: %w", err)
	}
	return total, nil
}

func (p *Projects) Project(ctx context.Context, id string) (project.Project, error) {
	if !validID(id) {
		return project.Project{}, fmt.Errorf("project %s: %w", id, fault.ErrNotFound)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var row projectRow
	err := p.db.pool.QueryRow(ctx,
		`SELECT `+projectColumns+` FROM projects p WHERE p.id = $1 AND p.archived_at IS NULL`, id,
	).Scan(row.targets()...)
	if errors.Is(err, pgx.ErrNoRows) {
		return project.Project{}, fmt.Errorf("project %s: %w", id, fault.ErrNotFound)
	}
	if err != nil {
		return project.Project{}, fmt.Errorf("select project: %w", err)
	}
	return row.project(), nil
}

func (p *Projects) UpdateProject(ctx context.Context, change project.Change) (project.Project, error) {
	if !validID(change.ProjectID) {
		return project.Project{}, fmt.Errorf("project %s: %w", change.ProjectID, fault.ErrNotFound)
	}
	var initial, color, textColor *string
	if change.Appearance != nil {
		initial, color, textColor = &change.Appearance.Initial, &change.Appearance.Color, &change.Appearance.TextColor
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var row projectRow
	err := p.db.pool.QueryRow(ctx, `
		UPDATE projects AS p SET
			name = coalesce($2, name),
			description = coalesce($3, description),
			platforms = coalesce($4, platforms),
			package_name = coalesce($5, package_name),
			initial = coalesce($6, initial),
			color = coalesce($7, color),
			text_color = coalesce($8, text_color),
			updated_at = $9
		WHERE id = $1 AND archived_at IS NULL
		RETURNING `+projectColumns,
		change.ProjectID, change.Name, change.Description, platformStrings(change.Platforms), change.PackageName,
		initial, color, textColor, p.db.clock.Now(),
	).Scan(row.targets()...)
	if errors.Is(err, pgx.ErrNoRows) {
		return project.Project{}, fmt.Errorf("project %s: %w", change.ProjectID, fault.ErrNotFound)
	}
	if err != nil {
		return project.Project{}, fmt.Errorf("update project: %w", err)
	}
	return row.project(), nil
}

func (p *Projects) ArchiveProject(ctx context.Context, id string) error {
	if !validID(id) {
		return fmt.Errorf("project %s: %w", id, fault.ErrNotFound)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	tag, err := p.db.pool.Exec(ctx,
		`UPDATE projects SET archived_at = $2, updated_at = $2 WHERE id = $1 AND archived_at IS NULL`,
		id, p.db.clock.Now())
	if err != nil {
		return fmt.Errorf("archive project: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("project %s: %w", id, fault.ErrNotFound)
	}
	return nil
}

func (p *Projects) SetAuthProfile(ctx context.Context, projectID string, profile project.AuthProfile) error {
	if !validID(projectID) {
		return fmt.Errorf("project %s: %w", projectID, fault.ErrNotFound)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	tag, err := p.db.pool.Exec(ctx,
		`UPDATE projects SET auth_profile = $2, updated_at = $3 WHERE id = $1 AND archived_at IS NULL`,
		projectID, newAuthProfileDocument(profile), p.db.clock.Now())
	if err != nil {
		return fmt.Errorf("store auth profile: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("project %s: %w", projectID, fault.ErrNotFound)
	}
	return nil
}

// Members lists the owner first, then everyone else by when they joined.
func (p *Projects) Members(ctx context.Context, projectID string) ([]project.Member, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	rows, err := p.db.pool.Query(ctx, `
		SELECT u.id, u.email, u.name, u.avatar_url, m.role, m.joined_at
		FROM project_members m JOIN users u ON u.id = m.user_id
		WHERE m.project_id = $1
		ORDER BY m.role = 'owner' DESC, m.joined_at, u.id`,
		projectID)
	if err != nil {
		return nil, fmt.Errorf("select members: %w", err)
	}
	defer rows.Close()
	var members []project.Member
	for rows.Next() {
		var row memberRow
		if err := rows.Scan(row.targets()...); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		members = append(members, row.member())
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read members: %w", err)
	}
	return members, nil
}

func (p *Projects) AddMember(ctx context.Context, in project.NewMember) (project.Member, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var row memberRow
	err := p.db.pool.QueryRow(ctx, `
		WITH added AS (
			INSERT INTO project_members (project_id, user_id, role, joined_at)
			SELECT $1, id, $3, $4 FROM users WHERE email = $2
			RETURNING user_id, role, joined_at
		)
		SELECT u.id, u.email, u.name, u.avatar_url, added.role, added.joined_at
		FROM added JOIN users u ON u.id = added.user_id`,
		in.ProjectID, in.Email, string(in.Role), p.db.clock.Now(),
	).Scan(row.targets()...)
	if errors.Is(err, pgx.ErrNoRows) {
		return project.Member{}, fmt.Errorf("account by email: %w", fault.ErrNotFound)
	}
	if isUniqueViolation(err) {
		return project.Member{}, fmt.Errorf("already a member: %w", fault.ErrConflict)
	}
	if err != nil {
		return project.Member{}, fmt.Errorf("insert member: %w", err)
	}
	return row.member(), nil
}

func (p *Projects) SetMemberRole(ctx context.Context, projectID, userID string, role project.Role) error {
	if !validID(projectID) || !validID(userID) {
		return fmt.Errorf("member %s: %w", userID, fault.ErrNotFound)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	tag, err := p.db.pool.Exec(ctx,
		`UPDATE project_members SET role = $3 WHERE project_id = $1 AND user_id = $2`,
		projectID, userID, string(role))
	if err != nil {
		return fmt.Errorf("set member role: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("member %s: %w", userID, fault.ErrNotFound)
	}
	return nil
}

func (p *Projects) RemoveMember(ctx context.Context, projectID, userID string) error {
	if !validID(projectID) || !validID(userID) {
		return fmt.Errorf("member %s: %w", userID, fault.ErrNotFound)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	tag, err := p.db.pool.Exec(ctx,
		`DELETE FROM project_members WHERE project_id = $1 AND user_id = $2`, projectID, userID)
	if err != nil {
		return fmt.Errorf("remove member: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("member %s: %w", userID, fault.ErrNotFound)
	}
	return nil
}
