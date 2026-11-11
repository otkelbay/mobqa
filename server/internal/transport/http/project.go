package http

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/project"
)

type Projects interface {
	Create(ctx context.Context, in project.CreateInput) (project.Project, error)
	List(ctx context.Context, in project.ListInput) (project.Page, error)
	Get(ctx context.Context, userID, projectID string) (project.Detail, error)
	Update(ctx context.Context, in project.UpdateInput) (project.Project, error)
	Archive(ctx context.Context, userID, projectID string) error
	AuthProfile(ctx context.Context, userID, projectID string) (project.AuthProfile, error)
	SetAuthProfile(ctx context.Context, in project.SetAuthProfileInput) (project.AuthProfile, error)
	AddMember(ctx context.Context, in project.AddMemberInput) (project.Member, error)
	SetMemberRole(ctx context.Context, in project.MemberRoleInput) error
	RemoveMember(ctx context.Context, in project.RemoveMemberInput) error
}

type createProjectRequest struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Platforms   []string `json:"platforms"`
	PackageName string   `json:"package_name"`
}

type appearanceBody struct {
	Initial   string `json:"initial"`
	Color     string `json:"color"`
	TextColor string `json:"text_color"`
}

type updateProjectRequest struct {
	Name        *string         `json:"name"`
	Description *string         `json:"description"`
	Platforms   []string        `json:"platforms"`
	PackageName *string         `json:"package_name"`
	Appearance  *appearanceBody `json:"appearance"`
}

type authProfileBody struct {
	Method      string   `json:"method"`
	LoginVar    string   `json:"login_var,omitempty"`
	PasswordVar string   `json:"password_var,omitempty"`
	OTP         *otpBody `json:"otp,omitempty"`
	SSO         *ssoBody `json:"sso,omitempty"`
	Notes       string   `json:"notes,omitempty"`
}

type otpBody struct {
	Override string `json:"override,omitempty"`
	ActionID string `json:"action_id,omitempty"`
}

type ssoBody struct {
	Provider   string `json:"provider"`
	AccountVar string `json:"account_var,omitempty"`
}

type addMemberRequest struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

type memberRoleRequest struct {
	Role string `json:"role"`
}

type projectResponse struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Platforms   []string       `json:"platforms"`
	PackageName string         `json:"package_name"`
	Appearance  appearanceBody `json:"appearance"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

type projectDetailResponse struct {
	projectResponse
	AuthProfile authProfileBody  `json:"auth_profile"`
	Role        string           `json:"role"`
	Members     []memberResponse `json:"members"`
}

type projectPageResponse struct {
	Items   []projectResponse `json:"items"`
	Total   int               `json:"total"`
	Page    int               `json:"page"`
	Limit   int               `json:"limit"`
	HasMore bool              `json:"has_more"`
}

type memberResponse struct {
	UserID    string    `json:"user_id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	AvatarURL string    `json:"avatar_url,omitempty"`
	Role      string    `json:"role"`
	JoinedAt  time.Time `json:"joined_at"`
}

func platforms(values []string) []project.Platform {
	if values == nil {
		return nil
	}
	result := make([]project.Platform, len(values))
	for i, value := range values {
		result[i] = project.Platform(value)
	}
	return result
}

func newProjectResponse(p project.Project) projectResponse {
	names := make([]string, len(p.Platforms))
	for i, platform := range p.Platforms {
		names[i] = string(platform)
	}
	return projectResponse{
		ID:          p.ID,
		Name:        p.Name,
		Description: p.Description,
		Platforms:   names,
		PackageName: p.PackageName,
		Appearance: appearanceBody{
			Initial: p.Appearance.Initial, Color: p.Appearance.Color, TextColor: p.Appearance.TextColor,
		},
		CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt,
	}
}

func newMemberResponse(m project.Member) memberResponse {
	return memberResponse{
		UserID: m.UserID, Email: m.Email, Name: m.Name, AvatarURL: m.AvatarURL, Role: string(m.Role), JoinedAt: m.JoinedAt,
	}
}

func newAuthProfileBody(p project.AuthProfile) authProfileBody {
	body := authProfileBody{Method: string(p.Method), LoginVar: p.LoginVar, PasswordVar: p.PasswordVar, Notes: p.Notes}
	if p.OTP != nil {
		body.OTP = &otpBody{Override: p.OTP.Override, ActionID: p.OTP.ActionID}
	}
	if p.SSO != nil {
		body.SSO = &ssoBody{Provider: string(p.SSO.Provider), AccountVar: p.SSO.AccountVar}
	}
	return body
}

func (b authProfileBody) profile() project.AuthProfile {
	p := project.AuthProfile{
		Method: project.AuthMethod(b.Method), LoginVar: b.LoginVar, PasswordVar: b.PasswordVar, Notes: b.Notes,
	}
	if b.OTP != nil {
		p.OTP = &project.OTPSource{Override: b.OTP.Override, ActionID: b.OTP.ActionID}
	}
	if b.SSO != nil {
		p.SSO = &project.SSO{Provider: project.SSOProvider(b.SSO.Provider), AccountVar: b.SSO.AccountVar}
	}
	return p
}

