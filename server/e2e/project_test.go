//go:build e2e

package e2e

import (
	"net/http"
	"testing"
)

type projectBody struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Platforms []string `json:"platforms"`
	Role      string   `json:"role"`
	Members   []struct {
		UserID string `json:"user_id"`
		Role   string `json:"role"`
	} `json:"members"`
	AuthProfile struct {
		Method   string `json:"method"`
		LoginVar string `json:"login_var"`
	} `json:"auth_profile"`
}

type projectPage struct {
	Items   []projectBody `json:"items"`
	Total   int           `json:"total"`
	HasMore bool          `json:"has_more"`
}

type environmentBody struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsDefault bool   `json:"is_default"`
	Variables []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"variables"`
}

type actionBody struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Request struct {
		Method         string `json:"method"`
		ExpectedStatus int    `json:"expected_status"`
		Extract        []struct {
			Variable string `json:"variable"`
		} `json:"extract"`
	} `json:"request"`
}

func createProject(a *api, token, name string) projectBody {
	a.t.Helper()
	var created projectBody
	a.do(call{method: http.MethodPost, path: "/projects", token: token, body: map[string]any{
		"name": name, "platforms": []string{"android"}, "package_name": "com.example.shop",
	}}, http.StatusCreated, &created)
	return created
}

func TestProjectLifecycleAndRoles(t *testing.T) {
	api := newAPI(t)
	ada := register(api)
	bob := registerAs(api, "bob@example.com")
	shop := createProject(api, ada.AccessToken, "Shop")
	path := "/projects/" + shop.ID

	var outsider errorBody
	api.do(call{method: http.MethodGet, path: path, token: bob.AccessToken}, http.StatusNotFound, &outsider)
	if outsider.Error != "project not found" {
		t.Fatalf("outsider sees %q", outsider.Error)
	}

	api.do(call{method: http.MethodPost, path: path + "/members", token: ada.AccessToken,
		body: map[string]string{"email": "Bob@Example.com", "role": "viewer"}}, http.StatusCreated, nil)
	var detail projectBody
	api.do(call{method: http.MethodGet, path: path, token: bob.AccessToken}, http.StatusOK, &detail)
	if detail.Role != "viewer" || len(detail.Members) != 2 || detail.Members[0].Role != "owner" ||
		detail.AuthProfile.Method != "none" {
		t.Fatalf("detail = %+v", detail)
	}

	rename := call{method: http.MethodPatch, path: path, token: bob.AccessToken, body: map[string]string{"name": "Mine"}}
	api.do(rename, http.StatusForbidden, nil)
	api.do(call{method: http.MethodPatch, path: path + "/members/" + bob.User.ID, token: ada.AccessToken,
		body: map[string]string{"role": "admin"}}, http.StatusNoContent, nil)
	var renamed projectBody
	api.do(rename, http.StatusOK, &renamed)
	if renamed.Name != "Mine" || renamed.Platforms[0] != "android" {
		t.Fatalf("renamed = %+v", renamed)
	}

	api.do(call{method: http.MethodDelete, path: path + "/members/" + ada.User.ID, token: bob.AccessToken},
		http.StatusForbidden, nil)
	api.do(call{method: http.MethodDelete, path: path, token: bob.AccessToken}, http.StatusForbidden, nil)
	api.do(call{method: http.MethodDelete, path: path, token: ada.AccessToken}, http.StatusNoContent, nil)
	api.do(call{method: http.MethodGet, path: path, token: ada.AccessToken}, http.StatusNotFound, nil)
}

func TestProjectListPagesAndSearches(t *testing.T) {
	api := newAPI(t)
	ada := register(api)
	for _, name := range []string{"Shop", "Bank", "Shopping list"} {
		createProject(api, ada.AccessToken, name)
	}

	var page projectPage
	api.do(call{method: http.MethodGet, path: "/projects?search=shop&limit=1", token: ada.AccessToken},
		http.StatusOK, &page)
	if page.Total != 2 || len(page.Items) != 1 || page.Items[0].Name != "Shopping list" || !page.HasMore {
		t.Fatalf("page = %+v", page)
	}
	api.do(call{method: http.MethodGet, path: "/projects?limit=many", token: ada.AccessToken},
		http.StatusBadRequest, nil)
}

func TestAuthProfileRoundTrip(t *testing.T) {
	api := newAPI(t)
	ada := register(api)
	projectPath := "/projects/" + createProject(api, ada.AccessToken, "Shop").ID
	path := projectPath + "/auth-profile"

	api.do(call{method: http.MethodPut, path: path, token: ada.AccessToken,
		body: map[string]any{"method": "password", "login_var": "1BAD"}}, http.StatusBadRequest, nil)
	api.do(call{method: http.MethodPut, path: path, token: ada.AccessToken, body: map[string]any{
		"method": "phone_otp", "login_var": "PHONE", "otp": map[string]string{"override": "0000"},
	}}, http.StatusOK, nil)

	var stored projectBody
	api.do(call{method: http.MethodGet, path: projectPath, token: ada.AccessToken}, http.StatusOK, &stored)
	if stored.AuthProfile.Method != "phone_otp" || stored.AuthProfile.LoginVar != "PHONE" {
		t.Fatalf("auth profile = %+v", stored.AuthProfile)
	}
}

func TestEnvironmentSecretsStayMasked(t *testing.T) {
	api := newAPI(t)
	ada := register(api)
	path := "/projects/" + createProject(api, ada.AccessToken, "Shop").ID + "/environments"

	var created environmentBody
	api.do(call{method: http.MethodPost, path: path, token: ada.AccessToken, body: map[string]any{
		"name": "Staging", "is_default": true, "variables": []map[string]string{
			{"name": "LOGIN", "type": "phone", "value": "+77010000000"},
			{"name": "PASSWORD", "type": "secret", "value": "hunter22"},
		},
	}}, http.StatusCreated, &created)
	if created.Variables[1].Value != "********" {
		t.Fatalf("created leaks %+v", created.Variables)
	}

	api.do(call{method: http.MethodPatch, path: path + "/" + created.ID, token: ada.AccessToken,
		body: map[string]any{"variables": []map[string]string{
			{"name": "LOGIN", "type": "phone", "value": "+77019999999"},
			{"name": "PASSWORD", "type": "secret", "value": "********"},
		}}}, http.StatusOK, nil)
	api.do(call{method: http.MethodPost, path: path, token: ada.AccessToken,
		body: map[string]any{"name": "Staging"}}, http.StatusConflict, nil)

	var listed []environmentBody
	api.do(call{method: http.MethodGet, path: path, token: ada.AccessToken}, http.StatusOK, &listed)
	if len(listed) != 1 || !listed[0].IsDefault || listed[0].Variables[0].Value != "+77019999999" ||
		listed[0].Variables[1].Value != "********" {
		t.Fatalf("listed = %+v", listed)
	}

	api.do(call{method: http.MethodDelete, path: path + "/" + created.ID, token: ada.AccessToken},
		http.StatusNoContent, nil)
	api.do(call{method: http.MethodDelete, path: path + "/" + created.ID, token: ada.AccessToken},
		http.StatusNotFound, nil)
}

func TestActionsLibrary(t *testing.T) {
	api := newAPI(t)
	ada := register(api)
	bob := registerAs(api, "bob@example.com")
	shop := createProject(api, ada.AccessToken, "Shop")
	path := "/projects/" + shop.ID + "/actions"
	api.do(call{method: http.MethodPost, path: "/projects/" + shop.ID + "/members", token: ada.AccessToken,
		body: map[string]string{"email": "bob@example.com", "role": "viewer"}}, http.StatusCreated, nil)
	fetchOTP := map[string]any{"name": "Fetch OTP", "request": map[string]any{
		"method": "GET", "url": "{{API_URL}}/test/otp?phone={{PHONE}}",
		"extract": []map[string]string{{"variable": "OTP", "pattern": `"code":"(\d+)"`}},
	}}

	var created actionBody
	api.do(call{method: http.MethodPost, path: path, token: ada.AccessToken, body: fetchOTP}, http.StatusCreated,
		&created)
	if created.Request.ExpectedStatus != 200 || len(created.Request.Extract) != 1 {
		t.Fatalf("created = %+v", created)
	}
	api.do(call{method: http.MethodPost, path: path, token: ada.AccessToken, body: fetchOTP}, http.StatusConflict,
		nil)
	api.do(call{method: http.MethodPost, path: path, token: bob.AccessToken, body: fetchOTP}, http.StatusForbidden,
		nil)
	api.do(call{method: http.MethodPost, path: path, token: ada.AccessToken, body: map[string]any{
		"name": "Local", "request": map[string]any{"method": "GET", "url": "file:///etc/passwd"},
	}}, http.StatusBadRequest, nil)

	fetchOTP["request"] = map[string]any{"method": "POST", "url": "{{API_URL}}/test/otp", "expected_status": 201}
	api.do(call{method: http.MethodPut, path: path + "/" + created.ID, token: ada.AccessToken, body: fetchOTP},
		http.StatusOK, nil)
	var listed []actionBody
	api.do(call{method: http.MethodGet, path: path, token: bob.AccessToken}, http.StatusOK, &listed)
	if len(listed) != 1 || listed[0].Request.Method != http.MethodPost || listed[0].Request.ExpectedStatus != 201 ||
		len(listed[0].Request.Extract) != 0 {
		t.Fatalf("listed = %+v", listed)
	}

	api.do(call{method: http.MethodDelete, path: path + "/" + created.ID, token: ada.AccessToken},
		http.StatusNoContent, nil)
	api.do(call{method: http.MethodGet, path: path + "/" + created.ID, token: ada.AccessToken},
		http.StatusNotFound, nil)
}
