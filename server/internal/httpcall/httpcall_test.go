package httpcall_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mobqa/backend/internal/httpcall"
	"github.com/mobqa/backend/internal/usecase/action"
)

func TestSendPassesTheCallAndKeepsTheReply(t *testing.T) {
	var method, auth, body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		method, auth, body = r.Method, r.Header.Get("Authorization"), string(raw)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"code":"4821"}`))
	}))
	defer server.Close()

	reply, err := httpcall.New().Send(context.Background(), action.Call{
		Method: action.MethodPost, URL: server.URL + "/seed", Body: `{"phone":"7701"}`,
		Headers: []action.Header{{Name: "Authorization", Value: "Bearer t0ken"}},
	})

	if err != nil || reply.Status != http.StatusCreated || reply.Body != `{"code":"4821"}` {
		t.Fatalf("Send = %+v, %v", reply, err)
	}
	if method != http.MethodPost || auth != "Bearer t0ken" || body != `{"phone":"7701"}` {
		t.Fatalf("server got %s %q %q", method, auth, body)
	}
}

func TestSendReportsAnUnreachableServer(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	server.Close()

	_, err := httpcall.New().Send(context.Background(), action.Call{Method: action.MethodGet, URL: server.URL})

	if err == nil {
		t.Fatal("Send to a closed server succeeded")
	}
}
