package project

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mobqa/backend/internal/fault"
)

type AddMemberInput struct {
	UserID    string
	ProjectID string
	Email     string
	Role      Role
}

func (in AddMemberInput) Validate() error {
	if strings.TrimSpace(in.Email) == "" {
		return fault.WithDetail(fault.ErrInvalidInput, "email is required")
	}
	return validateAssignable(in.Role)
}

// AddMember gives an existing account a role in the project; it needs an admin.
func (s *Service) AddMember(ctx context.Context, in AddMemberInput) (Member, error) {
	if err := in.Validate(); err != nil {
		return Member{}, err
	}
	if _, err := s.access.Require(ctx, in.ProjectID, in.UserID, RoleAdmin); err != nil {
		return Member{}, err
	}
	added, err := s.store.AddMember(ctx, NewMember{
		ProjectID: in.ProjectID, Email: strings.ToLower(strings.TrimSpace(in.Email)), Role: in.Role,
	})
	switch {
	case errors.Is(err, fault.ErrNotFound):
		return Member{}, fault.WithDetail(fault.ErrNotFound, "no account uses this email")
	case errors.Is(err, fault.ErrConflict):
		return Member{}, fault.WithDetail(fault.ErrConflict, "this account is already a member")
	case err != nil:
		return Member{}, fmt.Errorf("add member: %w", err)
	}
	return added, nil
}

type MemberRoleInput struct {
	UserID    string
	ProjectID string
	MemberID  string
	Role      Role
}

func (in MemberRoleInput) Validate() error {
	return validateAssignable(in.Role)
}

// SetMemberRole changes a member's role. The owner's role is fixed, so a project always has its owner.
func (s *Service) SetMemberRole(ctx context.Context, in MemberRoleInput) error {
	if err := in.Validate(); err != nil {
		return err
	}
	if _, err := s.access.Require(ctx, in.ProjectID, in.UserID, RoleAdmin); err != nil {
		return err
	}
	if err := s.requireNotOwner(ctx, in.ProjectID, in.MemberID); err != nil {
		return err
	}
	if err := s.store.SetMemberRole(ctx, in.ProjectID, in.MemberID, in.Role); err != nil {
		return fmt.Errorf("set member role: %w", err)
	}
	return nil
}

type RemoveMemberInput struct {
	UserID    string
	ProjectID string
	MemberID  string
}

// RemoveMember takes a member out of the project. Admins remove others; anyone may leave, except the
// owner, who archives the project instead.
func (s *Service) RemoveMember(ctx context.Context, in RemoveMemberInput) error {
	need := RoleAdmin
	if in.MemberID == in.UserID {
		need = RoleViewer
	}
	if _, err := s.access.Require(ctx, in.ProjectID, in.UserID, need); err != nil {
		return err
	}
	if err := s.requireNotOwner(ctx, in.ProjectID, in.MemberID); err != nil {
		return err
	}
	if err := s.store.RemoveMember(ctx, in.ProjectID, in.MemberID); err != nil {
		return fmt.Errorf("remove member: %w", err)
	}
	return nil
}

func (s *Service) requireNotOwner(ctx context.Context, projectID, memberID string) error {
	role, err := s.store.Role(ctx, projectID, memberID)
	if errors.Is(err, fault.ErrNotFound) {
		return fault.WithDetail(fault.ErrNotFound, "member not found")
	}
	if err != nil {
		return fmt.Errorf("load member role: %w", err)
	}
	if role == RoleOwner {
		return fault.WithDetail(fault.ErrForbidden, "the project owner cannot be changed or removed")
	}
	return nil
}

func validateAssignable(role Role) error {
	if !role.assignable() {
		return fault.WithDetail(fault.ErrInvalidInput, "role must be admin, engineer or viewer")
	}
	return nil
}
