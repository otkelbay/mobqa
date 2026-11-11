//go:build e2e

package e2e

import (
	"net/http"
	"net/url"
	"testing"
)

type noteBody struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Source  string `json:"source"`
	Indexed bool   `json:"indexed"`
}

type matchBody struct {
	NoteID  string `json:"note_id"`
	Excerpt string `json:"excerpt"`
}

func searchPath(projectID, query string) string {
	return "/projects/" + projectID + "/knowledge/search?q=" + url.QueryEscape(query)
}

func TestKnowledgeNotesAreFoundByMeaningOnceIndexed(t *testing.T) {
	api := newAPI(t)
	ada := register(api)
	bob := registerAs(api, "bob@example.com")
	shop := createProject(api, ada.AccessToken, "Shop")
	api.do(call{method: http.MethodPost, path: "/projects/" + shop.ID + "/members", token: ada.AccessToken,
		body: map[string]string{"email": "bob@example.com", "role": "viewer"}}, http.StatusCreated, nil)
	notes := "/projects/" + shop.ID + "/knowledge"
	var otp, checkout noteBody
	api.do(call{method: http.MethodPost, path: notes, token: ada.AccessToken, body: map[string]any{
		"kind": "gotcha", "title": "Sign in code", "content": "The code arrives by SMS. Use 0000 on staging.",
	}}, http.StatusCreated, &otp)
	api.do(call{method: http.MethodPost, path: notes, token: ada.AccessToken, body: map[string]any{
		"kind": "flow", "title": "Checkout", "content": "Add the item to the cart, then pay with a saved card.",
	}}, http.StatusCreated, &checkout)
	api.do(call{method: http.MethodPost, path: notes, token: bob.AccessToken, body: map[string]any{
		"kind": "note", "title": "Theme", "content": "Dark",
	}}, http.StatusForbidden, nil)
	if otp.Indexed {
		t.Fatalf("a new note is indexed before its job ran: %+v", otp)
	}
	api.startWorker()

	found := await(api, call{method: http.MethodGet, path: searchPath(shop.ID, "sms code"), token: bob.AccessToken},
		func(m []matchBody) bool { return len(m) == 2 })
	if found[0].NoteID != otp.ID {
		t.Fatalf("matches for \"sms code\" = %+v", found)
	}
	var indexed noteBody
	api.do(call{method: http.MethodGet, path: notes + "/" + otp.ID, token: bob.AccessToken}, http.StatusOK, &indexed)
	if !indexed.Indexed {
		t.Fatalf("note = %+v, want indexed", indexed)
	}

	api.do(call{method: http.MethodPatch, path: notes + "/" + checkout.ID, token: ada.AccessToken,
		body: map[string]any{"content": "Pay by bank transfer from the cart."}}, http.StatusOK, nil)
	await(api, call{method: http.MethodGet, path: searchPath(shop.ID, "bank transfer"), token: ada.AccessToken},
		func(m []matchBody) bool { return len(m) > 0 && m[0].Excerpt == "Pay by bank transfer from the cart." })

	api.do(call{method: http.MethodDelete, path: notes + "/" + otp.ID, token: ada.AccessToken},
		http.StatusNoContent, nil)
	remaining := await(api, call{method: http.MethodGet, path: searchPath(shop.ID, "sms code"), token: ada.AccessToken},
		func(m []matchBody) bool { return len(m) == 1 })
	if remaining[0].NoteID != checkout.ID {
		t.Fatalf("matches after delete = %+v", remaining)
	}
}
