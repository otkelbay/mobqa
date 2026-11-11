package ui_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/ui"
)

func TestCommandValidate(t *testing.T) {
	android := ui.Target{Serial: "R58", Platform: project.PlatformAndroid}
	tests := []struct {
		name    string
		command ui.Command
		valid   bool
	}{
		{name: "tap", command: ui.Command{Target: android, Kind: ui.KindTap, Point: ui.Point{X: 1, Y: 2}}, valid: true},
		{name: "type", command: ui.Command{Target: android, Kind: ui.KindType, Text: "hi"}, valid: true},
		{name: "no serial", command: ui.Command{Target: ui.Target{Platform: project.PlatformIOS}, Kind: ui.KindBack}},
		{name: "no platform", command: ui.Command{Target: ui.Target{Serial: "R58"}, Kind: ui.KindBack}},
		{name: "unknown kind", command: ui.Command{Target: android, Kind: "shake"}},
		{name: "type without text", command: ui.Command{Target: android, Kind: ui.KindType, Text: " "}},
		{name: "launch without package", command: ui.Command{Target: android, Kind: ui.KindLaunch}},
		{name: "negative point", command: ui.Command{Target: android, Kind: ui.KindTap, Point: ui.Point{X: -1}}},
		{name: "long duration", command: ui.Command{Target: android, Kind: ui.KindSwipe, Duration: time.Minute}},
		{name: "long text", command: ui.Command{Target: android, Kind: ui.KindType, Text: strings.Repeat("a", 2001)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.command.Validate()

			if tt.valid != (err == nil) || (err != nil && !errors.Is(err, fault.ErrInvalidInput)) {
				t.Fatalf("Validate = %v, want valid %t", err, tt.valid)
			}
		})
	}
}

func TestRectCenter(t *testing.T) {
	if center := (ui.Rect{X: 40, Y: 1000, Width: 1000, Height: 121}).Center(); center != (ui.Point{X: 540, Y: 1060}) {
		t.Fatalf("Center = %+v", center)
	}
}
