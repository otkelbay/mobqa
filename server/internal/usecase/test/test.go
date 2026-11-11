// Package test manages a project's tests: ordered steps in plain language for one platform. Every change
// to a test's content is stored as an immutable version, so any earlier version can be read or restored.
package test

import (
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/project"
)

type Priority string

const (
	PriorityCritical Priority = "critical"
	PriorityHigh     Priority = "high"
	PriorityMedium   Priority = "medium"
	PriorityLow      Priority = "low"
)

// Source says how a test came to exist.
type Source string

const (
	SourceManual    Source = "manual"
	SourceGenerated Source = "generated"
	SourceDuplicate Source = "duplicate"
)

// ChangeSource says why a version exists.
type ChangeSource string

const (
	ChangeManual   ChangeSource = "manual"
	ChangeAgent    ChangeSource = "agent"
	ChangeGenerate ChangeSource = "generate"
	ChangeRestore  ChangeSource = "restore"
)

// Action is what a step does. The set is closed so that authoring and runs can rely on it.
type Action string

const (
	ActionTap            Action = "tap"
	ActionLongPress      Action = "long_press"
	ActionDoubleTap      Action = "double_tap"
	ActionInput          Action = "input"
	ActionClearText      Action = "clear_text"
	ActionHideKeyboard   Action = "hide_keyboard"
	ActionSwipe          Action = "swipe"
	ActionScroll         Action = "scroll"
	ActionScrollUntil    Action = "scroll_until_visible"
	ActionWait           Action = "wait"
	ActionWaitForElement Action = "wait_for_element"
	ActionBack           Action = "back"
	ActionOpenDeeplink   Action = "open_deeplink"
	ActionAssert         Action = "assert"
	ActionAssertCount    Action = "assert_count"
	ActionScreenshot     Action = "screenshot"
	// ActionCall performs the project's API action named by the step's target.
	ActionCall Action = "call"
)

// actions lists every Action; the generation prompt shows the same list to the model.
func actions() []Action {
	return []Action{
		ActionTap, ActionLongPress, ActionDoubleTap, ActionInput, ActionClearText, ActionHideKeyboard, ActionSwipe,
		ActionScroll, ActionScrollUntil, ActionWait, ActionWaitForElement, ActionBack, ActionOpenDeeplink,
		ActionAssert, ActionAssertCount, ActionScreenshot, ActionCall,
	}
}

const (
	maxTitleLength       = 200
	maxDescriptionLength = 5000
	maxTags              = 20
	maxTagLength         = 40
	maxSteps             = 200
	maxTargetLength      = 500
	maxStepTextLength    = 1000
	maxNoteLength        = 500
)

type Step struct {
	Action Action
	// Target is the element or value the action works on, such as "Sign in button" or a deeplink.
	Target         string
	Description    string
	ExpectedResult string
}

// Content is the versioned part of a test.
type Content struct {
	Title       string
	Description string
	Steps       []Step
}

type Test struct {
	ID        string
	ProjectID string
	Platform  project.Platform
	Priority  Priority
	Tags      []string
	Source    Source
	Content   Content
	Version   int
	StepCount int
	CreatedBy string
	UpdatedBy string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Version is a snapshot of a test's content. Listings leave Content.Steps empty and report StepCount.
type Version struct {
	Number    int
	Content   Content
	StepCount int
	Source    ChangeSource
	Note      string
	CreatedBy string
	CreatedAt time.Time
}

func (c Content) validate() error {
	if strings.TrimSpace(c.Title) != c.Title || c.Title == "" || utf8.RuneCountInString(c.Title) > maxTitleLength {
		return fault.WithDetail(fault.ErrInvalidInput, "title must be 1 to 200 characters without surrounding spaces")
	}
	if utf8.RuneCountInString(c.Description) > maxDescriptionLength {
		return fault.WithDetail(fault.ErrInvalidInput, "description must be at most 5000 characters")
	}
	return validateSteps(c.Steps)
}

func validateSteps(steps []Step) error {
	if len(steps) > maxSteps {
		return fault.WithDetail(fault.ErrInvalidInput, "a test has at most 200 steps")
	}
	for _, step := range steps {
		if err := step.validate(); err != nil {
			return err
		}
	}
	return nil
}

func (s Step) validate() error {
	if !slices.Contains(actions(), s.Action) {
		return fault.WithDetail(fault.ErrInvalidInput, "unknown step action")
	}
	if utf8.RuneCountInString(s.Target) > maxTargetLength ||
		utf8.RuneCountInString(s.Description) > maxStepTextLength ||
		utf8.RuneCountInString(s.ExpectedResult) > maxStepTextLength {
		return fault.WithDetail(fault.ErrInvalidInput, "step targets are limited to 500 characters and texts to 1000")
	}
	if s.Action == ActionCall && strings.TrimSpace(s.Target) == "" {
		return fault.WithDetail(fault.ErrInvalidInput, "a call step names an API action as its target")
	}
	return nil
}

func validatePriority(priority Priority) error {
	switch priority {
	case PriorityCritical, PriorityHigh, PriorityMedium, PriorityLow:
		return nil
	default:
		return fault.WithDetail(fault.ErrInvalidInput, "priority must be critical, high, medium or low")
	}
}

func validatePlatform(platform project.Platform) error {
	if platform != project.PlatformAndroid && platform != project.PlatformIOS {
		return fault.WithDetail(fault.ErrInvalidInput, "platform must be android or ios")
	}
	return nil
}

func validateNote(note string) error {
	if utf8.RuneCountInString(note) > maxNoteLength {
		return fault.WithDetail(fault.ErrInvalidInput, "note must be at most 500 characters")
	}
	return nil
}

// normalizeTags trims, lowercases and deduplicates tags, keeping their first-seen order.
func normalizeTags(tags []string) ([]string, error) {
	normalized := make([]string, 0, len(tags))
	seen := make(map[string]bool, len(tags))
	for _, tag := range tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag == "" || utf8.RuneCountInString(tag) > maxTagLength {
			return nil, fault.WithDetail(fault.ErrInvalidInput, "tags must be 1 to 40 characters")
		}
		if !seen[tag] {
			seen[tag] = true
			normalized = append(normalized, tag)
		}
	}
	if len(normalized) > maxTags {
		return nil, fault.WithDetail(fault.ErrInvalidInput, "a test has at most 20 tags")
	}
	return normalized, nil
}

// copyTitle names a duplicate, shortening the original so the result still fits.
func copyTitle(title string) string {
	const suffix = " (copy)"
	runes := []rune(title)
	if keep := maxTitleLength - utf8.RuneCountInString(suffix); len(runes) > keep {
		runes = runes[:keep]
	}
	return strings.TrimSpace(string(runes)) + suffix
}
