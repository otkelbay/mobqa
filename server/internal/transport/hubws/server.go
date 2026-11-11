package hubws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/coder/websocket"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/action"
	"github.com/mobqa/backend/internal/usecase/hub"
	"github.com/mobqa/backend/internal/usecase/run"
	"github.com/mobqa/backend/internal/usecase/ui"
)

var (
	errReplaced    = errors.New("replaced by a newer connection from the same hub")
	errShutdown    = errors.New("the API is shutting down")
	errNotAnswered = errors.New("the API serves no hub requests")
)

type Hubs interface {
	Authenticate(ctx context.Context, token string) (hub.Hub, error)
	ReportInventory(ctx context.Context, hubID string, inventory hub.Inventory) error
	Heartbeat(ctx context.Context, hubID string) error
	Disconnect(ctx context.Context, hubID string) error
}

// Server accepts hub connections and routes calls to the hub they name. A hub that reconnects replaces
// its previous connection, which is closed without marking the hub offline.
type Server struct {
	hubs      Hubs
	logger    *slog.Logger
	closing   chan struct{}
	closeOnce sync.Once
	sessions  sync.WaitGroup
	mu        sync.Mutex
	peers     map[string]*peer
}

func NewServer(hubs Hubs, logger *slog.Logger) *Server {
	return &Server{hubs: hubs, logger: logger, closing: make(chan struct{}), peers: map[string]*peer{}}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	h, err := s.hubs.Authenticate(r.Context(), token)
	if errors.Is(err, fault.ErrUnauthorized) {
		http.Error(w, "invalid hub token", http.StatusUnauthorized)
		return
	}
	if err != nil {
		s.logger.ErrorContext(r.Context(), "authenticate hub", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if s.shuttingDown() {
		http.Error(w, "shutting down", http.StatusServiceUnavailable)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		s.logger.WarnContext(r.Context(), "accept hub connection", "hub_id", h.ID, "error", err)
		return
	}
	s.sessions.Add(1)
	defer s.sessions.Done()
	// A hijacked request's context lives as long as this handler, so it bounds the session; shutdown
	// cancels it too.
	ctx, cancel := context.WithCancelCause(r.Context())
	defer cancel(nil)
	go func() {
		select {
		case <-s.closing:
			cancel(errShutdown)
		case <-ctx.Done():
		}
	}()
	s.serve(ctx, h.ID, conn)
}

func (s *Server) serve(ctx context.Context, hubID string, conn *websocket.Conn) {
	logger := s.logger.With("hub_id", hubID)
	p := newPeer(ctx, conn, &session{hubs: s.hubs, hubID: hubID}, logger)
	s.attach(hubID, p)
	logger.Info("hub connected")
	cause := p.run()
	if s.detach(hubID, p) {
		if err := s.hubs.Disconnect(context.WithoutCancel(ctx), hubID); err != nil {
			logger.Error("record hub disconnect", "error", err)
		}
	}
	logger.Info("hub disconnected", "cause", cause)
}

func (s *Server) attach(hubID string, p *peer) {
	s.mu.Lock()
	previous := s.peers[hubID]
	s.peers[hubID] = p
	s.mu.Unlock()
	if previous != nil {
		previous.close(errReplaced)
	}
}

// detach forgets the peer unless a newer connection has already taken its place, and reports whether
// it did.
func (s *Server) detach(hubID string, p *peer) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.peers[hubID] != p {
		return false
	}
	delete(s.peers, hubID)
	return true
}

// Call sends a request to a connected hub and returns its reply's payload.
func (s *Server) Call(ctx context.Context, hubID, typ string, payload any) (json.RawMessage, error) {
	s.mu.Lock()
	p := s.peers[hubID]
	s.mu.Unlock()
	if p == nil {
		return nil, fault.WithDetail(fault.ErrUnavailable, "the hub is not connected")
	}
	return p.call(ctx, typ, payload)
}

// Execute runs one attempt on a hub. A refusal or an unreadable reply from the hub is a *run.HubError.
func (s *Server) Execute(ctx context.Context, hubID string, execution run.Execution) (run.Outcome, error) {
	payload, err := s.Call(ctx, hubID, TypeExecute, newExecution(execution))
	var remote *RemoteError
	if errors.As(err, &remote) {
		return run.Outcome{}, &run.HubError{Message: remote.Message}
	}
	if err != nil {
		return run.Outcome{}, err
	}
	var outcome Outcome
	if err := json.Unmarshal(payload, &outcome); err != nil {
		return run.Outcome{}, fmt.Errorf("%w: %w", &run.HubError{Message: "the hub sent an unreadable outcome"}, err)
	}
	return outcome.run(), nil
}

// Observe reads what a device on a hub shows. A refusal or an unreadable reply is a *ui.DeviceError.
func (s *Server) Observe(ctx context.Context, hubID string, target ui.Target) (ui.Screen, error) {
	payload, err := s.Call(ctx, hubID, TypeObserve, newTarget(target))
	if err != nil {
		return ui.Screen{}, deviceError(err)
	}
	var screen Screen
	if err := json.Unmarshal(payload, &screen); err != nil {
		return ui.Screen{}, fmt.Errorf("%w: %w", &ui.DeviceError{Message: "the hub sent an unreadable screen"}, err)
	}
	return screen.ui(), nil
}

// Act carries out a command on a device on a hub. A refusal is a *ui.DeviceError.
func (s *Server) Act(ctx context.Context, hubID string, command ui.Command) error {
	_, err := s.Call(ctx, hubID, TypeAct, newCommand(command))
	return deviceError(err)
}

// Capture takes a screenshot of a device on a hub. A refusal or an unreadable reply is a *ui.DeviceError.
func (s *Server) Capture(ctx context.Context, hubID string, target ui.Target) ([]byte, error) {
	payload, err := s.Call(ctx, hubID, TypeCapture, newTarget(target))
	if err != nil {
		return nil, deviceError(err)
	}
	var shot Capture
	if err := json.Unmarshal(payload, &shot); err != nil {
		return nil, fmt.Errorf("%w: %w", &ui.DeviceError{Message: "the hub sent an unreadable screenshot"}, err)
	}
	return shot.JPEG, nil
}

// Perform sends an API action's request from a hub and returns the variables it extracted. A failed call
// is a *ui.DeviceError.
func (s *Server) Perform(ctx context.Context, hubID string, request action.Request, variables map[string]string,
) (map[string]string, error) {
	payload, err := s.Call(ctx, hubID, TypePerform, Perform{Action: newAction(request), Variables: variables})
	if err != nil {
		return nil, deviceError(err)
	}
	var performed Performed
	if err := json.Unmarshal(payload, &performed); err != nil {
		return nil, fmt.Errorf("%w: %w", &ui.DeviceError{Message: "the hub sent an unreadable reply"}, err)
	}
	return performed.Variables, nil
}

// Publish hands a phone's offer to stream a device's screen to its hub and returns the hub's answer. A
// refusal or an unreadable reply is a *ui.DeviceError.
func (s *Server) Publish(ctx context.Context, hubID string, target ui.Target, offer string) (string, error) {
	return s.stream(ctx, hubID, TypePublish, Offer{Serial: target.Serial, Platform: string(target.Platform), SDP: offer})
}

// Watch hands a browser's offer to receive a device's screen to its hub and returns the hub's answer. A
// refusal or an unreadable reply is a *ui.DeviceError.
func (s *Server) Watch(ctx context.Context, hubID string, target ui.Target, offer string) (string, error) {
	return s.stream(ctx, hubID, TypeWatch, Offer{Serial: target.Serial, Platform: string(target.Platform), SDP: offer})
}

func (s *Server) stream(ctx context.Context, hubID, typ string, offer Offer) (string, error) {
	payload, err := s.Call(ctx, hubID, typ, offer)
	if err != nil {
		return "", deviceError(err)
	}
	var answer Answer
	if err := json.Unmarshal(payload, &answer); err != nil {
		return "", fmt.Errorf("%w: %w", &ui.DeviceError{Message: "the hub sent an unreadable answer"}, err)
	}
	return answer.SDP, nil
}

func deviceError(err error) error {
	var remote *RemoteError
	if errors.As(err, &remote) {
		return &ui.DeviceError{Message: remote.Message}
	}
	return err
}

func (s *Server) shuttingDown() bool {
	select {
	case <-s.closing:
		return true
	default:
		return false
	}
}

// Shutdown closes every hub connection and waits for their sessions to end.
func (s *Server) Shutdown(ctx context.Context) error {
	s.closeOnce.Do(func() { close(s.closing) })
	done := make(chan struct{})
	go func() {
		s.sessions.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("hub sessions still open: %w", ctx.Err())
	}
}

// session handles what one hub sends. The hub's first event must be its inventory.
type session struct {
	hubs      Hubs
	hubID     string
	inventory bool
}

func (s *session) event(ctx context.Context, env Envelope) error {
	switch {
	case env.Type == TypeInventory:
		var inv Inventory
		if err := env.decode(&inv); err != nil {
			return fault.WithDetail(fault.ErrInvalidInput, "the inventory is not valid JSON")
		}
		if err := s.hubs.ReportInventory(ctx, s.hubID, inv.hub()); err != nil {
			return fmt.Errorf("report inventory: %w", err)
		}
		s.inventory = true
		return nil
	case !s.inventory:
		return fault.WithDetail(fault.ErrInvalidInput, "the first message must be the inventory")
	case env.Type == TypeHeartbeat:
		if err := s.hubs.Heartbeat(ctx, s.hubID); err != nil {
			return fmt.Errorf("record heartbeat: %w", err)
		}
		return nil
	default:
		return fault.WithDetail(fault.ErrInvalidInput, "unknown event type")
	}
}

func (s *session) request(context.Context, Envelope) (any, error) {
	return nil, errNotAnswered
}
