package http

import (
	"context"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/device"
	"github.com/mobqa/backend/internal/usecase/hub"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/ui"
)

type Hubs interface {
	Register(ctx context.Context, in hub.RegisterInput) (hub.Registration, error)
	List(ctx context.Context, in hub.ListInput) ([]hub.Hub, error)
	RotateToken(ctx context.Context, ref hub.Ref) (hub.Registration, error)
	Delete(ctx context.Context, ref hub.Ref) error
}

type Devices interface {
	List(ctx context.Context, in device.ListInput) ([]device.Device, error)
	Get(ctx context.Context, ref device.Ref) (device.Device, error)
}

type Control interface {
	Screen(ctx context.Context, ref device.Ref) ([]byte, error)
	Act(ctx context.Context, in device.ActInput) error
}

type Streams interface {
	RotateStreamKey(ctx context.Context, ref device.Ref) (device.StreamKey, error)
	Publish(ctx context.Context, key, offer string) (string, error)
	Watch(ctx context.Context, in device.WatchInput) (string, error)
}

type commandRequest struct {
	Kind       string `json:"kind"`
	X          int    `json:"x"`
	Y          int    `json:"y"`
	ToX        int    `json:"to_x"`
	ToY        int    `json:"to_y"`
	Text       string `json:"text"`
	DurationMS int64  `json:"duration_ms"`
}

type registerHubRequest struct {
	Name string `json:"name"`
}