// queryInt reads an optional non-negative integer query parameter; absent means zero.
func queryInt(r *http.Request, name string) (int, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fault.WithDetail(fault.ErrInvalidInput, "page and limit must be integers")
	}
	return value, nil
}

type projectHandler struct {
	projects Projects
	logger   *slog.Logger
}

func (h projectHandler) create(w http.ResponseWriter, r *http.Request) {
	var request createProjectRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	created, err := h.projects.Create(r.Context(), project.CreateInput{
		UserID:      currentUserID(r.Context()),
		Name:        request.Name,
		Description: request.Description,
		Platforms:   platforms(request.Platforms),
		PackageName: request.PackageName,
	})
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusCreated, newProjectResponse(created))
}

func (h projectHandler) list(w http.ResponseWriter, r *http.Request) {
	page, err := queryInt(r, "page")
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	limit, err := queryInt(r, "limit")
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	found, err := h.projects.List(r.Context(), project.ListInput{
		UserID: currentUserID(r.Context()), Search: r.URL.Query().Get("search"), Page: page, Limit: limit,
	})
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	items := make([]projectResponse, len(found.Items))
	for i, item := range found.Items {
		items[i] = newProjectResponse(item)
	}
	writeJSON(w, http.StatusOK, projectPageResponse{
		Items: items, Total: found.Total, Page: found.Page, Limit: found.Limit, HasMore: found.HasMore(),
	})
}

func (h projectHandler) get(w http.ResponseWriter, r *http.Request) {
	detail, err := h.projects.Get(r.Context(), currentUserID(r.Context()), r.PathValue("projectID"))
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	members := make([]memberResponse, len(detail.Members))
	for i, member := range detail.Members {
		members[i] = newMemberResponse(member)
	}
	writeJSON(w, http.StatusOK, projectDetailResponse{
		projectResponse: newProjectResponse(detail.Project),
		AuthProfile:     newAuthProfileBody(detail.Project.AuthProfile),
		Role:            string(detail.Role),
		Members:         members,
	})
}

func (h projectHandler) update(w http.ResponseWriter, r *http.Request) {
	var request updateProjectRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	change := project.Change{
		ProjectID:   r.PathValue("projectID"),
		Name:        request.Name,
		Description: request.Description,
		Platforms:   platforms(request.Platforms),
		PackageName: request.PackageName,
	}
	if a := request.Appearance; a != nil {
		change.Appearance = &project.Appearance{Initial: a.Initial, Color: a.Color, TextColor: a.TextColor}
	}
	updated, err := h.projects.Update(r.Context(), project.UpdateInput{UserID: currentUserID(r.Context()), Change: change})
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, newProjectResponse(updated))
}

func (h projectHandler) archive(w http.ResponseWriter, r *http.Request) {
	if err := h.projects.Archive(r.Context(), currentUserID(r.Context()), r.PathValue("projectID")); err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h projectHandler) authProfile(w http.ResponseWriter, r *http.Request) {
	profile, err := h.projects.AuthProfile(r.Context(), currentUserID(r.Context()), r.PathValue("projectID"))
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, newAuthProfileBody(profile))
}

func (h projectHandler) setAuthProfile(w http.ResponseWriter, r *http.Request) {
	var request authProfileBody
	if err := decodeJSON(w, r, &request); err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	stored, err := h.projects.SetAuthProfile(r.Context(), project.SetAuthProfileInput{
		UserID: currentUserID(r.Context()), ProjectID: r.PathValue("projectID"), Profile: request.profile(),
	})
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, newAuthProfileBody(stored))
}

func (h projectHandler) addMember(w http.ResponseWriter, r *http.Request) {
	var request addMemberRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	added, err := h.projects.AddMember(r.Context(), project.AddMemberInput{
		UserID:    currentUserID(r.Context()),
		ProjectID: r.PathValue("projectID"),
		Email:     request.Email,
		Role:      project.Role(request.Role),
	})
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusCreated, newMemberResponse(added))
}

func (h projectHandler) setMemberRole(w http.ResponseWriter, r *http.Request) {
	var request memberRoleRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	err := h.projects.SetMemberRole(r.Context(), project.MemberRoleInput{
		UserID:    currentUserID(r.Context()),
		ProjectID: r.PathValue("projectID"),
		MemberID:  r.PathValue("memberID"),
		Role:      project.Role(request.Role),
	})
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h projectHandler) removeMember(w http.ResponseWriter, r *http.Request) {
	err := h.projects.RemoveMember(r.Context(), project.RemoveMemberInput{
		UserID:    currentUserID(r.Context()),
		ProjectID: r.PathValue("projectID"),
		MemberID:  r.PathValue("memberID"),
	})
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
