// Package sfu relays the live screens of a hub's devices over WebRTC. The phone encodes its screen as
// H.264 once and publishes it to the hub; the hub copies the packets to every browser watching, without
// decoding them. Signalling is one offer and one answer per peer, with every ICE candidate in the SDP.
package sfu

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
)

var errClosed = errors.New("the hub is shutting down")

type Config struct {
	// ICEServers are STUN or TURN URLs that let peers outside the hub's network reach it.
	ICEServers []string
	// FFmpeg is the ffmpeg binary that turns recordings into mp4; without it the hub does not record.
	FFmpeg string
}

// SFU keeps one stream per device. A stream outlives its peers: viewers who joined before the phone
// published see the screen as soon as it does, and keep watching when the phone publishes again.
type SFU struct {
	api     *webrtc.API
	config  webrtc.Configuration
	ffmpeg  string
	logger  *slog.Logger
	mu      sync.Mutex
	streams map[string]*stream
	closed  bool
}

func New(cfg Config, logger *slog.Logger) (*SFU, error) {
	return newSFU(cfg, webrtc.SettingEngine{}, logger)
}

func newSFU(cfg Config, settings webrtc.SettingEngine, logger *slog.Logger) (*SFU, error) {
	media := &webrtc.MediaEngine{}
	if err := media.RegisterDefaultCodecs(); err != nil {
		return nil, fmt.Errorf("register codecs: %w", err)
	}
	config := webrtc.Configuration{}
	if len(cfg.ICEServers) > 0 {
		config.ICEServers = []webrtc.ICEServer{{URLs: cfg.ICEServers}}
	}
	return &SFU{
		api:    webrtc.NewAPI(webrtc.WithMediaEngine(media), webrtc.WithSettingEngine(settings)),
		config: config, ffmpeg: cfg.FFmpeg, logger: logger, streams: map[string]*stream{},
	}, nil
}

// Publish answers the phone's offer to send the device's screen. A new offer from the same device
// replaces the previous phone connection.
func (s *SFU) Publish(ctx context.Context, serial, offer string) (string, error) {
	st, err := s.stream(serial)
	if err != nil {
		return "", err
	}
	pc, err := s.api.NewPeerConnection(s.config)
	if err != nil {
		return "", fmt.Errorf("create peer connection: %w", err)
	}
	if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		_ = pc.Close()
		return "", fmt.Errorf("add video transceiver: %w", err)
	}
	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		st.forward(pc, track)
	})
	st.watchState(pc, "phone", nil, func() { st.dropPhone(pc) })
	answer, err := negotiate(ctx, pc, offer)
	if err != nil {
		return "", err
	}
	st.setPhone(pc)
	return answer, nil
}

// Watch answers a browser's offer to receive the device's screen.
func (s *SFU) Watch(ctx context.Context, serial, offer string) (string, error) {
	st, err := s.stream(serial)
	if err != nil {
		return "", err
	}
	pc, err := s.api.NewPeerConnection(s.config)
	if err != nil {
		return "", fmt.Errorf("create peer connection: %w", err)
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer}); err != nil {
		_ = pc.Close()
		return "", fmt.Errorf("read offer: %w", err)
	}
	sender, err := pc.AddTrack(st.track)
	if err != nil {
		_ = pc.Close()
		return "", fmt.Errorf("add screen track: %w", err)
	}
	go st.relayFeedback(sender)
	st.watchState(pc, "viewer", st.requestKeyframe, func() { st.dropViewer(pc) })
	answer, err := answer(ctx, pc)
	if err != nil {
		return "", err
	}
	st.addViewer(pc)
	return answer, nil
}

// Close ends every stream and refuses new ones.
func (s *SFU) Close() {
	s.mu.Lock()
	streams := s.streams
	s.streams, s.closed = map[string]*stream{}, true
	s.mu.Unlock()
	for _, st := range streams {
		st.close()
	}
}

func (s *SFU) stream(serial string) (*stream, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errClosed
	}
	if st, ok := s.streams[serial]; ok {
		return st, nil
	}
	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264}, "screen",
		serial)
	if err != nil {
		return nil, fmt.Errorf("create screen track: %w", err)
	}
	st := &stream{
		serial: serial, logger: s.logger, track: track, viewers: map[*webrtc.PeerConnection]struct{}{},
		recordings: map[*Recording]struct{}{},
	}
	s.streams[serial] = st
	return st, nil
}

