// Package llm talks to an OpenAI-compatible provider (OpenRouter by default) through langchaingo. It
// implements the chat interfaces usecases declare and keeps langchaingo out of every other package.
package llm

import (
	"context"
	"fmt"
	"strings"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/openai"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/chat"
)

// stopLength is the finish reason OpenAI-compatible APIs report when a reply hits its token cap.
const stopLength = "length"

type Config struct {
	BaseURL string
	APIKey  string
	Model   string
	// EmbeddingModel turns text into vectors for retrieval.
	EmbeddingModel string
}

type Client struct {
	model *openai.LLM
}

// New returns a client for the configured provider, or one that fails every request when cfg has no
// API key, so a deployment without a model still starts and reports the gap per request.
func New(cfg Config) (*Client, error) {
	if cfg.APIKey == "" {
		return &Client{}, nil
	}
	model, err := openai.New(openai.WithBaseURL(cfg.BaseURL), openai.WithToken(cfg.APIKey), openai.WithModel(cfg.Model),
		openai.WithEmbeddingModel(cfg.EmbeddingModel))
	if err != nil {
		return nil, fmt.Errorf("create %s client: %w", cfg.Model, err)
	}
	return &Client{model: model}, nil
}

func (c *Client) Chat(ctx context.Context, request chat.Request) (chat.Response, error) {
	if c.model == nil {
		return chat.Response{}, fmt.Errorf("%w: %w", fault.ErrUnavailable, chat.ErrNotConfigured)
	}
	messages := make([]llms.MessageContent, len(request.Messages))
	for i, message := range request.Messages {
		messages[i] = llms.TextParts(messageType(message.Role), message.Text)
	}
	// OpenRouter documents max_tokens; langchaingo otherwise sends OpenAI's newer max_completion_tokens.
	options := []llms.CallOption{
		llms.WithMaxTokens(request.MaxTokens), openai.WithLegacyMaxTokensField(), llms.WithTemperature(0),
	}
	if request.JSON {
		options = append(options, llms.WithJSONMode())
	}
	response, err := c.model.GenerateContent(ctx, messages, options...)
	if err != nil {
		return chat.Response{}, fmt.Errorf("%w: generate content: %w", fault.ErrUnavailable, err)
	}
	if len(response.Choices) == 0 {
		return chat.Response{}, fmt.Errorf("%w: the model returned no choices", fault.ErrUnavailable)
	}
	choice := response.Choices[0]
	return chat.Response{Text: choice.Content, Truncated: strings.EqualFold(choice.StopReason, stopLength)}, nil
}

// Embed returns one vector per text, in the order of texts.
func (c *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if c.model == nil {
		return nil, fmt.Errorf("%w: %w", fault.ErrUnavailable, chat.ErrNotConfigured)
	}
	vectors, err := c.model.CreateEmbedding(ctx, texts)
	if err != nil {
		return nil, fmt.Errorf("%w: create embeddings: %w", fault.ErrUnavailable, err)
	}
	return vectors, nil
}

func messageType(role chat.Role) llms.ChatMessageType {
	switch role {
	case chat.RoleSystem:
		return llms.ChatMessageTypeSystem
	case chat.RoleAssistant:
		return llms.ChatMessageTypeAI
	default:
		return llms.ChatMessageTypeHuman
	}
}
