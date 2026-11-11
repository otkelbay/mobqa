package project_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/project"
)

const (
	owner    = "user-owner"
	admin    = "user-admin"
	engineer = "user-engineer"
	viewer   = "user-viewer"
	outsider = "user-outsider"
	projID   = "project-1"
)

type fakeStore struct {
	projects map[string]project.Project
	members  map[string]map[string]project.Role
	accounts map[string]string
	changes  []project.Change
	profiles []project.AuthProfile
	archived []string
	removed  []string
	queries  []project.Query
}

// newFakeStore holds one project with a member of every role and an account that is not a member.
func newFakeStore() *fakeStore {
	return &fakeStore{
		projects: map[string]project.Project{projID: {ID: projID, Name: "Shop"}},
		members: map[string]map[string]project.Role{projID: {
			owner: project.RoleOwner, admin: project.RoleAdmin, engineer: project.RoleEngineer, viewer: project.RoleViewer,
		}},
		accounts: map[string]string{"bob@example.com": outsider, "eve@example.com": engineer},
	}
}

func (s *fakeStore) Role(_ context.Context, projectID, userID string) (project.Role, error) {
	role, ok := s.members[projectID][userID]
	if !ok {
		return "", fault.ErrNotFound
	}
	return role, nil
}

func (s *fakeStore) CreateProject(_ context.Context, in project.NewProject) (project.Project, error) {
	created := project.Project{
		ID: fmt.Sprintf("project-%d", len(s.projects)+1), Name: in.Name, Description: in.Description,
		Platforms: in.Platforms, PackageName: in.PackageName, Appearance: in.Appearance,
	}
	s.projects[created.ID] = created
	s.members[created.ID] = map[string]project.Role{in.OwnerID: project.RoleOwner}
	return created, nil
}

func (s *fakeStore) Projects(_ context.Context, query project.Query) ([]project.Project, int, error) {
	s.queries = append(s.queries, query)
	return []project.Project{s.projects[projID]}, 41, nil
}

func (s *fakeStore) Project(_ context.Context, id string) (project.Project, error) {
	found, ok := s.projects[id]
	if !ok {
		return project.Project{}, fault.ErrNotFound
	}
	return found, nil
}

func (s *fakeStore) UpdateProject(_ context.Context, change project.Change) (project.Project, error) {
	s.changes = append(s.changes, change)
	return s.projects[change.ProjectID], nil
}

func (s *fakeStore) ArchiveProject(_ context.Context, id string) error {
	s.archived = append(s.archived, id)
	return nil
}

func (s *fakeStore) SetAuthProfile(_ context.Context, _ string, profile project.AuthProfile) error {
	s.profiles = append(s.profiles, profile)
	return nil
}

func (s *fakeStore) Members(_ context.Context, projectID string) ([]project.Member, error) {
	var members []project.Member
	for userID, role := range s.members[projectID] {
		members = append(members, project.Member{UserID: userID, Role: role})
	}
	return members, nil
}

func (s *fakeStore) AddMember(_ context.Context, in project.NewMember) (project.Member, error) {
	userID, ok := s.accounts[in.Email]
	if !ok {
		return project.Member{}, fault.ErrNotFound
	}
	if _, member := s.members[in.ProjectID][userID]; member {
		return project.Member{}, fault.ErrConflict
	}
	s.members[in.ProjectID][userID] = in.Role
	return project.Member{UserID: userID, Email: in.Email, Role: in.Role}, nil
}

func (s *fakeStore) SetMemberRole(_ context.Context, projectID, userID string, role project.Role) error {
	s.members[projectID][userID] = role
	return nil
}

func (s *fakeStore) RemoveMember(_ context.Context, projectID, userID string) error {
	delete(s.members[projectID], userID)
	s.removed = append(s.removed, userID)
	return nil
}

func requireKind(t *testing.T, err, kind error, detail string) {
	t.Helper()
	if !errors.Is(err, kind) {
		t.Fatalf("error = %v, want %v", err, kind)
	}
	if got, _ := fault.Detail(err); got != detail {
		t.Fatalf("detail = %q, want %q", got, detail)
	}
}

