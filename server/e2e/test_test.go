//go:build e2e

package e2e

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

type stepBody struct {
	Action string `json:"action"`
	Target string `json:"target"`
}

type testBody struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Priority  string     `json:"priority"`
	Tags      []string   `json:"tags"`
	Source    string     `json:"source"`
	Version   int        `json:"version"`
	StepCount int        `json:"step_count"`
	Steps     []stepBody `json:"steps"`
}

type versionBody struct {
	Version int    `json:"version"`
	Title   string `json:"title"`
	Source  string `json:"source"`
	Note    string `json:"note"`
}

func TestTestLifecycle(t *testing.T) {
	api := newAPI(t)
	ada := register(api)
	shop := createProject(api, ada.AccessToken, "Shop")
	tests := "/projects/" + shop.ID + "/tests"

	var created testBody
	api.do(call{method: http.MethodPost, path: tests, token: ada.AccessToken, body: map[string]any{
		"platform": "android", "title": "Sign in", "tags": []string{"Auth", "auth"},
		"steps": []stepBody{{Action: "tap", Target: "Sign in"}, {Action: "input", Target: "Phone"}},
	}}, http.StatusCreated, &created)
	if created.Version != 1 || created.Priority != "medium" || len(created.Tags) != 1 || len(created.Steps) != 2 {
		t.Fatalf("created = %+v", created)
	}
	one := tests + "/" + created.ID

	var edited testBody
	api.do(call{method: http.MethodPatch, path: one, token: ada.AccessToken, body: map[string]any{
		"base_version": 1, "title": "Sign in with phone", "priority": "critical",
	}}, http.StatusOK, &edited)
	if edited.Version != 2 || edited.Priority != "critical" || len(edited.Steps) != 2 {
		t.Fatalf("edited = %+v", edited)
	}
	api.do(call{method: http.MethodPatch, path: one, token: ada.AccessToken, body: map[string]any{
		"base_version": 1, "steps": []stepBody{},
	}}, http.StatusConflict, nil)

	var restored testBody
	api.do(call{method: http.MethodPost, path: one + "/versions/1/restore", token: ada.AccessToken},
		http.StatusOK, &restored)
	if restored.Version != 3 || restored.Title != "Sign in" || restored.Priority != "critical" {
		t.Fatalf("restored = %+v", restored)
	}
	var versions []versionBody
	api.do(call{method: http.MethodGet, path: one + "/versions", token: ada.AccessToken}, http.StatusOK, &versions)
	if len(versions) != 3 || versions[0].Source != "restore" || versions[0].Note != "restored version 1" ||
		versions[1].Title != "Sign in with phone" {
		t.Fatalf("versions = %+v", versions)
	}

	var duplicate testBody
	api.do(call{method: http.MethodPost, path: one + "/duplicate", token: ada.AccessToken},
		http.StatusCreated, &duplicate)
	if duplicate.Title != "Sign in (copy)" || duplicate.Version != 1 || duplicate.Source != "duplicate" {
		t.Fatalf("duplicate = %+v", duplicate)
	}
	var page struct {
		Items []testBody `json:"items"`
		Total int        `json:"total"`
	}
	api.do(call{method: http.MethodGet, path: tests + "?search=copy", token: ada.AccessToken}, http.StatusOK, &page)
	if page.Total != 1 || page.Items[0].ID != duplicate.ID || page.Items[0].StepCount != 2 {
		t.Fatalf("search = %+v", page)
	}

	api.do(call{method: http.MethodDelete, path: one, token: ada.AccessToken}, http.StatusNoContent, nil)
	api.do(call{method: http.MethodGet, path: one, token: ada.AccessToken}, http.StatusNotFound, nil)
}

func TestTestRoles(t *testing.T) {
	api := newAPI(t)
	ada := register(api)
	bob := registerAs(api, "bob@example.com")
	eve := registerAs(api, "eve@example.com")
	shop := createProject(api, ada.AccessToken, "Shop")
	tests := "/projects/" + shop.ID + "/tests"
	api.do(call{method: http.MethodPost, path: "/projects/" + shop.ID + "/members", token: ada.AccessToken,
		body: map[string]string{"email": "bob@example.com", "role": "viewer"}}, http.StatusCreated, nil)
	var created testBody
	api.do(call{method: http.MethodPost, path: tests, token: ada.AccessToken,
		body: map[string]any{"platform": "ios", "title": "Checkout"}}, http.StatusCreated, &created)

	api.do(call{method: http.MethodGet, path: tests + "/" + created.ID, token: bob.AccessToken}, http.StatusOK, nil)
	api.do(call{method: http.MethodPost, path: tests, token: bob.AccessToken,
		body: map[string]any{"platform": "ios", "title": "Refund"}}, http.StatusForbidden, nil)
	api.do(call{method: http.MethodDelete, path: tests + "/" + created.ID, token: bob.AccessToken},
		http.StatusForbidden, nil)
	api.do(call{method: http.MethodGet, path: tests, token: eve.AccessToken}, http.StatusNotFound, nil)
}

type generationBody struct {
	ID      string   `json:"id"`
	Status  string   `json:"status"`
	TestIDs []string `json:"test_ids"`
	Failure string   `json:"failure"`
}

