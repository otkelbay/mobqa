//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mobqa/backend/internal/clock/clocktest"
	"github.com/mobqa/backend/internal/dbtest"
	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/store/postgres"
	"github.com/mobqa/backend/internal/usecase/auth"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/user"
)

type projectFixture struct {
	db       *postgres.DB
	projects *postgres.Projects
	users    *postgres.Users
	now      *clocktest.Manual
}

func newProjects(t *testing.T) projectFixture {
	t.Helper()
	now := clocktest.New(testStart())
	db := dbtest.New(t, now)
	return projectFixture{db: db, projects: postgres.NewProjects(db), users: postgres.NewUsers(db), now: now}
}

func (f projectFixture) account(t *testing.T, name string) user.User {
	t.Helper()
	created, err := f.users.CreateUser(context.Background(), auth.NewUser{
		Email: name + "@example.com", Name: name, PasswordHash: []byte("hash"), Timezone: "UTC", Language: "en",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return created
}

func (f projectFixture) create(t *testing.T, ownerID, name string) project.Project {
	t.Helper()
	created, err := f.projects.CreateProject(context.Background(), project.NewProject{
		OwnerID: ownerID, Name: name, Platforms: []project.Platform{project.PlatformAndroid},
		PackageName: "com.example.app",
		Appearance:  project.Appearance{Initial: "S", Color: "#EFF4FF", TextColor: "#1D4ED8"},
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return created
}

func (f projectFixture) requireRole(t *testing.T, projectID, userID string, want project.Role) {
	t.Helper()
	role, err := f.projects.Role(context.Background(), projectID, userID)
	if err != nil || role != want {
		t.Fatalf("Role = %q, %v; want %q", role, err, want)
	}
}

func TestProjectsCreateMakesOwner(t *testing.T) {
	f := newProjects(t)
	ada := f.account(t, "ada")

	created := f.create(t, ada.ID, "Shop")

	if created.Name != "Shop" || created.AuthProfile.Method != project.AuthNone || !created.CreatedAt.Equal(testStart()) {
		t.Fatalf("CreateProject = %+v", created)
	}
	f.requireRole(t, created.ID, ada.ID, project.RoleOwner)
	found, err := f.projects.Project(context.Background(), created.ID)
	if err != nil || found.Appearance != created.Appearance || found.Platforms[0] != project.PlatformAndroid {
		t.Fatalf("Project = %+v, %v", found, err)
	}
}

func TestProjectsRoleOfOutsiderAndMalformedID(t *testing.T) {
	f := newProjects(t)
	ada, bob := f.account(t, "ada"), f.account(t, "bob")
	created := f.create(t, ada.ID, "Shop")

	for _, ids := range [][2]string{{created.ID, bob.ID}, {"not-a-uuid", ada.ID}} {
		if _, err := f.projects.Role(context.Background(), ids[0], ids[1]); !errors.Is(err, fault.ErrNotFound) {
			t.Fatalf("Role(%v) = %v, want ErrNotFound", ids, err)
		}
	}
}

func TestProjectsListPagesSearchesAndHidesOthers(t *testing.T) {
	f := newProjects(t)
	ada, bob := f.account(t, "ada"), f.account(t, "bob")
	for i := range 3 {
		f.create(t, ada.ID, fmt.Sprintf("Shop %d", i))
		f.now.Advance(time.Minute)
	}
	f.create(t, ada.ID, "100%_off")
	f.create(t, bob.ID, "Bob's")
	ctx := context.Background()

	page, total, err := f.projects.Projects(ctx, project.Query{UserID: ada.ID, Limit: 2, Offset: 1})
	if err != nil || total != 4 || len(page) != 2 || page[0].Name != "Shop 2" || page[1].Name != "Shop 1" {
		t.Fatalf("Projects = %+v, %d, %v", page, total, err)
	}
	found, total, err := f.projects.Projects(ctx, project.Query{UserID: ada.ID, Search: "%_", Limit: 10})
	if err != nil || total != 1 || found[0].Name != "100%_off" {
		t.Fatalf("Projects(search) = %+v, %d, %v", found, total, err)
	}
	past, total, err := f.projects.Projects(ctx, project.Query{UserID: ada.ID, Limit: 10, Offset: 10})
	if err != nil || total != 4 || len(past) != 0 {
		t.Fatalf("Projects(past end) = %+v, %d, %v", past, total, err)
	}
}

func TestProjectsUpdateChangesOnlyGivenFields(t *testing.T) {
	f := newProjects(t)
	created := f.create(t, f.account(t, "ada").ID, "Shop")
	f.now.Advance(time.Hour)
	name := "Store"

	updated, err := f.projects.UpdateProject(context.Background(), project.Change{
		ProjectID: created.ID, Name: &name,
		Platforms:  []project.Platform{project.PlatformIOS, project.PlatformAndroid},
		Appearance: &project.Appearance{Initial: "St", Color: "#000000", TextColor: "#FFFFFF"},
	})

	if err != nil || updated.Name != "Store" || len(updated.Platforms) != 2 ||
		updated.PackageName != "com.example.app" || updated.Appearance.Initial != "St" ||
		!updated.UpdatedAt.Equal(testStart().Add(time.Hour)) {
		t.Fatalf("UpdateProject = %+v, %v", updated, err)
	}
}

func TestProjectsArchiveHidesProject(t *testing.T) {
	f := newProjects(t)
	ada := f.account(t, "ada")
	created := f.create(t, ada.ID, "Shop")
	ctx := context.Background()

	if err := f.projects.ArchiveProject(ctx, created.ID); err != nil {
		t.Fatalf("ArchiveProject: %v", err)
	}

	if _, err := f.projects.Role(ctx, created.ID, ada.ID); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("Role after archive = %v", err)
	}
	if _, err := f.projects.Project(ctx, created.ID); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("Project after archive = %v", err)
	}
	if err := f.projects.ArchiveProject(ctx, created.ID); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("second ArchiveProject = %v", err)
	}
}

func TestProjectsAuthProfileRoundTrip(t *testing.T) {
	f := newProjects(t)
	created := f.create(t, f.account(t, "ada").ID, "Shop")
	ctx := context.Background()
	profile := project.AuthProfile{
		Method: project.AuthPhoneOTP, LoginVar: "PHONE", Notes: "test builds accept 0000",
		OTP: &project.OTPSource{Override: "0000", ActionID: "action-1"},
		SSO: &project.SSO{Provider: project.SSOGoogle, AccountVar: "GOOGLE_ACCOUNT"},
	}

	if err := f.projects.SetAuthProfile(ctx, created.ID, profile); err != nil {
		t.Fatalf("SetAuthProfile: %v", err)
	}

	found, err := f.projects.Project(ctx, created.ID)
	got := found.AuthProfile
	if err != nil || got.Method != profile.Method || got.LoginVar != "PHONE" || got.Notes != profile.Notes ||
		*got.OTP != *profile.OTP || *got.SSO != *profile.SSO {
		t.Fatalf("AuthProfile = %+v, %v", got, err)
	}
}

func TestProjectsMembers(t *testing.T) {
	f := newProjects(t)
	ada, bob := f.account(t, "ada"), f.account(t, "bob")
	created := f.create(t, ada.ID, "Shop")
	ctx := context.Background()
	f.now.Advance(time.Minute)

	added, err := f.projects.AddMember(ctx, project.NewMember{
		ProjectID: created.ID, Email: "bob@example.com", Role: project.RoleEngineer,
	})
	if err != nil || added.UserID != bob.ID || added.Name != "bob" || added.Role != project.RoleEngineer {
		t.Fatalf("AddMember = %+v, %v", added, err)
	}
	_, err = f.projects.AddMember(ctx, project.NewMember{ProjectID: created.ID, Email: "bob@example.com", Role: "viewer"})
	if !errors.Is(err, fault.ErrConflict) {
		t.Fatalf("second AddMember = %v, want ErrConflict", err)
	}
	_, err = f.projects.AddMember(ctx, project.NewMember{ProjectID: created.ID, Email: "eve@example.com", Role: "viewer"})
	if !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("AddMember(unknown) = %v, want ErrNotFound", err)
	}

	if err := f.projects.SetMemberRole(ctx, created.ID, bob.ID, project.RoleAdmin); err != nil {
		t.Fatalf("SetMemberRole: %v", err)
	}
	members, err := f.projects.Members(ctx, created.ID)
	if err != nil || len(members) != 2 || members[0].UserID != ada.ID || members[1].Role != project.RoleAdmin {
		t.Fatalf("Members = %+v, %v", members, err)
	}

	if err := f.projects.RemoveMember(ctx, created.ID, bob.ID); err != nil {
		t.Fatalf("RemoveMember: %v", err)
	}
	if err := f.projects.RemoveMember(ctx, created.ID, bob.ID); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("second RemoveMember = %v, want ErrNotFound", err)
	}
}
