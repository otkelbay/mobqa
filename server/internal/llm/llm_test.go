package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/llm"
	"github.com/mobqa/backend/internal/usecase/chat"
)

type completionRequest struct {
	Model          string `json:"model"`
	MaxTokens      int    `json:"max_tokens"`
	ResponseFormat *struct {
		Type string `json:"type"`
	} `json:"response_format"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

type provider struct {
	status       int
	finishReason string
	received     []completionRequest
	authorized   []string
}

func (p *provider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var request completionRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	p.received = append(p.received, request)
	p.authorized = append(p.authorized, r.Header.Get("Authorization"))
	if p.status != 0 {
		http.Error(w, `{"error":{"message":"overloaded"}}`, p.status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": "c1", "object": "chat.completion", "model": request.Model,
		"choices": []map[string]any{{
			"index": 0, "finish_reason": p.finishReason,
			"message": map[string]any{"role": "assistant", "content": `{"tests":[]}`},
		}},
	})
}

type embeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// embedder answers embedding requests with a vector per input whose only value is the input's length.
type embedder struct {
	status   int
	received []embeddingRequest
}

func (e *embedder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var request embeddingRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	e.received = append(e.received, request)
	if e.status != 0 {
		http.Error(w, `{"error":{"message":"overloaded"}}`, e.status)
		return
	}
	data := make([]map[string]any, len(request.Input))
	for i, input := range request.Input {
		data[i] = map[string]any{"object": "embedding", "index": i, "embedding": []float32{float32(len(input))}}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "model": request.Model, "data": data})
}

func client(t *testing.T, p http.Handler) *llm.Client {
	t.Helper()
	server := httptest.NewServer(p)
	t.Cleanup(server.Close)
	c, err := llm.New(llm.Config{
		BaseURL: server.URL, APIKey: "sk-test", Model: "vendor/model", EmbeddingModel: "vendor/embedder",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestChatSendsConversationAndReadsReply(t *testing.T) {
	p := &provider{finishReason: "stop"}

	response, err := client(t, p).Chat(context.Background(), chat.Request{
		Messages: []chat.Message{
			{Role: chat.RoleSystem, Text: "You write tests."},
			{Role: chat.RoleUser, Text: "Sign in"},
		},
		MaxTokens: 500, JSON: true,
	})

	if err != nil || response.Text != `{"tests":[]}` || response.Truncated {
		t.Fatalf("Chat = %+v, %v", response, err)
	}
	sent := p.received[0]
	if sent.Model != "vendor/model" || sent.MaxTokens != 500 || sent.ResponseFormat == nil ||
		sent.ResponseFormat.Type != "json_object" || p.authorized[0] != "Bearer sk-test" {
		t.Fatalf("request = %+v, authorization %q", sent, p.authorized[0])
	}
	if len(sent.Messages) != 2 || sent.Messages[0].Role != "system" || sent.Messages[1].Content != "Sign in" {
		t.Fatalf("messages = %+v", sent.Messages)
	}
}

func TestChatReportsTruncation(t *testing.T) {
	response, err := client(t, &provider{finishReason: "length"}).Chat(context.Background(), chat.Request{
		Messages: []chat.Message{{Role: chat.RoleUser, Text: "Sign in"}}, MaxTokens: 10,
	})

	if err != nil || !response.Truncated {
		t.Fatalf("Chat = %+v, %v, want truncated", response, err)
	}
}

func TestChatProviderFailureIsUnavailable(t *testing.T) {
	_, err := client(t, &provider{status: http.StatusBadRequest}).Chat(context.Background(), chat.Request{
		Messages: []chat.Message{{Role: chat.RoleUser, Text: "Sign in"}}, MaxTokens: 10,
	})

	if !errors.Is(err, fault.ErrUnavailable) {
		t.Fatalf("Chat = %v, want ErrUnavailable", err)
	}
}

func TestChatWithoutKeyIsNotConfigured(t *testing.T) {
	c, err := llm.New(llm.Config{Model: "vendor/model"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = c.Chat(context.Background(), chat.Request{Messages: []chat.Message{{Role: chat.RoleUser, Text: "hi"}}})

	if !errors.Is(err, fault.ErrUnavailable) || !errors.Is(err, chat.ErrNotConfigured) {
		t.Fatalf("Chat = %v, want ErrUnavailable and ErrNotConfigured", err)
	}
}

func TestEmbedReturnsVectorPerTextInOrder(t *testing.T) {
	e := &embedder{}

	vectors, err := client(t, e).Embed(context.Background(), []string{"sign in", "pay"})

	if err != nil || len(vectors) != 2 || vectors[0][0] != 7 || vectors[1][0] != 3 {
		t.Fatalf("Embed = %v, %v", vectors, err)
	}
	if e.received[0].Model != "vendor/embedder" || len(e.received[0].Input) != 2 {
		t.Fatalf("request = %+v", e.received[0])
	}
}

func TestEmbedProviderFailureIsUnavailable(t *testing.T) {
	_, err := client(t, &embedder{status: http.StatusTooManyRequests}).Embed(context.Background(), []string{"pay"})

	if !errors.Is(err, fault.ErrUnavailable) {
		t.Fatalf("Embed = %v, want ErrUnavailable", err)
	}
}

func TestEmbedWithoutKeyIsNotConfigured(t *testing.T) {
	c, err := llm.New(llm.Config{Model: "vendor/model"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = c.Embed(context.Background(), []string{"pay"})

	if !errors.Is(err, fault.ErrUnavailable) || !errors.Is(err, chat.ErrNotConfigured) {
		t.Fatalf("Embed = %v, want ErrUnavailable and ErrNotConfigured", err)
	}
}
