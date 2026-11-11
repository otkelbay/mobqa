//go:build e2e

package e2e

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mobqa/backend/internal/transport/hubws"
	"github.com/mobqa/backend/internal/usecase/agent"
	"github.com/mobqa/backend/internal/usecase/device"
	"github.com/mobqa/backend/internal/usecase/hub"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/ui"
)

type hubBody struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Online  bool   `json:"online"`
}

type registrationBody struct {
	Hub   hubBody `json:"hub"`
	Token string  `json:"token"`
}

type deviceBody struct {
	ID       string `json:"id"`
	HubName  string `json:"hub_name"`
	Serial   string `json:"serial"`
	Platform string `json:"platform"`
	Status   string `json:"status"`
}

func registerHub(a *api, token, projectID, name string) registrationBody {
	a.t.Helper()
	var reg registrationBody
	a.do(call{method: http.MethodPost, path: "/projects/" + projectID + "/hubs", token: token,
		body: map[string]string{"name": name}}, http.StatusCreated, &reg)
	return reg
}

func TestHubRegistrationAndRoles(t *testing.T) {
	api := newAPI(t)
	ada := register(api)
	bob := registerAs(api, "bob@example.com")
	shop := createProject(api, ada.AccessToken, "Shop")
	hubs := "/projects/" + shop.ID + "/hubs"
	api.do(call{method: http.MethodPost, path: "/projects/" + shop.ID + "/members", token: ada.AccessToken,
		body: map[string]string{"email": "bob@example.com", "role": "engineer"}}, http.StatusCreated, nil)

	api.do(call{method: http.MethodPost, path: hubs, token: bob.AccessToken,
		body: map[string]string{"name": "Bob's Mac"}}, http.StatusForbidden, nil)
	reg := registerHub(api, ada.AccessToken, shop.ID, "Office Mac")
	if reg.Token == "" || reg.Hub.Online || reg.Hub.Name != "Office Mac" {
		t.Fatalf("registration = %+v", reg)
	}
	api.do(call{method: http.MethodPost, path: hubs, token: ada.AccessToken,
		body: map[string]string{"name": "Office Mac"}}, http.StatusConflict, nil)

	var listed []hubBody
	api.do(call{method: http.MethodGet, path: hubs, token: bob.AccessToken}, http.StatusOK, &listed)
	if len(listed) != 1 || listed[0].ID != reg.Hub.ID {
		t.Fatalf("hubs = %+v", listed)
	}
	var rotated registrationBody
	api.do(call{method: http.MethodPost, path: hubs + "/" + reg.Hub.ID + "/token", token: ada.AccessToken},
		http.StatusOK, &rotated)
	if rotated.Token == "" || rotated.Token == reg.Token {
		t.Fatalf("rotated token = %q", rotated.Token)
	}

	var devices []deviceBody
	api.do(call{method: http.MethodGet, path: "/projects/" + shop.ID + "/devices", token: bob.AccessToken},
		http.StatusOK, &devices)
	if len(devices) != 0 {
		t.Fatalf("devices before any hub connected = %+v", devices)
	}
	api.do(call{method: http.MethodDelete, path: hubs + "/" + reg.Hub.ID, token: bob.AccessToken},
		http.StatusForbidden, nil)
	api.do(call{method: http.MethodDelete, path: hubs + "/" + reg.Hub.ID, token: ada.AccessToken},
		http.StatusNoContent, nil)
}

// bench is a fake discoverer standing in for adb: tests plug and unplug phones on it.
type bench struct {
	mu      sync.Mutex
	serials []string
}

func (b *bench) plug(serials ...string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.serials = serials
}

func (b *bench) Discover(context.Context) ([]hub.ReportedDevice, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	devices := make([]hub.ReportedDevice, len(b.serials))
	for i, serial := range b.serials {
		devices[i] = hub.ReportedDevice{
			Serial: serial, Name: "Pixel", Model: "Pixel 8a", Platform: project.PlatformAndroid, OSVersion: "15",
			Physical: true, State: device.StateConnected,
		}
	}
	return devices, nil
}

