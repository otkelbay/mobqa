package project

import (
	"context"
	"errors"
	"fmt"

	"github.com/mobqa/backend/internal/fault"
)

// Role is a member's level in a project; each level includes everything below it.
type Role string

const (
	RoleViewer   Role = "viewer"
	RoleEngineer Role = "engineer"
	RoleAdmin    Role = "admin"
	RoleOwner    Role = "owner"
)

func (r Role) rank() int {
	switch r {
	case RoleViewer:
		return 1
	case RoleEngineer:
		return 2
	case RoleAdmin:
		return 3
	case RoleOwner:
		return 4
	default:
		return 0
	}
}

// Includes reports whether a member with role r may do what needs role need.
func (r Role) Includes(need Role) bool {
	return r.rank() >= need.rank() && need.rank() > 0
}

// assignable roles can be granted through membership changes; ownership comes only from creating a project.
func (r Role) assignable() bool {
	return r == RoleViewer || r == RoleEngineer || r == RoleAdmin
}

type roleStore interface {
	Role(ctx context.Context, projectID, userID string) (Role, error)
}

// Access answers whether a user may act on a project. Other usecases embed it to guard their own data.
type Access struct {
	roles roleStore
}

func NewAccess(roles roleStore) *Access {
	return &Access{roles: roles}
}

// Require returns the user's role when it includes need. A user outside the project gets ErrNotFound,
// so the response does not reveal that the project exists.
func (a *Access) Require(ctx context.Context, projectID, userID string, need Role) (Role, error) {
	role, err := a.roles.Role(ctx, projectID, userID)
	if errors.Is(err, fault.ErrNotFound) {
		return "", fault.WithDetail(fault.ErrNotFound, "project not found")
	}
	if err != nil {
		return "", fmt.Errorf("load project role: %w", err)
	}
	if !role.Includes(need) {
		return "", fault.WithDetail(fault.ErrForbidden, "your project role does not allow this")
	}
	return role, nil
}