func negotiate(ctx context.Context, pc *webrtc.PeerConnection, offer string) (string, error) {
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer}); err != nil {
		_ = pc.Close()
		return "", fmt.Errorf("read offer: %w", err)
	}
	return answer(ctx, pc)
}

// answer creates the local answer and waits until it lists every ICE candidate, since there is no
// channel to trickle them later.
func answer(ctx context.Context, pc *webrtc.PeerConnection) (string, error) {
	created, err := pc.CreateAnswer(nil)
	if err != nil {
		_ = pc.Close()
		return "", fmt.Errorf("create answer: %w", err)
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(created); err != nil {
		_ = pc.Close()
		return "", fmt.Errorf("set answer: %w", err)
	}
	select {
	case <-gathered:
		return pc.LocalDescription().SDP, nil
	case <-ctx.Done():
		_ = pc.Close()
		return "", ctx.Err()
	}
}

// stream is one device's screen: the phone connection that sends it, and the viewers and recordings that
// receive it.
type stream struct {
	serial     string
	logger     *slog.Logger
	track      *webrtc.TrackLocalStaticRTP
	mu         sync.Mutex
	phone      *webrtc.PeerConnection
	ssrc       webrtc.SSRC
	viewers    map[*webrtc.PeerConnection]struct{}
	recordings map[*Recording]struct{}
}

// watchState calls connected once the peer connects, and releases the peer with gone once its connection
// fails or the other side closes it.
func (st *stream) watchState(pc *webrtc.PeerConnection, role string, connected, gone func()) {
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		st.logger.Debug("webrtc peer", "serial", st.serial, "role", role, "state", state.String())
		switch state {
		case webrtc.PeerConnectionStateConnected:
			if connected != nil {
				connected()
			}
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			gone()
			_ = pc.Close()
		}
	})
}

func (st *stream) setPhone(pc *webrtc.PeerConnection) {
	st.mu.Lock()
	previous := st.phone
	st.phone = pc
	st.mu.Unlock()
	if previous != nil {
		_ = previous.Close()
	}
}

func (st *stream) dropPhone(pc *webrtc.PeerConnection) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.phone == pc {
		st.phone = nil
	}
}

func (st *stream) publishing() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.phone != nil
}

func (st *stream) addRecording(r *Recording) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.recordings[r] = struct{}{}
}

func (st *stream) dropRecording(r *Recording) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.recordings, r)
}

func (st *stream) addViewer(pc *webrtc.PeerConnection) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.viewers[pc] = struct{}{}
}

func (st *stream) dropViewer(pc *webrtc.PeerConnection) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.viewers, pc)
}

// forward copies the phone's packets to the shared track and the recordings until the phone's track
// ends. A failed write means a viewer has gone, which must not stop the stream for the others.
func (st *stream) forward(pc *webrtc.PeerConnection, track *webrtc.TrackRemote) {
	st.mu.Lock()
	if st.phone != nil && st.phone != pc {
		st.mu.Unlock()
		return
	}
	st.ssrc = track.SSRC()
	st.mu.Unlock()
	for {
		packet, _, err := track.ReadRTP()
		if err != nil {
			return
		}
		_ = st.track.WriteRTP(packet)
		st.mu.Lock()
		for r := range st.recordings {
			r.write(packet)
		}
		st.mu.Unlock()
	}
}

// relayFeedback passes a viewer's requests for a keyframe on to the phone, which encodes one. Without it a
// viewer who lost packets would wait for the phone's next scheduled keyframe.
func (st *stream) relayFeedback(sender *webrtc.RTPSender) {
	for {
		packets, _, err := sender.ReadRTCP()
		if err != nil {
			return
		}
		for _, packet := range packets {
			switch packet.(type) {
			case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
				st.requestKeyframe()
			}
		}
	}
}

func (st *stream) requestKeyframe() {
	st.mu.Lock()
	phone, ssrc := st.phone, st.ssrc
	st.mu.Unlock()
	if phone == nil || ssrc == 0 {
		return
	}
	_ = phone.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: uint32(ssrc)}})
}

func (st *stream) close() {
	st.mu.Lock()
	peers := make([]*webrtc.PeerConnection, 0, len(st.viewers)+1)
	if st.phone != nil {
		peers = append(peers, st.phone)
	}
	for pc := range st.viewers {
		peers = append(peers, pc)
	}
	st.phone, st.viewers = nil, map[*webrtc.PeerConnection]struct{}{}
	st.mu.Unlock()
	for _, pc := range peers {
		_ = pc.Close()
	}
}
