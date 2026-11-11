package ui_test

import (
	"errors"
	"testing"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/ui"
)

func shop() ui.Screen {
	return ui.Screen{Width: 1000, Height: 2000, Elements: []ui.Element{
		{ID: "app:id/title", Text: "Cart", Bounds: ui.Rect{Width: 1000, Height: 100}},
		{ID: "app:id/item", Text: "Shoes", Label: "Remove shoes", Bounds: ui.Rect{Y: 200, Width: 1000, Height: 100}},
		{ID: "app:id/item", Text: "Socks", Label: "Remove socks", Bounds: ui.Rect{Y: 300, Width: 1000, Height: 100}},
		{Text: "Buy", Bounds: ui.Rect{Y: 400, Width: 500, Height: 100}},
		{Text: "Buy", Bounds: ui.Rect{X: 500, Y: 400, Width: 500, Height: 100}},
	}}
}

func TestSelectorForUsesTheFewestFieldsThatTellElementsApart(t *testing.T) {
	screen := shop()
	tests := []struct {
		name    string
		element int
		want    ui.Selector
	}{
		{name: "unique id", element: 0, want: ui.Selector{ID: "app:id/title"}},
		{name: "shared id, unique text", element: 2, want: ui.Selector{Text: "Socks"}},
		{name: "identical twins", element: 4, want: ui.Selector{Text: "Buy", Index: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selector := ui.SelectorFor(screen, screen.Elements[tt.element])

			found, ok := selector.Find(screen)
			if selector != tt.want || !ok || found != screen.Elements[tt.element] {
				t.Fatalf("SelectorFor = %+v finds %+v, want %+v", selector, found, tt.want)
			}
		})
	}
}

func TestFindReportsMissingElements(t *testing.T) {
	for _, selector := range []ui.Selector{{Text: "Pay"}, {Text: "Buy", Index: 2}, {ID: "app:id/item", Text: "Cart"}} {
		if found, ok := selector.Find(shop()); ok {
			t.Fatalf("%v found %+v", selector, found)
		}
	}
}

func TestSwipePointsStayInsideTheArea(t *testing.T) {
	area := ui.Rect{X: 0, Y: 200, Width: 1000, Height: 1000}

	from, to, err := ui.SwipePoints(area, ui.DirectionUp)

	if err != nil || from != (ui.Point{X: 500, Y: 1000}) || to != (ui.Point{X: 500, Y: 400}) {
		t.Fatalf("SwipePoints = %+v -> %+v, %v", from, to, err)
	}
	if _, _, err := ui.SwipePoints(area, "sideways"); !errors.Is(err, fault.ErrInvalidInput) {
		t.Fatalf("SwipePoints sideways = %v", err)
	}
}

func TestExpandSubstitutesVariables(t *testing.T) {
	text, err := ui.Expand("{{PHONE}} / {{ PHONE }}", map[string]string{"PHONE": "+77001234567"})
	if err != nil || text != "+77001234567 / {{ PHONE }}" {
		t.Fatalf("Expand = %q, %v", text, err)
	}

	if _, err := ui.Expand("{{PASSWORD}}", map[string]string{}); !errors.Is(err, ui.ErrUnknownVariable) {
		t.Fatalf("Expand unknown = %v", err)
	}
}

func TestValidateScript(t *testing.T) {
	buy := &ui.Selector{Text: "Buy"}
	tests := []struct {
		name  string
		op    ui.Op
		valid bool
	}{
		{name: "tap", op: ui.Op{Step: 1, Kind: ui.KindTap, Selector: buy}, valid: true},
		{name: "assert", op: ui.Op{Step: 2, Kind: ui.KindAssert, Selector: buy}, valid: true},
		{name: "launch", op: ui.Op{Kind: ui.KindLaunch, Text: "com.shop.app"}, valid: true},
		{name: "swipe", op: ui.Op{Step: 1, Kind: ui.KindSwipe, Direction: ui.DirectionUp}, valid: true},
		{name: "call", op: ui.Op{Step: 1, Kind: ui.KindCall, Text: "Fetch OTP"}, valid: true},
		{name: "step beyond the test", op: ui.Op{Step: 3, Kind: ui.KindBack}},
		{name: "call without action", op: ui.Op{Step: 1, Kind: ui.KindCall}},
		{name: "call on an element", op: ui.Op{Step: 1, Kind: ui.KindCall, Text: "Fetch OTP", Selector: buy}},
		{name: "tap without selector", op: ui.Op{Step: 1, Kind: ui.KindTap}},
		{name: "assert without selector", op: ui.Op{Step: 1, Kind: ui.KindAssert}},
		{name: "empty selector", op: ui.Op{Step: 1, Kind: ui.KindTap, Selector: &ui.Selector{Index: 1}}},
		{name: "swipe without direction", op: ui.Op{Step: 1, Kind: ui.KindSwipe}},
		{name: "type without text", op: ui.Op{Step: 1, Kind: ui.KindType}},
		{name: "unknown kind", op: ui.Op{Step: 1, Kind: "shake"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ui.ValidateScript([]ui.Op{tt.op}, 2)

			if tt.valid != (err == nil) || (err != nil && !errors.Is(err, fault.ErrInvalidInput)) {
				t.Fatalf("ValidateScript = %v, want valid %t", err, tt.valid)
			}
		})
	}
}