type hubResponse struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Version    string     `json:"version"`
	Online     bool       `json:"online"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

type registrationResponse struct {
	Hub   hubResponse `json:"hub"`
	Token string      `json:"token"`
}

type deviceResponse struct {
	ID         string    `json:"id"`
	HubID      string    `json:"hub_id"`
	HubName    string    `json:"hub_name"`
	Serial     string    `json:"serial"`
	Name       string    `json:"name"`
	Model      string    `json:"model"`
	Platform   string    `json:"platform"`
	OSVersion  string    `json:"os_version"`
	Physical   bool      `json:"physical"`
	Status     string    `json:"status"`
	LastSeenAt time.Time `json:"last_seen_at"`
}

type streamKeyResponse struct {
	Device deviceResponse `json:"device"`
	Key    string         `json:"key"`
}

// iceServersResponse has the shape of RTCConfiguration, so a browser can pass it on as it is.
type iceServersResponse struct {
	ICEServers []iceServerResponse `json:"ice_servers"`
}

type iceServerResponse struct {
	URLs []string `json:"urls"`
}

func newHubResponse(h hub.Hub) hubResponse {
	return hubResponse{
		ID: h.ID, Name: h.Name, Version: h.Version, Online: h.Online, LastSeenAt: h.LastSeenAt, CreatedAt: h.CreatedAt,
	}
}

func newRegistrationResponse(reg hub.Registration) registrationResponse {
	return registrationResponse{Hub: newHubResponse(reg.Hub), Token: reg.Token}
}

func newDeviceResponse(d device.Device) deviceResponse {
	return deviceResponse{
		ID: d.ID, HubID: d.HubID, HubName: d.HubName, Serial: d.Serial, Name: d.Name, Model: d.Model,
		Platform: string(d.Platform), OSVersion: d.OSVersion, Physical: d.Physical, Status: string(d.Status),
		LastSeenAt: d.LastSeenAt,
	}
}

type hubHandler struct {
	hubs   Hubs
	logger *slog.Logger
}

func (h hubHandler) ref(r *http.Request) hub.Ref {
	return hub.Ref{UserID: currentUserID(r.Context()), ProjectID: r.PathValue("projectID"), HubID: r.PathValue("hubID")}
}

func (h hubHandler) list(w http.ResponseWriter, r *http.Request) {
	found, err := h.hubs.List(r.Context(),
		hub.ListInput{UserID: currentUserID(r.Context()), ProjectID: r.PathValue("projectID")})
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	items := make([]hubResponse, len(found))
	for i, item := range found {
		items[i] = newHubResponse(item)
	}
	writeJSON(w, http.StatusOK, items)
}

func (h hubHandler) register(w http.ResponseWriter, r *http.Request) {
	var request registerHubRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	reg, err := h.hubs.Register(r.Context(), hub.RegisterInput{
		UserID: currentUserID(r.Context()), ProjectID: r.PathValue("projectID"), Name: request.Name,
	})
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusCreated, newRegistrationResponse(reg))
}

func (h hubHandler) rotateToken(w http.ResponseWriter, r *http.Request) {
	reg, err := h.hubs.RotateToken(r.Context(), h.ref(r))
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, newRegistrationResponse(reg))
}

func (h hubHandler) remove(w http.ResponseWriter, r *http.Request) {
	if err := h.hubs.Delete(r.Context(), h.ref(r)); err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type deviceHandler struct {
	devices    Devices
	control    Control
	streams    Streams
	iceServers []string
	logger     *slog.Logger
}

func (h deviceHandler) ref(r *http.Request) device.Ref {
	return device.Ref{
		UserID: currentUserID(r.Context()), ProjectID: r.PathValue("projectID"), DeviceID: r.PathValue("deviceID"),
	}
}

func (h deviceHandler) list(w http.ResponseWriter, r *http.Request) {
	found, err := h.devices.List(r.Context(), device.ListInput{
		UserID: currentUserID(r.Context()), ProjectID: r.PathValue("projectID"),
		Platform: project.Platform(r.URL.Query().Get("platform")),
	})
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	items := make([]deviceResponse, len(found))
	for i, item := range found {
		items[i] = newDeviceResponse(item)
	}
	writeJSON(w, http.StatusOK, items)
}

func (h deviceHandler) get(w http.ResponseWriter, r *http.Request) {
	found, err := h.devices.Get(r.Context(), h.ref(r))
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, newDeviceResponse(found))
}

func (h deviceHandler) screen(w http.ResponseWriter, r *http.Request) {
	shot, err := h.control.Screen(r.Context(), h.ref(r))
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(shot)
}

func (h deviceHandler) act(w http.ResponseWriter, r *http.Request) {
	var request commandRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	err := h.control.Act(r.Context(), device.ActInput{Ref: h.ref(r), Command: ui.Command{
		Kind: ui.Kind(request.Kind), Point: ui.Point{X: request.X, Y: request.Y},
		To: ui.Point{X: request.ToX, Y: request.ToY}, Text: request.Text,
		Duration: time.Duration(request.DurationMS) * time.Millisecond,
	}})
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h deviceHandler) rotateStreamKey(w http.ResponseWriter, r *http.Request) {
	key, err := h.streams.RotateStreamKey(r.Context(), h.ref(r))
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, streamKeyResponse{Device: newDeviceResponse(key.Device), Key: key.Key})
}

// watch takes a browser's offer as in WHEP: the SDP is the body, with every candidate in it.
func (h deviceHandler) watch(w http.ResponseWriter, r *http.Request) {
	offer, err := readOffer(w, r)
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	answer, err := h.streams.Watch(r.Context(), device.WatchInput{Ref: h.ref(r), Offer: offer})
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	writeAnswer(w, answer)
}

// publish takes a phone's offer as in WHIP, authenticated by the device's stream key.
func (h deviceHandler) publish(w http.ResponseWriter, r *http.Request) {
	offer, err := readOffer(w, r)
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	key, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	answer, err := h.streams.Publish(r.Context(), key, offer)
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	writeAnswer(w, answer)
}

func (h deviceHandler) listICEServers(w http.ResponseWriter, _ *http.Request) {
	servers := []iceServerResponse{}
	if len(h.iceServers) > 0 {
		servers = append(servers, iceServerResponse{URLs: h.iceServers})
	}
	writeJSON(w, http.StatusOK, iceServersResponse{ICEServers: servers})
}

func readOffer(w http.ResponseWriter, r *http.Request) (string, error) {
	if media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); media != "application/sdp" {
		return "", fault.WithDetail(fault.ErrInvalidInput, "the offer must be sent as application/sdp")
	}
	offer, err := io.ReadAll(http.MaxBytesReader(w, r.Body, device.MaxOfferBytes))
	if err != nil {
		return "", fault.WithDetail(fault.ErrInvalidInput, "the offer must be an SDP session description")
	}
	return string(offer), nil
}

func writeAnswer(w http.ResponseWriter, answer string) {
	w.Header().Set("Content-Type", "application/sdp")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusCreated)
	_, _ = io.WriteString(w, answer)
}