// awaitGeneration polls a generation until the worker has finished it.
func awaitGeneration(a *api, c call) generationBody {
	a.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var found generationBody
		a.do(c, http.StatusOK, &found)
		if found.Status != "pending" {
			return found
		}
		if time.Now().After(deadline) {
			a.t.Fatalf("generation %s never finished", found.ID)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestGenerateTestsFromBrief(t *testing.T) {
	api := newAPI(t)
	ada := register(api)
	shop := createProject(api, ada.AccessToken, "Shop")
	api.model.answer(`{"tests":[{"title":"Sign in","priority":"high","tags":["auth"],"steps":[` +
		`{"action":"input","target":"Phone"},{"action":"tap","target":"Continue"},` +
		`{"action":"assert","target":"Home","expected_result":"Home opens"}]}]}`)
	api.startWorker()

	var pending generationBody
	api.do(call{method: http.MethodPost, path: "/projects/" + shop.ID + "/test-generations", token: ada.AccessToken,
		body: map[string]string{"platform": "android", "brief": "Sign in with a phone number"}},
		http.StatusAccepted, &pending)
	done := awaitGeneration(api, call{method: http.MethodGet,
		path: "/projects/" + shop.ID + "/test-generations/" + pending.ID, token: ada.AccessToken})

	if done.Status != "succeeded" || len(done.TestIDs) != 1 {
		t.Fatalf("generation = %+v", done)
	}
	var generated testBody
	api.do(call{method: http.MethodGet, path: "/projects/" + shop.ID + "/tests/" + done.TestIDs[0],
		token: ada.AccessToken}, http.StatusOK, &generated)
	if generated.Title != "Sign in" || generated.Source != "generated" || len(generated.Steps) != 3 {
		t.Fatalf("generated test = %+v", generated)
	}
}

func TestGenerationWithUnusableReplyFails(t *testing.T) {
	api := newAPI(t)
	ada := register(api)
	shop := createProject(api, ada.AccessToken, "Shop")
	api.model.answer("I cannot help with that.")
	api.startWorker()

	var pending generationBody
	api.do(call{method: http.MethodPost, path: "/projects/" + shop.ID + "/test-generations", token: ada.AccessToken,
		body: map[string]string{"platform": "ios", "brief": "Checkout"}}, http.StatusAccepted, &pending)
	done := awaitGeneration(api, call{method: http.MethodGet,
		path: "/projects/" + shop.ID + "/test-generations/" + pending.ID, token: ada.AccessToken})

	if done.Status != "failed" || done.Failure != "the model returned no usable tests" || len(done.TestIDs) != 0 {
		t.Fatalf("generation = %+v", done)
	}
}

type proposalBody struct {
	Reply       string     `json:"reply"`
	Changed     bool       `json:"changed"`
	Steps       []stepBody `json:"steps"`
	BaseVersion int        `json:"base_version"`
}

func TestAgentEditProposesStepsGroundedInKnowledge(t *testing.T) {
	api := newAPI(t)
	ada := register(api)
	shop := createProject(api, ada.AccessToken, "Shop")
	api.do(call{method: http.MethodPost, path: "/projects/" + shop.ID + "/knowledge", token: ada.AccessToken,
		body: map[string]any{"kind": "gotcha", "title": "Sign in code", "content": "Use 0000 on staging.", "pinned": true},
	}, http.StatusCreated, nil)
	var created testBody
	api.do(call{method: http.MethodPost, path: "/projects/" + shop.ID + "/tests", token: ada.AccessToken,
		body: map[string]any{
			"platform": "android", "title": "Sign in", "steps": []stepBody{{Action: "tap", Target: "Sign in"}},
		},
	}, http.StatusCreated, &created)
	one := "/projects/" + shop.ID + "/tests/" + created.ID
	api.model.answer(`{"reply":"Added the code.","changed":true,"steps":[` +
		`{"action":"tap","target":"Sign in"},{"action":"input","target":"0000"}]}`)

	var proposal proposalBody
	api.do(call{method: http.MethodPost, path: one + "/agent-edit", token: ada.AccessToken, body: map[string]any{
		"instruction": "Enter the sign in code", "history": []map[string]string{{"role": "user", "content": "Hi"}},
	}}, http.StatusOK, &proposal)

	if !proposal.Changed || proposal.BaseVersion != 1 || len(proposal.Steps) != 2 || proposal.Reply != "Added the code." {
		t.Fatalf("proposal = %+v", proposal)
	}
	if prompt := api.model.lastRequest().Messages[0].Text; !strings.Contains(prompt, "Use 0000 on staging.") {
		t.Fatalf("system prompt lacks the pinned note: %s", prompt)
	}
	var unchanged testBody
	api.do(call{method: http.MethodGet, path: one, token: ada.AccessToken}, http.StatusOK, &unchanged)
	if unchanged.Version != 1 || len(unchanged.Steps) != 1 {
		t.Fatalf("proposing saved the test: %+v", unchanged)
	}
	var saved testBody
	api.do(call{method: http.MethodPatch, path: one, token: ada.AccessToken, body: map[string]any{
		"base_version": proposal.BaseVersion, "steps": proposal.Steps,
	}}, http.StatusOK, &saved)
	if saved.Version != 2 || len(saved.Steps) != 2 {
		t.Fatalf("saved = %+v", saved)
	}
}
