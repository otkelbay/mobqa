package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/device"
	"github.com/mobqa/backend/internal/usecase/hub"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/ui"
)

type fakeHubs struct {
	Hubs
	registered []hub.RegisterInput
}

func (f *fakeHubs) Register(_ context.Context, in hub.RegisterInput) (hub.Registration, error) {
	f.registered = append(f.registered, in)
	return hub.Registration{
		Hub:   hub.Hub{ID: "hub-1", Name: in.Name, CreatedAt: shopTime()},
		Token: "mqh_secret",
	}, nil
}

func (f *fakeHubs) Delete(_ context.Context, ref hub.Ref) error {
	if ref.HubID != "hub-1" {
		return fault.WithDetail(fault.ErrNotFound, "hub not found")
	}
	return nil
}

type fakeDevices struct {
	Devices
	listed []device.ListInput
}

func (f *fakeDevices) List(_ context.Context, in device.ListInput) ([]device.Device, error) {
	f.listed = append(f.listed, in)
	return []device.Device{{
		ID: "device-1", HubID: "hub-1", HubName: "Office Mac", Serial: "R58M", Name: "Pixel", Model: "Pixel 8a",
		Platform: project.PlatformAndroid, OSVersion: "15", Physical: true, Status: device.StatusOnline,
		LastSeenAt: shopTime().Add(time.Minute),
	}}, nil
}

type fakeControl struct {
	acted []device.ActInput
}

func (f *fakeControl) Screen(_ context.Context, ref device.Ref) ([]byte, error) {
	if ref.DeviceID != "device-1" {
		return nil, fault.WithDetail(fault.ErrNotFound, "device not found")
	}
	return []byte("jpeg"), nil
}

func (f *fakeControl) Act(_ context.Context, in device.ActInput) error {
	f.acted = append(f.acted, in)
	return nil
}

func TestDeviceScreenIsAJPEG(t *testing.T) {
	recorder := serveAuthorized(t, Dependencies{Control: &fakeControl{}},
		jsonRequest(http.MethodGet, "/projects/project-1/devices/device-1/screen", ""))
	missing := serveAuthorized(t, Dependencies{Control: &fakeControl{}},
		jsonRequest(http.MethodGet, "/projects/project-1/devices/device-9/screen", ""))

	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "image/jpeg" ||
		recorder.Body.String() != "jpeg" || missing.Code != http.StatusNotFound {
		t.Fatalf("screen = %d %q, missing %d", recorder.Code, recorder.Body.String(), missing.Code)
	}
}

func TestDeviceCommandsReachControl(t *testing.T) {
	control := &fakeControl{}

	recorder := serveAuthorized(t, Dependencies{Control: control}, jsonRequest(http.MethodPost,
		"/projects/project-1/devices/device-1/commands",
		`{"kind":"swipe","x":500,"y":1800,"to_x":500,"to_y":400,"duration_ms":300}`))

	want := device.ActInput{
		Ref: device.Ref{UserID: "user-1", ProjectID: "project-1", DeviceID: "device-1"},
		Command: ui.Command{Kind: ui.KindSwipe, Point: ui.Point{X: 500, Y: 1800}, To: ui.Point{X: 500, Y: 400},
			Duration: 300 * time.Millisecond},
	}
	if recorder.Code != http.StatusNoContent || len(control.acted) != 1 || control.acted[0] != want {
		t.Fatalf("act = %d, inputs %+v", recorder.Code, control.acted)
	}
}

// fakeStreams answers offers with what it was given; its stream key is mqs_secret.
type fakeStreams struct {
	watched []device.WatchInput
}

func (f *fakeStreams) RotateStreamKey(_ context.Context, ref device.Ref) (device.StreamKey, error) {
	return device.StreamKey{Device: device.Device{ID: ref.DeviceID, HubID: "hub-1", Platform: project.PlatformIOS,
		Status: device.StatusOnline}, Key: "mqs_secret"}, nil
}

func (f *fakeStreams) Publish(_ context.Context, key, offer string) (string, error) {
	if key != "mqs_secret" {
		return "", fault.WithDetail(fault.ErrUnauthorized, "invalid stream key")
	}
	return "answer to " + offer, nil
}

func (f *fakeStreams) Watch(_ context.Context, in device.WatchInput) (string, error) {
	f.watched = append(f.watched, in)
	return "answer to " + in.Offer, nil
}

func sdpRequest(path, offer string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(offer))
	request.Header.Set("Content-Type", "application/sdp")
	return request
}

