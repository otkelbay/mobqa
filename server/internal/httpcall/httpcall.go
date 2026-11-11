// Package httpcall sends the HTTP requests of API actions from the hub.
package httpcall

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mobqa/backend/internal/usecase/action"
)

const (
	timeout = 30 * time.Second
	// maxBody is how much of a reply is kept for extraction; test endpoints answer with a few fields.
	maxBody = 1 << 20
)

type Sender struct {
	client *http.Client
}

func New() *Sender {
	return &Sender{client: &http.Client{Timeout: timeout}}
}

func (s *Sender) Send(ctx context.Context, call action.Call) (action.Reply, error) {
	body := io.Reader(http.NoBody)
	if call.Body != "" {
		body = strings.NewReader(call.Body)
	}
	request, err := http.NewRequestWithContext(ctx, string(call.Method), call.URL, body)
	if err != nil {
		return action.Reply{}, fmt.Errorf("build the request: %w", err)
	}
	for _, header := range call.Headers {
		request.Header.Add(header.Name, header.Value)
	}
	response, err := s.client.Do(request)
	if err != nil {
		return action.Reply{}, fmt.Errorf("send the request: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	reply, err := io.ReadAll(io.LimitReader(response.Body, maxBody))
	if err != nil {
		return action.Reply{}, fmt.Errorf("read the reply: %w", err)
	}
	return action.Reply{Status: response.StatusCode, Body: string(reply)}, nil
}
