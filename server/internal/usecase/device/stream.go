package device

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mobqa/backend/internal/clock"
	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/ui"
)

const (
	streamKeyPrefix = "mqs_"
	// MaxOfferBytes bounds an offer; a real one, with every candidate, is a few kilobytes.
	MaxOfferBytes = 64 << 10
	// negotiationTimeout covers the hub gathering its candidates, which waits on its STUN servers.
	negotiationTimeout = 15 * time.Second
)

// Relay hands offers for a device's live screen to its hub, which answers them. It reports ErrUnavailable
// when the hub cannot be reached and a *ui.DeviceError when the hub refused.
type Relay interface {
	Publish(ctx context.Context, hubID string, target ui.Target, offer string) (string, error)
	Watch(ctx context.Context, hubID string, target ui.Target, offer string) (string, error)
}

type streamStore interface {
	store
	SetStreamKeyHash(ctx context.Context, projectID, id string, hash []byte) (Device, error)
	DeviceByStreamKey(ctx context.Context, hash []byte) (Device, error)
}

type StreamDependencies struct {
	Store  streamStore
	Access access
	Relay  Relay
	Clock  clock.Clock
}

// Streams connects the phone that publishes a device's screen and the people who watch it to the SFU on
// the device's hub. The API only passes the offers and answers along; the video never reaches it.
type Streams struct {
	deps StreamDependencies
}

func NewStreams(deps StreamDependencies) *Streams {
	return &Streams{deps: deps}
}

// StreamKey is shown once; only its hash is kept.
type StreamKey struct {
	Device Device
	Key    string
}

// RotateStreamKey gives a device a new key for the phone to publish its screen with. The old key stops
// working at once.
func (s *Streams) RotateStreamKey(ctx context.Context, ref Ref) (StreamKey, error) {
	if _, err := s.deps.Access.Require(ctx, ref.ProjectID, ref.UserID, project.RoleAdmin); err != nil {
		return StreamKey{}, err
	}
	key := streamKeyPrefix + rand.Text()
	updated, err := s.deps.Store.SetStreamKeyHash(ctx, ref.ProjectID, ref.DeviceID, hashKey(key))
	if errors.Is(err, fault.ErrNotFound) {
		return StreamKey{}, fault.WithDetail(fault.ErrNotFound, "device not found")
	}
	if err != nil {
		return StreamKey{}, fmt.Errorf("rotate stream key: %w", err)
	}
	updated.Status = updated.StatusAt(s.deps.Clock.Now())
	return StreamKey{Device: updated, Key: key}, nil
}

// Publish answers the offer of a phone that holds the device's stream key.
func (s *Streams) Publish(ctx context.Context, key, offer string) (string, error) {
	if err := validateOffer(offer); err != nil {
		return "", err
	}
	d, err := s.deps.Store.DeviceByStreamKey(ctx, hashKey(key))
	if errors.Is(err, fault.ErrNotFound) {
		return "", fault.WithDetail(fault.ErrUnauthorized, "invalid stream key")
	}
	if err != nil {
		return "", fmt.Errorf("load device by stream key: %w", err)
	}
	return s.relay(ctx, d, offer, s.deps.Relay.Publish)
}

type WatchInput struct {
	Ref   Ref
	Offer string
}

// Watch answers a person's offer to receive the device's screen.
func (s *Streams) Watch(ctx context.Context, in WatchInput) (string, error) {
	if err := validateOffer(in.Offer); err != nil {
		return "", err
	}
	if _, err := s.deps.Access.Require(ctx, in.Ref.ProjectID, in.Ref.UserID, project.RoleViewer); err != nil {
		return "", err
	}
	d, err := s.deps.Store.Device(ctx, in.Ref.ProjectID, in.Ref.DeviceID)
	if errors.Is(err, fault.ErrNotFound) {
		return "", fault.WithDetail(fault.ErrNotFound, "device not found")
	}
	if err != nil {
		return "", fmt.Errorf("load device: %w", err)
	}
	return s.relay(ctx, d, in.Offer, s.deps.Relay.Watch)
}

func (s *Streams) relay(ctx context.Context, d Device, offer string,
	send func(context.Context, string, ui.Target, string) (string, error),
) (string, error) {
	if d.StatusAt(s.deps.Clock.Now()) != StatusOnline {
		return "", fault.WithDetail(fault.ErrConflict, "the device is not online")
	}
	ctx, cancel := context.WithTimeout(ctx, negotiationTimeout)
	defer cancel()
	answer, err := send(ctx, d.HubID, ui.Target{Serial: d.Serial, Platform: d.Platform}, offer)
	if err != nil {
		return "", refusal(err, "stream its screen")
	}
	return answer, nil
}

func validateOffer(offer string) error {
	if !strings.HasPrefix(offer, "v=0") || len(offer) > MaxOfferBytes {
		return fault.WithDetail(fault.ErrInvalidInput, "the offer must be an SDP session description")
	}
	return nil
}

func hashKey(key string) []byte {
	sum := sha256.Sum256([]byte(key))
	return sum[:]
}
