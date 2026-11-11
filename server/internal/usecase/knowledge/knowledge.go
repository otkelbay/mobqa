// Package knowledge is what the agent knows about a project beyond its tests: notes that people and the
// agent write about flows, gotchas and selectors. Notes are split into chunks, embedded in the
// background and retrieved by meaning, so prompts carry the few notes that matter instead of all of them.
package knowledge

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mobqa/backend/internal/fault"
)

// Dimensions is the embedding size the store keeps; the embedding model must produce exactly this many.
const Dimensions = 1536

const (
	maxTitleLength   = 200
	maxContentLength = 20000
	// chunkSize keeps a chunk to a few paragraphs: small enough to be about one thing, large enough to
	// make sense on its own.
	chunkSize = 1000
	separator = "\n\n"
)

var errDimensions = errors.New("the embedding model returned vectors of the wrong size")

type Kind string

const (
	KindNote     Kind = "note"
	KindFlow     Kind = "flow"
	KindGotcha   Kind = "gotcha"
	KindSelector Kind = "selector"
)

// Source tells notes people wrote from what the agent learned on its own.
type Source string

const (
	SourceUser  Source = "user"
	SourceAgent Source = "agent"
)

type Note struct {
	ID        string
	ProjectID string
	Kind      Kind
	Title     string
	Content   string
	Source    Source
	// Pinned notes go into every prompt, whatever it is about.
	Pinned bool
	// Indexed means the current text has been embedded and can be found by meaning.
	Indexed   bool
	CreatedBy string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Match is a chunk of a note found by meaning; Similarity is the cosine similarity to the query.
type Match struct {
	NoteID     string
	Kind       Kind
	Title      string
	Excerpt    string
	Similarity float64
}

func validateKind(kind Kind) error {
	switch kind {
	case KindNote, KindFlow, KindGotcha, KindSelector:
		return nil
	default:
		return fault.WithDetail(fault.ErrInvalidInput, "kind must be note, flow, gotcha or selector")
	}
}

func validateTitle(title string) error {
	if strings.TrimSpace(title) == "" || utf8.RuneCountInString(title) > maxTitleLength {
		return fault.WithDetail(fault.ErrInvalidInput, "title must be 1 to 200 characters")
	}
	return nil
}

func validateContent(content string) error {
	if strings.TrimSpace(content) == "" || utf8.RuneCountInString(content) > maxContentLength {
		return fault.WithDetail(fault.ErrInvalidInput, "content must be 1 to 20000 characters")
	}
	return nil
}

// chunk splits content into pieces of at most chunkSize runes, breaking between paragraphs where it can
// and between words where a paragraph is too long, so that each piece reads on its own.
func chunk(content string) []string {
	var pieces, current []string
	size := 0
	for _, paragraph := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), separator) {
		for _, part := range splitWords(strings.TrimSpace(paragraph)) {
			length := utf8.RuneCountInString(part)
			if size > 0 && size+len(separator)+length > chunkSize {
				pieces, current, size = append(pieces, strings.Join(current, separator)), nil, 0
			}
			if size > 0 {
				size += len(separator)
			}
			current, size = append(current, part), size+length
		}
	}
	if len(current) > 0 {
		pieces = append(pieces, strings.Join(current, separator))
	}
	return pieces
}

// splitWords cuts text longer than chunkSize at the last space before the limit, or mid-word when one
// word is longer than that.
func splitWords(text string) []string {
	var parts []string
	runes := []rune(text)
	for len(runes) > chunkSize {
		cut := chunkSize
		for i := chunkSize; i > 0; i-- {
			if runes[i] == ' ' || runes[i] == '\n' {
				cut = i
				break
			}
		}
		parts = append(parts, strings.TrimSpace(string(runes[:cut])))
		runes = []rune(strings.TrimSpace(string(runes[cut:])))
	}
	if len(runes) > 0 {
		parts = append(parts, string(runes))
	}
	return parts
}

type embedder interface {
	// Embed reports ErrUnavailable when the model cannot be reached or is not configured.
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

func embed(ctx context.Context, model embedder, texts []string) ([][]float32, error) {
	vectors, err := model.Embed(ctx, texts)
	if err != nil {
		return nil, fmt.Errorf("embed: %w", err)
	}
	if len(vectors) != len(texts) {
		return nil, fmt.Errorf("%w: %d vectors for %d texts", errDimensions, len(vectors), len(texts))
	}
	for _, vector := range vectors {
		if len(vector) != Dimensions {
			return nil, fmt.Errorf("%w: %d, want %d", errDimensions, len(vector), Dimensions)
		}
	}
	return vectors, nil
}

// Item is one piece of knowledge chosen for a prompt.
type Item struct {
	NoteID string
	Kind   Kind
	Title  string
	Text   string
	Pinned bool
}

// Context is what Retrieve chose for a prompt. Partial means matches by meaning are missing because the
// embedding model was unavailable, so only pinned notes are included.
type Context struct {
	Items   []Item
	Partial bool
}

// Render formats the context for a prompt, or returns "" when there is nothing to say.
func (c Context) Render() string {
	if len(c.Items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("What is known about this app:\n")
	for _, item := range c.Items {
		fmt.Fprintf(&b, "\n[%s] %s\n%s\n", item.Kind, item.Title, item.Text)
	}
	return b.String()
}

// NoteIDs lists the notes the context draws on, each once.
func (c Context) NoteIDs() []string {
	ids := []string{}
	for _, item := range c.Items {
		if !slices.Contains(ids, item.NoteID) {
			ids = append(ids, item.NoteID)
		}
	}
	return ids
}

// add appends item when it fits the remaining budget of runes; an item that does not fit is left out so
// that smaller ones after it can still be used.
func (c *Context) add(item Item, budget *int) {
	length := utf8.RuneCountInString(item.Title) + utf8.RuneCountInString(item.Text)
	if length > *budget {
		return
	}
	*budget -= length
	c.Items = append(c.Items, item)
}
