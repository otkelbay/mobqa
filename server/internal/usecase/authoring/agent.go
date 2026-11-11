package authoring

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/mobqa/backend/internal/usecase/action"
	"github.com/mobqa/backend/internal/usecase/chat"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/test"
	"github.com/mobqa/backend/internal/usecase/ui"
)

const (
	// maxShownElements keeps a busy screen within the prompt; the elements a person sees first come first.
	maxShownElements = 150
	maxShownText     = 100
	// recentTurns is how much of the current step the agent is reminded of.
	recentTurns = 6
	// minRedacted keeps short values, which would match unrelated text, out of redaction.
	minRedacted = 4
)

// Actions the agent may choose besides the commands it sends to the device.
const (
	actionWait = "wait"
	actionDone = "done"
	actionFail = "fail"
)

var errBadDecision = errors.New("the reply was not a valid action")

func systemPrompt(platform project.Platform, signIn, known string) string {
	prompt := `You are a senior mobile QA engineer automating a manual test on a real ` + string(platform) +
		` device. Each turn you see the current screen as numbered elements and choose one action for the current step.
Reply with one JSON object and nothing else:
{"thought":"...","action":"...","element":0,"text":"...","direction":"..."}
Actions:
- tap, long_press, double_tap: element.
- type: element is the field and text what to type; the field is tapped first. Write variables as {{NAME}}.
- clear: element; removes the field's text.
- swipe: direction up, down, left or right; element optionally names the list to scroll. Swiping up shows what is below.
- back, enter, hide_keyboard.
- open_url: text is the URL.
- assert: element is what proves the step's expected result.
- wait: the screen is still loading.
- done: the current step is complete.
- fail: the step cannot be done in this app; text says why.
Rules:
- Do only what the current step asks; never start the next step.
- Choose only listed elements; if the one you need is missing, scroll or wait for it.
- A step with an expected result ends with an assert of the element that shows it, then done.
- Type credentials and test data only through the listed variables; never invent their values.
- Hide the keyboard after typing when it covers what the next action needs.`
	for _, part := range []string{signIn, known} {
		if part != "" {
			prompt += "\n\n" + part
		}
	}
	return prompt
}

// signInBrief tells the agent how the app is signed in to, following the project's auth profile. It
// names variables, never their values; a fixed test code is configuration, not a secret.
func signInBrief(p project.AuthProfile, library []action.Action) string {
	var lines []string
	switch p.Method {
	case project.AuthPassword:
		lines = append(lines, "Sign in with a login and password"+named(" the login is", p.LoginVar)+
			named(" the password is", p.PasswordVar)+".")
	case project.AuthPhoneOTP, project.AuthEmailOTP:
		to := "phone number"
		if p.Method == project.AuthEmailOTP {
			to = "email address"
		}
		lines = append(lines, "Sign in with a one-time code sent to the "+to+named("", p.LoginVar)+".")
	case project.AuthGoogleSSO, project.AuthAppleSSO:
		lines = append(lines, ssoLine(p))
	}
	if p.OTP != nil && p.OTP.Override != "" {
		lines = append(lines, "Test builds accept the one-time code "+p.OTP.Override+".")
	}
	if p.OTP != nil && p.OTP.ActionID != "" {
		for _, a := range library {
			if a.ID == p.OTP.ActionID {
				lines = append(lines, otpActionLine(a))
			}
		}
	}
	if p.Notes != "" {
		lines = append(lines, "Notes: "+p.Notes)
	}
	if len(lines) == 0 {
		return ""
	}
	return "Signing in:\n- " + strings.Join(lines, "\n- ")
}

func ssoLine(p project.AuthProfile) string {
	provider := "Google"
	if p.Method == project.AuthAppleSSO {
		provider = "Apple"
	}
	line := "Sign in with " + provider + " through the system account picker"
	if p.SSO != nil && p.SSO.AccountVar != "" {
		line += ", choosing the account {{" + p.SSO.AccountVar + "}}"
	}
	return line + "; never type into a web sign-in page."
}

func otpActionLine(a action.Action) string {
	line := "The API action " + strconv.Quote(a.Name) + " reads the code the backend sent; a call step runs it"
	variables := make([]string, len(a.Request.Extract))
	for i, extraction := range a.Request.Extract {
		variables[i] = "{{" + extraction.Variable + "}}"
	}
	if len(variables) > 0 {
		line += " and the code is then " + strings.Join(variables, " or ")
	}
	return line + "."
}

// named is ", <label> {{NAME}}" for a set variable and nothing otherwise.
func named(label, variable string) string {
	if variable == "" {
		return ""
	}
	return "," + label + " {{" + variable + "}}"
}

// prompt describes the test, the current step, what was done in it so far and the screen.
func (s *session) prompt(number int, recent []Turn, screen ui.Screen) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Test: %s\nSteps:\n", s.work.Title)
	for i, step := range s.work.Steps {
		marker := "  "
		if i+1 == number {
			marker = "->"
		}
		fmt.Fprintf(&b, "%s %d. %s\n", marker, i+1, describeStep(step))
	}
	fmt.Fprintf(&b, "\nCurrent step %d: %s\n", number, describeStep(s.work.Steps[number-1]))
	if len(s.hints) == 0 {
		b.WriteString("Variables: none\n")
	} else {
		b.WriteString("Variables:\n- " + strings.Join(s.hints, "\n- ") + "\n")
	}
	if len(recent) > 0 {
		b.WriteString("\nDone in this step so far:\n")
		for _, turn := range recent {
			b.WriteString("- " + describeTurn(turn) + "\n")
		}
	}
	b.WriteString("\n" + renderScreen(redact(screen, s.variables)))
	return b.String()
}