type runningHub struct {
	stop context.CancelFunc
	done chan error
}

func (a *api) startHub(token string, b *bench) runningHub {
	a.t.Helper()
	return a.startHubWith(token, agent.Config{Version: "e2e", Discoverers: []agent.Discoverer{b}})
}

func (a *api) startHubWith(token string, cfg agent.Config) runningHub {
	a.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	client := hubws.NewClient(
		hubws.ClientConfig{URL: a.url, Token: token, PollInterval: 20 * time.Millisecond},
		agent.New(cfg), slog.New(slog.NewTextHandler(io.Discard, nil)))
	h := runningHub{stop: cancel, done: make(chan error, 1)}
	go func() { h.done <- client.Run(ctx) }()
	return h
}

func (h runningHub) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-h.done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("hub did not stop")
		return nil
	}
}

// await polls a resource until it satisfies the condition.
func await[T any](a *api, c call, condition func(T) bool) T {
	a.t.Helper()
	deadline := time.Now().Add(a.patience)
	for {
		var found T
		a.do(c, http.StatusOK, &found)
		if condition(found) {
			return found
		}
		if time.Now().After(deadline) {
			a.t.Fatalf("%s %s never matched, last %+v", c.method, c.path, found)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestHubLinkReportsDevices(t *testing.T) {
	api := newAPI(t)
	ada := register(api)
	shop := createProject(api, ada.AccessToken, "Shop")
	reg := registerHub(api, ada.AccessToken, shop.ID, "Office Mac")
	devices := call{method: http.MethodGet, path: "/projects/" + shop.ID + "/devices", token: ada.AccessToken}
	hubs := call{method: http.MethodGet, path: "/projects/" + shop.ID + "/hubs", token: ada.AccessToken}
	phones := &bench{}
	phones.plug("R58M1")

	running := api.startHub(reg.Token, phones)

	online := await(api, devices, func(d []deviceBody) bool { return len(d) == 1 && d[0].Status == "online" })
	if online[0].Serial != "R58M1" || online[0].HubName != "Office Mac" || online[0].Platform != "android" {
		t.Fatalf("device = %+v", online[0])
	}
	await(api, hubs, func(h []hubBody) bool { return len(h) == 1 && h[0].Online && h[0].Version == "e2e" })

	phones.plug("R58M2")
	await(api, devices, func(d []deviceBody) bool {
		return len(d) == 2 && d[0].Serial == "R58M1" && d[0].Status == "offline" && d[1].Status == "online"
	})

	running.stop()
	if err := running.wait(t); err != nil {
		t.Fatalf("hub stopped with %v", err)
	}
	await(api, hubs, func(h []hubBody) bool { return len(h) == 1 && !h[0].Online })
	await(api, devices, func(d []deviceBody) bool { return d[1].Status == "offline" })
}

func TestHubWithRotatedTokenIsRejected(t *testing.T) {
	api := newAPI(t)
	ada := register(api)
	shop := createProject(api, ada.AccessToken, "Shop")
	reg := registerHub(api, ada.AccessToken, shop.ID, "Office Mac")
	api.do(call{method: http.MethodPost, path: "/projects/" + shop.ID + "/hubs/" + reg.Hub.ID + "/token",
		token: ada.AccessToken}, http.StatusOK, nil)

	running := api.startHub(reg.Token, &bench{})
	defer running.stop()

	if err := running.wait(t); !errors.Is(err, hubws.ErrRejected) {
		t.Fatalf("hub stopped with %v, want ErrRejected", err)
	}
}

func TestPeopleWatchAndDriveDevicesFromTheBrowser(t *testing.T) {
	api := newAPI(t)
	ada := register(api)
	bob := registerAs(api, "bob@example.com")
	device := &phone{}
	b := onlineDevice(api, ada.AccessToken, device)
	api.do(call{method: http.MethodPost, path: "/projects/" + b.shop.ID + "/members", token: ada.AccessToken,
		body: map[string]string{"email": "bob@example.com", "role": "viewer"}}, http.StatusCreated, nil)
	path := "/projects/" + b.shop.ID + "/devices/" + b.device

	screen := api.send(call{method: http.MethodGet, path: path + "/screen", token: bob.AccessToken},
		rawBody{}, http.StatusOK, nil)
	if string(screen) != shot {
		t.Fatalf("screen = %q", screen)
	}
	tap := map[string]any{"kind": "tap", "x": 540, "y": 1875}
	api.do(call{method: http.MethodPost, path: path + "/commands", token: bob.AccessToken, body: tap},
		http.StatusForbidden, nil)
	api.do(call{method: http.MethodPost, path: path + "/commands", token: ada.AccessToken, body: tap},
		http.StatusNoContent, nil)
	api.do(call{method: http.MethodPost, path: path + "/commands", token: ada.AccessToken,
		body: map[string]any{"kind": "type", "text": "+77010000000"}}, http.StatusNoContent, nil)
	api.do(call{method: http.MethodPost, path: path + "/commands", token: ada.AccessToken,
		body: map[string]any{"kind": "teleport"}}, http.StatusBadRequest, nil)

	commands := device.commands()
	if len(commands) != 2 || commands[0].Kind != ui.KindTap || commands[0].Point != (ui.Point{X: 540, Y: 1875}) ||
		commands[0].Target.Serial != "R58M1" || commands[1].Text != "+77010000000" {
		t.Fatalf("commands = %+v", commands)
	}
}

func TestPhonesPublishAndPeopleWatchLiveScreens(t *testing.T) {
	api := newAPI(t)
	ada := register(api)
	bob := registerAs(api, "bob@example.com")
	b := onlineDevice(api, ada.AccessToken, &phone{})
	api.do(call{method: http.MethodPost, path: "/projects/" + b.shop.ID + "/members", token: ada.AccessToken,
		body: map[string]string{"email": "bob@example.com", "role": "viewer"}}, http.StatusCreated, nil)
	path := "/projects/" + b.shop.ID + "/devices/" + b.device
	sdp := func(c call, offer string, status int) string {
		return string(api.send(c, rawBody{reader: strings.NewReader(offer), contentType: "application/sdp"}, status, nil))
	}

	var ice struct {
		ICEServers []struct {
			URLs []string `json:"urls"`
		} `json:"ice_servers"`
	}
	api.do(call{method: http.MethodGet, path: "/ice-servers", token: bob.AccessToken}, http.StatusOK, &ice)
	if len(ice.ICEServers) != 1 || ice.ICEServers[0].URLs[0] != "stun:stun.example.com:3478" {
		t.Fatalf("ice servers = %+v", ice)
	}
	api.do(call{method: http.MethodPost, path: path + "/stream-key", token: bob.AccessToken}, http.StatusForbidden, nil)
	var key struct {
		Key string `json:"key"`
	}
	api.do(call{method: http.MethodPost, path: path + "/stream-key", token: ada.AccessToken}, http.StatusOK, &key)

	published := sdp(call{method: http.MethodPost, path: "/streams/publish", token: key.Key}, "v=0 phone",
		http.StatusCreated)
	watched := sdp(call{method: http.MethodPost, path: path + "/stream", token: bob.AccessToken}, "v=0 browser",
		http.StatusCreated)
	sdp(call{method: http.MethodPost, path: "/streams/publish", token: "mqs_guess"}, "v=0 phone",
		http.StatusUnauthorized)

	if published != "phone answer from R58M1 to v=0 phone" || watched != "viewer answer from R58M1 to v=0 browser" {
		t.Fatalf("published %q, watched %q", published, watched)
	}
}
