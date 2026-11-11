package adb

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/ui"
)

const dump = `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?>
<hierarchy rotation="0">
<node text="" resource-id="" class="android.widget.FrameLayout" package="com.shop.app" content-desc=""
 clickable="false" enabled="true" bounds="[0,0][1080,2400]">
  <node text="" resource-id="" class="android.view.View" package="com.shop.app" content-desc=""
   clickable="false" enabled="true" bounds="[0,0][1080,200]"/>
  <node text="Sign in" resource-id="com.shop.app:id/sign_in" class="android.widget.Button"
   package="com.shop.app" content-desc="" clickable="true" enabled="true" bounds="[40,1000][1040,1120]"/>
  <node text="" resource-id="com.shop.app:id/phone" class="android.widget.EditText" package="com.shop.app"
   content-desc="Phone" clickable="true" enabled="true" focused="true" bounds="[40,800][1040,920]"/>
  <node text="Hidden" resource-id="" class="android.widget.TextView" package="com.shop.app" content-desc=""
   clickable="false" enabled="true" bounds="[0,2500][100,2600]"/>
  <node text="Empty" resource-id="" class="android.widget.TextView" package="com.shop.app" content-desc=""
   clickable="false" enabled="true" bounds="[10,10][10,50]"/>
</node>
</hierarchy>`

// scriptedADB answers adb calls by their first device argument and records every call.
type scriptedADB struct {
	answers map[string]string
	calls   []string
}

func (s *scriptedADB) output(_ context.Context, args ...string) ([]byte, error) {
	call := strings.Join(args, " ")
	s.calls = append(s.calls, call)
	for prefix, answer := range s.answers {
		if strings.HasPrefix(call, prefix) {
			return []byte(answer), nil
		}
	}
	return nil, nil
}

func phone() *scriptedADB {
	return &scriptedADB{answers: map[string]string{
		"-s R58 exec-out cat":               dump,
		"-s R58 shell dumpsys input_method": "  mInputShown=true\n",
	}}
}

func TestObserveReadsUsableElements(t *testing.T) {
	adb := phone()

	screen, err := (&Controller{run: adb}).Observe(context.Background(), "R58")

	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if screen.App != "com.shop.app" || screen.Width != 1080 || screen.Height != 2400 || !screen.Keyboard {
		t.Fatalf("screen = %+v", screen)
	}
	want := []ui.Element{
		{
			ID: "com.shop.app:id/sign_in", Text: "Sign in", Class: "Button",
			Bounds: ui.Rect{X: 40, Y: 1000, Width: 1000, Height: 120}, Clickable: true, Enabled: true,
		},
		{
			ID: "com.shop.app:id/phone", Label: "Phone", Class: "EditText",
			Bounds: ui.Rect{X: 40, Y: 800, Width: 1000, Height: 120}, Clickable: true, Editable: true, Enabled: true,
			Focused: true,
		},
	}
	if len(screen.Elements) != len(want) || screen.Elements[0] != want[0] || screen.Elements[1] != want[1] {
		t.Fatalf("elements = %+v", screen.Elements)
	}
}

func TestObserveRejectsUnreadableDumps(t *testing.T) {
	for name, answer := range map[string]string{"not xml": "ERROR: null root node", "no window": "<hierarchy/>"} {
		t.Run(name, func(t *testing.T) {
			adb := &scriptedADB{answers: map[string]string{"-s R58 exec-out cat": answer}}

			if _, err := (&Controller{run: adb}).Observe(context.Background(), "R58"); err == nil {
				t.Fatal("Observe accepted an unreadable dump")
			}
		})
	}
}