func pointer[T any](value T) *T {
	return &value
}

func TestCreateMakesCreatorOwnerWithDefaultAppearance(t *testing.T) {
	store := newFakeStore()
	service := project.NewService(store)

	created, err := service.Create(context.Background(), project.CreateInput{
		UserID: "user-new", Name: "élan", Platforms: []project.Platform{project.PlatformIOS}, PackageName: "com.elan.app",
	})

	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	want := project.Appearance{Initial: "É", Color: "#EFF4FF", TextColor: "#1D4ED8"}
	if created.Appearance != want || created.PackageName != "com.elan.app" {
		t.Fatalf("created = %+v", created)
	}
	if store.members[created.ID]["user-new"] != project.RoleOwner {
		t.Fatal("creator is not the owner")
	}
}

func TestCreateValidatesInput(t *testing.T) {
	android := []project.Platform{project.PlatformAndroid}
	tests := []struct {
		name   string
		input  project.CreateInput
		detail string
	}{
		{
			name:   "empty name",
			input:  project.CreateInput{Platforms: android},
			detail: "name must be 1 to 80 characters without surrounding spaces",
		},
		{
			name:   "no platform",
			input:  project.CreateInput{Name: "Shop"},
			detail: "choose at least one platform",
		},
		{
			name:   "unknown platform",
			input:  project.CreateInput{Name: "Shop", Platforms: []project.Platform{"web"}},
			detail: "platform must be android or ios",
		},
		{
			name:   "repeated platform",
			input:  project.CreateInput{Name: "Shop", Platforms: []project.Platform{"ios", "ios"}},
			detail: "each platform may be listed once",
		},
		{
			name:   "bad package name",
			input:  project.CreateInput{Name: "Shop", Platforms: android, PackageName: "shop"},
			detail: "package name must be a reverse-DNS id such as com.example.app",
		},
		{
			name:   "long description",
			input:  project.CreateInput{Name: "Shop", Platforms: android, Description: strings.Repeat("d", 2001)},
			detail: "description must be at most 2000 characters",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.input.UserID = owner

			_, err := project.NewService(newFakeStore()).Create(context.Background(), tt.input)

			requireKind(t, err, fault.ErrInvalidInput, tt.detail)
		})
	}
}

func TestListAppliesPaging(t *testing.T) {
	tests := []struct {
		name      string
		input     project.ListInput
		wantQuery project.Query
		wantPage  int
		wantLimit int
		wantMore  bool
	}{
		{
			name:      "defaults",
			input:     project.ListInput{UserID: owner},
			wantQuery: project.Query{UserID: owner, Limit: 20},
			wantPage:  1, wantLimit: 20, wantMore: true,
		},
		{
			name:      "third page with trimmed search",
			input:     project.ListInput{UserID: owner, Search: " shop ", Page: 3, Limit: 20},
			wantQuery: project.Query{UserID: owner, Search: "shop", Limit: 20, Offset: 40},
			wantPage:  3, wantLimit: 20, wantMore: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeStore()

			page, err := project.NewService(store).List(context.Background(), tt.input)

			if err != nil || store.queries[0] != tt.wantQuery {
				t.Fatalf("List: %v, query %+v, want %+v", err, store.queries, tt.wantQuery)
			}
			if page.Page != tt.wantPage || page.Limit != tt.wantLimit || page.Total != 41 || page.HasMore() != tt.wantMore {
				t.Fatalf("page = %+v, has more %v", page, page.HasMore())
			}
		})
	}
}

func TestListRejectsOversizedPage(t *testing.T) {
	_, err := project.NewService(newFakeStore()).List(context.Background(),
		project.ListInput{UserID: owner, Limit: 101})

	requireKind(t, err, fault.ErrInvalidInput, "page must be positive and limit at most 100")
}

