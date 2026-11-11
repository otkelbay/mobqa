package device_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mobqa/backend/internal/clock/clocktest"
	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/device"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/ui"
)

// fakeRelay answers every offer with what it was given, unless it is set to fail.
type fakeRelay struct {
	err error
}

func (r *fakeRelay) Publish(_ context.Context, hubID string, target ui.Target, offer string) (string, error) {
	if r.err != nil {
		return "", r.err
	}
	return "phone answer from " + hubID + " " + target.Serial + " to " + offer, nil
}

func (r *fakeRelay) Watch(_ context.Context, hubID string, target ui.Target, offer string) (string, error) {
	if r.err != nil {
		return "", r.err
	}
	return "viewer answer from " + hubID + " " + target.Serial + " to " + offer, nil
}

func newStreams(relay *fakeRelay) *device.Streams {
	online := testStart().Add(time.Minute)
	store := &fakeStore{devices: []device.Device{
		{ID: "iphone", ProjectID: projID, HubID: "hub-1", Serial: "00008", Platform: project.PlatformIOS,
			State: device.StateConnected, HubOnlineUntil: &online},
		{ID: "unplugged", ProjectID: projID, HubID: "hub-1", Serial: "00009", Platform: project.PlatformIOS,
			State: device.StateDisconnected, HubOnlineUntil: &online},
	}}
	return device.NewStreams(device.StreamDependencies{
		Store: store, Access: roleAccess{"viewer": project.RoleViewer, "admin": project.RoleAdmin},
		Relay: relay, Clock: clocktest.New(testStart()),
	})
}

func TestStreamKeysLetThePhonePublish(t *testing.T) {
	streams := newStreams(&fakeRelay{})
	ctx := context.Background()
	iphone := device.Ref{UserID: "admin", ProjectID: projID, DeviceID: "iphone"}

	first, err := streams.RotateStreamKey(ctx, iphone)
	if err != nil || !strings.HasPrefix(first.Key, "mqs_") || first.Device.Status != device.StatusOnline {
		t.Fatalf("RotateStreamKey = %+v, %v", first, err)
	}
	answer, err := streams.Publish(ctx, first.Key, "v=0 phone")
	if err != nil || answer != "phone answer from hub-1 00008 to v=0 phone" {
		t.Fatalf("Publish = %q, %v", answer, err)
	}
	second, err := streams.RotateStreamKey(ctx, iphone)
	if err != nil || second.Key == first.Key {
		t.Fatalf("second RotateStreamKey = %+v, %v", second, err)
	}

	_, err = streams.Publish(ctx, first.Key, "v=0 phone")
	if detail, _ := fault.Detail(err); !errors.Is(err, fault.ErrUnauthorized) || detail != "invalid stream key" {
		t.Fatalf("Publish with the old key = %v", err)
	}
	if _, err := streams.RotateStreamKey(ctx, device.Ref{UserID: "viewer", ProjectID: projID,
		DeviceID: "iphone"}); !errors.Is(err, fault.ErrForbidden) {
		t.Fatalf("RotateStreamKey by a viewer = %v", err)
	}
}

func TestViewersWatchOnlineDevices(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name   string
		relay  *fakeRelay
		in     device.WatchInput
		want   string
		kind   error
		detail string
	}{
		{name: "online", relay: &fakeRelay{},
			in:   device.WatchInput{Ref: device.Ref{UserID: "viewer", ProjectID: projID, DeviceID: "iphone"}, Offer: "v=0"},
			want: "viewer answer from hub-1 00008 to v=0"},
		{name: "offline", relay: &fakeRelay{},
			in: device.WatchInput{Ref: device.Ref{UserID: "viewer", ProjectID: projID, DeviceID: "unplugged"},
				Offer: "v=0"},
			kind: fault.ErrConflict, detail: "the device is not online"},
		{name: "not sdp", relay: &fakeRelay{},
			in:   device.WatchInput{Ref: device.Ref{UserID: "viewer", ProjectID: projID, DeviceID: "iphone"}, Offer: "hi"},
			kind: fault.ErrInvalidInput, detail: "the offer must be an SDP session description"},
		{name: "outsider", relay: &fakeRelay{},
			in:   device.WatchInput{Ref: device.Ref{UserID: "eve", ProjectID: projID, DeviceID: "iphone"}, Offer: "v=0"},
			kind: fault.ErrNotFound},
		{name: "refused", relay: &fakeRelay{err: &ui.DeviceError{Message: "this hub cannot stream screens"}},
			in:   device.WatchInput{Ref: device.Ref{UserID: "viewer", ProjectID: projID, DeviceID: "iphone"}, Offer: "v=0"},
			kind: fault.ErrConflict, detail: "the device could not stream its screen: this hub cannot stream screens"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			answer, err := newStreams(tt.relay).Watch(ctx, tt.in)

			detail, _ := fault.Detail(err)
			if answer != tt.want || (tt.kind != nil && !errors.Is(err, tt.kind)) || detail != tt.detail {
				t.Fatalf("Watch = %q, %v", answer, err)
			}
		})
	}
}