func TestActSendsShellCommands(t *testing.T) {
	tests := []struct {
		name    string
		command ui.Command
		want    string
	}{
		{name: "tap", command: ui.Command{Kind: ui.KindTap, Point: ui.Point{X: 5, Y: 9}}, want: "input tap 5 9"},
		{
			name:    "double tap",
			command: ui.Command{Kind: ui.KindDoubleTap, Point: ui.Point{X: 5, Y: 9}},
			want:    "input tap 5 9 && input tap 5 9",
		},
		{
			name:    "long press",
			command: ui.Command{Kind: ui.KindLongPress, Point: ui.Point{X: 5, Y: 9}},
			want:    "input swipe 5 9 5 9 800",
		},
		{
			name:    "swipe",
			command: ui.Command{Kind: ui.KindSwipe, Point: ui.Point{Y: 900}, To: ui.Point{Y: 100}, Duration: time.Second},
			want:    "input swipe 0 900 0 100 1000",
		},
		{name: "type", command: ui.Command{Kind: ui.KindType, Text: "it's me"}, want: `input text 'it'\''s%sme'`},
		{
			name:    "open url",
			command: ui.Command{Kind: ui.KindOpenURL, Text: "shop://cart?a=1&b=2"},
			want:    "am start -a android.intent.action.VIEW -d 'shop://cart?a=1&b=2'",
		},
		{
			name:    "launch",
			command: ui.Command{Kind: ui.KindLaunch, Text: "com.shop.app"},
			want:    "monkey -p com.shop.app -c android.intent.category.LAUNCHER 1",
		},
		{name: "stop", command: ui.Command{Kind: ui.KindStop, Text: "com.shop.app"}, want: "am force-stop com.shop.app"},
		{name: "back", command: ui.Command{Kind: ui.KindBack}, want: "input keyevent KEYCODE_BACK"},
		{name: "hide keyboard", command: ui.Command{Kind: ui.KindHideKeyboard}, want: "input keyevent KEYCODE_BACK"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adb := phone()
			tt.command.Target = ui.Target{Serial: "R58", Platform: project.PlatformAndroid}

			err := (&Controller{run: adb}).Act(context.Background(), tt.command)

			if err != nil || adb.calls[len(adb.calls)-1] != "-s R58 shell "+tt.want {
				t.Fatalf("Act = %v, calls %q", err, adb.calls)
			}
		})
	}
}

func TestHideKeyboardDoesNothingWithoutKeyboard(t *testing.T) {
	adb := &scriptedADB{answers: map[string]string{"-s R58 exec-out cat": dump}}
	hide := ui.Command{Target: ui.Target{Serial: "R58"}, Kind: ui.KindHideKeyboard}

	err := (&Controller{run: adb}).Act(context.Background(), hide)

	if err != nil || strings.Contains(strings.Join(adb.calls, "\n"), "KEYCODE_BACK") {
		t.Fatalf("Act = %v, calls %q", err, adb.calls)
	}
}

func TestActRejectsWhatADBCannotDoSafely(t *testing.T) {
	tests := []struct {
		name    string
		command ui.Command
	}{
		{name: "non-ASCII text", command: ui.Command{Kind: ui.KindType, Text: "Привет"}},
		{name: "shell in a package name", command: ui.Command{Kind: ui.KindLaunch, Text: "com.app; reboot"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adb := phone()
			tt.command.Target.Serial = "R58"

			err := (&Controller{run: adb}).Act(context.Background(), tt.command)

			if err == nil || len(adb.calls) != 0 {
				t.Fatalf("Act = %v, calls %q", err, adb.calls)
			}
		})
	}
	if _, err := typeScript("é"); !errors.Is(err, errNonASCII) {
		t.Fatalf("typing é = %v, want errNonASCII", err)
	}
}

func TestInstallReplacesTheApp(t *testing.T) {
	adb := &scriptedADB{answers: map[string]string{"-s R58 install": "Performing Streamed Install\nSuccess\n"}}

	err := (&Controller{install: adb}).Install(context.Background(), "R58", "/cache/cafe.pkg")

	if err != nil || len(adb.calls) != 1 || adb.calls[0] != "-s R58 install -r -d /cache/cafe.pkg" {
		t.Fatalf("Install = %v, calls %q", err, adb.calls)
	}
	refused := &scriptedADB{answers: map[string]string{"-s R58 install": "Failure [INSTALL_FAILED_INSUFFICIENT_STORAGE]"}}
	err = (&Controller{install: refused}).Install(context.Background(), "R58", "/cache/cafe.pkg")
	if err == nil || !strings.Contains(err.Error(), "INSTALL_FAILED_INSUFFICIENT_STORAGE") {
		t.Fatalf("refused Install = %v", err)
	}
}

func TestCaptureReturnsAJPEG(t *testing.T) {
	var shot bytes.Buffer
	if err := png.Encode(&shot, image.NewGray(image.Rect(0, 0, 108, 240))); err != nil {
		t.Fatal(err)
	}
	adb := &scriptedADB{answers: map[string]string{"-s R58 exec-out screencap -p": shot.String()}}

	captured, err := (&Controller{run: adb}).Capture(context.Background(), "R58")

	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if config, err := jpeg.DecodeConfig(bytes.NewReader(captured)); err != nil || config.Width != 108 ||
		config.Height != 240 {
		t.Fatalf("captured = %+v, %v", config, err)
	}
	broken := &scriptedADB{answers: map[string]string{"-s R58 exec-out screencap": "error: no devices"}}
	if _, err := (&Controller{run: broken}).Capture(context.Background(), "R58"); err == nil {
		t.Fatal("Capture of a broken screenshot succeeded")
	}
}
