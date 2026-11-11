// Package chat is the vocabulary usecases share to talk to a language model. Each usecase declares its
// own interface over these types; the llm package implements it.
package chat

import "errors"

// ErrNotConfigured means the deployment has no model; retrying cannot help.
var ErrNotConfigured = errors.New("no language model is configured")

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

type Message struct {
	Role Role
	Text string
}

type Request struct {
	Messages []Message
	// MaxTokens caps the reply; a reply that hits it comes back with Truncated set.
	MaxTokens int
	// JSON asks the model for a single JSON object.
	JSON bool
}

type Response struct {
	Text string
	// Truncated means the reply stopped at MaxTokens, so structured output is likely incomplete.
	Truncated bool
}