func TestRoleRequirements(t *testing.T) {
	tests := []struct {
		name   string
		userID string
		call   string
		kind   error
	}{
		{name: "outsider cannot see", userID: outsider, call: "get", kind: fault.ErrNotFound},
		{name: "viewer can see", userID: viewer, call: "get"},
		{name: "viewer reads auth profile", userID: viewer, call: "auth profile"},
		{name: "viewer cannot edit auth profile", userID: viewer, call: "set auth profile", kind: fault.ErrForbidden},
		{name: "engineer edits auth profile", userID: engineer, call: "set auth profile"},
		{name: "engineer cannot change settings", userID: engineer, call: "update", kind: fault.ErrForbidden},
		{name: "admin changes settings", userID: admin, call: "update"},
		{name: "admin cannot archive", userID: admin, call: "archive", kind: fault.ErrForbidden},
		{name: "owner archives", userID: owner, call: "archive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := project.NewService(newFakeStore())

			err := callAs(service, tt.call, tt.userID)

			if tt.kind == nil && err != nil {
				t.Fatalf("%s: %v", tt.call, err)
			}
			if tt.kind != nil && !errors.Is(err, tt.kind) {
				t.Fatalf("%s = %v, want %v", tt.call, err, tt.kind)
			}
		})
	}
}

func callAs(service *project.Service, call, userID string) error {
	ctx := context.Background()
	var err error
	switch call {
	case "get":
		_, err = service.Get(ctx, userID, projID)
	case "auth profile":
		_, err = service.AuthProfile(ctx, userID, projID)
	case "set auth profile":
		_, err = service.SetAuthProfile(ctx, project.SetAuthProfileInput{
			UserID: userID, ProjectID: projID, Profile: project.AuthProfile{Method: project.AuthPassword},
		})
	case "update":
		_, err = service.Update(ctx, project.UpdateInput{
			UserID: userID, Change: project.Change{ProjectID: projID, Name: pointer("Shop 2")},
		})
	case "archive":
		err = service.Archive(ctx, userID, projID)
	}
	return err
}

func TestGetReturnsRoleAndMembers(t *testing.T) {
	detail, err := project.NewService(newFakeStore()).Get(context.Background(), engineer, projID)

	if err != nil || detail.Role != project.RoleEngineer || detail.Project.Name != "Shop" || len(detail.Members) != 4 {
		t.Fatalf("Get = %+v, %v", detail, err)
	}
}

func TestUpdateValidatesOnlyGivenFields(t *testing.T) {
	store := newFakeStore()
	service := project.NewService(store)

	_, err := service.Update(context.Background(), project.UpdateInput{UserID: admin, Change: project.Change{
		ProjectID: projID, Appearance: &project.Appearance{Initial: "SHO", Color: "#000000", TextColor: "#FFFFFF"},
	}})

	requireKind(t, err, fault.ErrInvalidInput, "initial must be one or two characters")
	if len(store.changes) != 0 {
		t.Fatal("invalid change reached the store")
	}
}

func TestSetAuthProfileValidates(t *testing.T) {
	tests := []struct {
		name    string
		profile project.AuthProfile
		detail  string
	}{
		{name: "unknown method", profile: project.AuthProfile{Method: "magic"}, detail: "unknown sign-in method"},
		{
			name:    "bad variable name",
			profile: project.AuthProfile{Method: project.AuthPassword, LoginVar: "LOGIN-NAME"},
			detail:  "variable names must be letters, digits and underscores",
		},
		{
			name:    "unknown sso provider",
			profile: project.AuthProfile{Method: project.AuthGoogleSSO, SSO: &project.SSO{Provider: "myspace"}},
			detail:  "unknown sso provider",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := project.NewService(newFakeStore()).SetAuthProfile(context.Background(),
				project.SetAuthProfileInput{UserID: owner, ProjectID: projID, Profile: tt.profile})

			requireKind(t, err, fault.ErrInvalidInput, tt.detail)
		})
	}
}

func TestSetAuthProfileStoresFullRecipe(t *testing.T) {
	store := newFakeStore()
	profile := project.AuthProfile{
		Method: project.AuthPhoneOTP, LoginVar: "PHONE",
		OTP:   &project.OTPSource{Override: "1111", ActionID: "action-1"},
		Notes: "skip the promo carousel",
	}

	got, err := project.NewService(store).SetAuthProfile(context.Background(),
		project.SetAuthProfileInput{UserID: engineer, ProjectID: projID, Profile: profile})

	if err != nil || got.Method != project.AuthPhoneOTP || len(store.profiles) != 1 ||
		store.profiles[0].OTP.Override != "1111" {
		t.Fatalf("SetAuthProfile = %+v, %v", got, err)
	}
}