func TestWatchingADeviceAnswersTheOffer(t *testing.T) {
	streams := &fakeStreams{}

	recorder := serveAuthorized(t, Dependencies{Streams: streams},
		sdpRequest("/projects/project-1/devices/device-1/stream", "v=0 browser"))
	asJSON := serveAuthorized(t, Dependencies{Streams: streams},
		jsonRequest(http.MethodPost, "/projects/project-1/devices/device-1/stream", `{"sdp":"v=0"}`))

	want := device.WatchInput{Ref: device.Ref{UserID: "user-1", ProjectID: "project-1", DeviceID: "device-1"},
		Offer: "v=0 browser"}
	if recorder.Code != http.StatusCreated || recorder.Header().Get("Content-Type") != "application/sdp" ||
		recorder.Body.String() != "answer to v=0 browser" || len(streams.watched) != 1 || streams.watched[0] != want {
		t.Fatalf("watch = %d %q, inputs %+v", recorder.Code, recorder.Body.String(), streams.watched)
	}
	requireResponse(t, asJSON, http.StatusBadRequest, `{"error":"the offer must be sent as application/sdp"}`)
}

func TestPublishingNeedsTheStreamKey(t *testing.T) {
	request := sdpRequest("/streams/publish", "v=0 phone")
	request.Header.Set("Authorization", "Bearer mqs_secret")
	published := serve(t, Dependencies{Streams: &fakeStreams{}}, request)
	stranger := serve(t, Dependencies{Streams: &fakeStreams{}}, sdpRequest("/streams/publish", "v=0 phone"))

	if published.Code != http.StatusCreated || published.Body.String() != "answer to v=0 phone" {
		t.Fatalf("publish = %d %q", published.Code, published.Body.String())
	}
	requireResponse(t, stranger, http.StatusUnauthorized, `{"error":"invalid stream key"}`)
}

func TestRotateStreamKeyReturnsTheKeyOnce(t *testing.T) {
	recorder := serveAuthorized(t, Dependencies{Streams: &fakeStreams{}},
		jsonRequest(http.MethodPost, "/projects/project-1/devices/device-1/stream-key", ""))

	requireResponse(t, recorder, http.StatusOK,
		`{"device":{"id":"device-1","hub_id":"hub-1","hub_name":"","serial":"","name":"","model":"",`+
			`"platform":"ios","os_version":"","physical":false,"status":"online",`+
			`"last_seen_at":"0001-01-01T00:00:00Z"},"key":"mqs_secret"}`)
}

func TestICEServersWithoutConfigAreEmpty(t *testing.T) {
	recorder := serveAuthorized(t, Dependencies{}, jsonRequest(http.MethodGet, "/ice-servers", ""))

	requireResponse(t, recorder, http.StatusOK, `{"ice_servers":[]}`)
}

func TestRegisterHubReturnsTokenOnce(t *testing.T) {
	hubs := &fakeHubs{}

	recorder := serveAuthorized(t, Dependencies{Hubs: hubs},
		jsonRequest(http.MethodPost, "/projects/project-1/hubs", `{"name":"Office Mac"}`))

	requireResponse(t, recorder, http.StatusCreated,
		`{"hub":{"id":"hub-1","name":"Office Mac","version":"","online":false,"created_at":"2026-11-11T09:00:00Z"},`+
			`"token":"mqh_secret"}`)
	want := hub.RegisterInput{UserID: "user-1", ProjectID: "project-1", Name: "Office Mac"}
	if hubs.registered[0] != want {
		t.Fatalf("registered = %+v", hubs.registered[0])
	}
}

func TestDeleteHub(t *testing.T) {
	hubs := &fakeHubs{}

	deleted := serveAuthorized(t, Dependencies{Hubs: hubs}, jsonRequest(http.MethodDelete, "/projects/p/hubs/hub-1", ""))
	missing := serveAuthorized(t, Dependencies{Hubs: hubs}, jsonRequest(http.MethodDelete, "/projects/p/hubs/nope", ""))

	requireResponse(t, deleted, http.StatusNoContent, "")
	requireResponse(t, missing, http.StatusNotFound, `{"error":"hub not found"}`)
}

func TestListDevicesFiltersByPlatform(t *testing.T) {
	devices := &fakeDevices{}

	recorder := serveAuthorized(t, Dependencies{Devices: devices},
		jsonRequest(http.MethodGet, "/projects/project-1/devices?platform=android", ""))

	requireResponse(t, recorder, http.StatusOK,
		`[{"id":"device-1","hub_id":"hub-1","hub_name":"Office Mac","serial":"R58M","name":"Pixel",`+
			`"model":"Pixel 8a","platform":"android","os_version":"15","physical":true,"status":"online",`+
			`"last_seen_at":"2026-11-11T09:01:00Z"}]`)
	want := device.ListInput{UserID: "user-1", ProjectID: "project-1", Platform: project.PlatformAndroid}
	if devices.listed[0] != want {
		t.Fatalf("listed = %+v", devices.listed[0])
	}
}