func describeStep(step test.Step) string {
	text := string(step.Action)
	for _, part := range []string{step.Target, step.Description} {
		if part != "" {
			text += " - " + part
		}
	}
	if step.ExpectedResult != "" {
		text += " (expected: " + step.ExpectedResult + ")"
	}
	return text
}

func describeTurn(turn Turn) string {
	text := turn.Action
	if turn.Element != "" {
		text += " " + turn.Element
	}
	if turn.Text != "" {
		text += fmt.Sprintf(" text=%q", turn.Text)
	}
	if turn.Error != "" {
		text += " failed: " + turn.Error
	}
	return text
}

func renderScreen(screen ui.Screen) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Screen of %s, %dx%d", screen.App, screen.Width, screen.Height)
	if screen.Keyboard {
		b.WriteString(", keyboard shown")
	}
	b.WriteString(":\n")
	for i, e := range screen.Elements[:min(len(screen.Elements), maxShownElements)] {
		fmt.Fprintf(&b, "[%d] %s\n", i, describeElement(e))
	}
	if len(screen.Elements) == 0 {
		b.WriteString("(no elements; the app may draw its own widgets or still be loading)\n")
	}
	return b.String()
}

func describeElement(e ui.Element) string {
	parts := []string{e.Class}
	if e.Text != "" {
		parts = append(parts, fmt.Sprintf("%q", truncate(e.Text)))
	}
	if e.Label != "" {
		parts = append(parts, fmt.Sprintf("label=%q", truncate(e.Label)))
	}
	if e.ID != "" {
		parts = append(parts, "id="+e.ID[strings.LastIndex(e.ID, "/")+1:])
	}
	center := e.Bounds.Center()
	parts = append(parts, fmt.Sprintf("at %d,%d", center.X, center.Y))
	for _, flag := range []struct {
		on   bool
		name string
	}{{e.Clickable, "clickable"}, {e.Editable, "editable"}, {e.Scrollable, "scrollable"}, {e.Focused, "focused"},
		{e.Checked, "checked"}, {!e.Enabled, "disabled"}} {
		if flag.on {
			parts = append(parts, flag.name)
		}
	}
	return strings.Join(parts, " ")
}

func truncate(text string) string {
	if utf8.RuneCountInString(text) <= maxShownText {
		return text
	}
	return string([]rune(text)[:maxShownText]) + "..."
}

// redact replaces variable values on the screen with their names, so that what was typed from a secret
// does not reach the model.
func redact(screen ui.Screen, variables map[string]string) ui.Screen {
	names := make([]string, 0, len(variables))
	for name, value := range variables {
		if utf8.RuneCountInString(value) >= minRedacted {
			names = append(names, name)
		}
	}
	sort.Slice(names, func(i, j int) bool { return len(variables[names[i]]) > len(variables[names[j]]) })
	screen.Elements = slices.Clone(screen.Elements)
	for i, e := range screen.Elements {
		for _, name := range names {
			e.Text = strings.ReplaceAll(e.Text, variables[name], "{{"+name+"}}")
			e.Label = strings.ReplaceAll(e.Label, variables[name], "{{"+name+"}}")
		}
		screen.Elements[i] = e
	}
	return screen
}

type decision struct {
	Thought   string `json:"thought"`
	Action    string `json:"action"`
	Element   *int   `json:"element"`
	Text      string `json:"text"`
	Direction string `json:"direction"`
}

// parseDecision reads the model's reply and checks it against the screen it was given.
func parseDecision(response chat.Response, screen ui.Screen) (decision, error) {
	if response.Truncated {
		return decision{}, fmt.Errorf("%w: it hit the token limit", errBadDecision)
	}
	var d decision
	if err := json.Unmarshal([]byte(response.Text), &d); err != nil {
		return decision{}, fmt.Errorf("%w: %w", errBadDecision, err)
	}
	d.Action, d.Text = strings.TrimSpace(d.Action), strings.TrimSpace(d.Text)
	if d.Element != nil && (*d.Element < 0 || *d.Element >= min(len(screen.Elements), maxShownElements)) {
		return decision{}, fmt.Errorf("%w: element %d is not on the screen", errBadDecision, *d.Element)
	}
	switch ui.Kind(d.Action) {
	case ui.KindTap, ui.KindLongPress, ui.KindDoubleTap, ui.KindAssert, ui.KindType, ui.KindClear:
		if d.Element == nil {
			return decision{}, fmt.Errorf("%w: %s needs an element", errBadDecision, d.Action)
		}
	case ui.KindSwipe, ui.KindBack, ui.KindEnter, ui.KindHideKeyboard, ui.KindOpenURL:
	default:
		if d.Action != actionWait && d.Action != actionDone && d.Action != actionFail {
			return decision{}, fmt.Errorf("%w: unknown action %q", errBadDecision, d.Action)
		}
	}
	if (d.Action == string(ui.KindType) || d.Action == string(ui.KindOpenURL)) && d.Text == "" {
		return decision{}, fmt.Errorf("%w: %s needs text", errBadDecision, d.Action)
	}
	return d, nil
}