func TestAddMember(t *testing.T) {
	tests := []struct {
		name   string
		userID string
		input  project.AddMemberInput
		kind   error
		detail string
	}{
		{name: "admin adds by email", userID: admin, input: project.AddMemberInput{Email: " Bob@Example.com ",
			Role: project.RoleEngineer}},
		{name: "unknown email", userID: admin, input: project.AddMemberInput{Email: "zed@example.com",
			Role: project.RoleViewer}, kind: fault.ErrNotFound, detail: "no account uses this email"},
		{name: "already member", userID: admin, input: project.AddMemberInput{Email: "eve@example.com",
			Role: project.RoleViewer}, kind: fault.ErrConflict, detail: "this account is already a member"},
		{name: "owner role not grantable", userID: owner, input: project.AddMemberInput{Email: "bob@example.com",
			Role: project.RoleOwner}, kind: fault.ErrInvalidInput, detail: "role must be admin, engineer or viewer"},
		{name: "engineer cannot add", userID: engineer, input: project.AddMemberInput{Email: "bob@example.com",
			Role: project.RoleViewer}, kind: fault.ErrForbidden, detail: "your project role does not allow this"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeStore()
			tt.input.UserID = tt.userID
			tt.input.ProjectID = projID

			member, err := project.NewService(store).AddMember(context.Background(), tt.input)

			if tt.kind != nil {
				requireKind(t, err, tt.kind, tt.detail)
				return
			}
			if err != nil || member.UserID != outsider || store.members[projID][outsider] != project.RoleEngineer {
				t.Fatalf("AddMember = %+v, %v", member, err)
			}
		})
	}
}

func TestOwnerCannotBeDemotedOrRemoved(t *testing.T) {
	store := newFakeStore()
	service := project.NewService(store)
	ctx := context.Background()

	roleErr := service.SetMemberRole(ctx,
		project.MemberRoleInput{UserID: admin, ProjectID: projID, MemberID: owner, Role: project.RoleViewer})
	leaveErr := service.RemoveMember(ctx, project.RemoveMemberInput{UserID: owner, ProjectID: projID, MemberID: owner})

	for _, err := range []error{roleErr, leaveErr} {
		requireKind(t, err, fault.ErrForbidden, "the project owner cannot be changed or removed")
	}
	if store.members[projID][owner] != project.RoleOwner {
		t.Fatal("owner role changed")
	}
}

func TestRemoveMember(t *testing.T) {
	tests := []struct {
		name     string
		userID   string
		memberID string
		kind     error
	}{
		{name: "admin removes engineer", userID: admin, memberID: engineer},
		{name: "viewer leaves", userID: viewer, memberID: viewer},
		{name: "engineer cannot remove viewer", userID: engineer, memberID: viewer, kind: fault.ErrForbidden},
		{name: "unknown member", userID: admin, memberID: outsider, kind: fault.ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeStore()

			err := project.NewService(store).RemoveMember(context.Background(),
				project.RemoveMemberInput{UserID: tt.userID, ProjectID: projID, MemberID: tt.memberID})

			if tt.kind != nil {
				if !errors.Is(err, tt.kind) {
					t.Fatalf("RemoveMember = %v, want %v", err, tt.kind)
				}
				return
			}
			if err != nil || !slices.Equal(store.removed, []string{tt.memberID}) {
				t.Fatalf("RemoveMember = %v, removed %v", err, store.removed)
			}
		})
	}
}

func TestSetMemberRole(t *testing.T) {
	store := newFakeStore()

	err := project.NewService(store).SetMemberRole(context.Background(),
		project.MemberRoleInput{UserID: admin, ProjectID: projID, MemberID: viewer, Role: project.RoleAdmin})

	if err != nil || store.members[projID][viewer] != project.RoleAdmin {
		t.Fatalf("SetMemberRole = %v, role %s", err, store.members[projID][viewer])
	}
}
