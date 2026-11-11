package sfu

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

// loopback lets peers on one machine reach each other without a network interface.
func loopback() webrtc.SettingEngine {
	settings := webrtc.SettingEngine{}
	settings.SetIncludeLoopbackCandidate(true)
	settings.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	return settings
}

func newTestSFU(t *testing.T) *SFU {
	t.Helper()
	s, err := newSFU(Config{}, loopback(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("newSFU: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

func newPeer(t *testing.T) *webrtc.PeerConnection {
	t.Helper()
	media := &webrtc.MediaEngine{}
	if err := media.RegisterDefaultCodecs(); err != nil {
		t.Fatalf("RegisterDefaultCodecs: %v", err)
	}
	api := webrtc.NewAPI(webrtc.WithMediaEngine(media), webrtc.WithSettingEngine(loopback()))
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("NewPeerConnection: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	return pc
}

// connect offers from pc with every candidate gathered, hands the offer to signal and applies its answer.
func connect(t *testing.T, pc *webrtc.PeerConnection, signal func(context.Context, string) (string, error)) {
	t.Helper()
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatalf("CreateOffer: %v", err)
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		t.Fatalf("SetLocalDescription: %v", err)
	}
	<-gathered
	answer, err := signal(t.Context(), pc.LocalDescription().SDP)
	if err != nil {
		t.Fatalf("signal: %v", err)
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}); err != nil {
		t.Fatalf("SetRemoteDescription: %v", err)
	}
}

// watcher is a browser that reports the codec of the first track it receives.
func watcher(t *testing.T, s *SFU, serial string) <-chan string {
	t.Helper()
	pc := newPeer(t)
	if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatalf("AddTransceiverFromKind: %v", err)
	}
	received := make(chan string, 1)
	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if _, _, err := track.ReadRTP(); err == nil {
			received <- track.Codec().MimeType
		}
	})
	connect(t, pc, func(ctx context.Context, offer string) (string, error) { return s.Watch(ctx, serial, offer) })
	return received
}

// keyframe looks like a frame to a viewer, though no decoder could show it.
func keyframe() [][]byte {
	return [][]byte{{0, 0, 0, 1, 0x65, 0x88, 0x84, 0x00, 0x33}}
}

// publish is the phone of device R58M1, sending the NAL units of frames in a loop, one every 20ms, until the
// test ends.
func publish(t *testing.T, s *SFU, frames [][]byte) {
	t.Helper()
	pc := newPeer(t)
	track, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264}, "screen",
		"phone")
	if err != nil {
		t.Fatalf("NewTrackLocalStaticSample: %v", err)
	}
	if _, err := pc.AddTrack(track); err != nil {
		t.Fatalf("AddTrack: %v", err)
	}
	connect(t, pc, func(ctx context.Context, offer string) (string, error) { return s.Publish(ctx, "R58M1", offer) })
	go func() {
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for i := 0; ; i = (i + 1) % len(frames) {
			select {
			case <-t.Context().Done():
				return
			case <-tick.C:
				_ = track.WriteSample(media.Sample{Data: frames[i], Duration: 20 * time.Millisecond})
			}
		}
	}()
}

func await(t *testing.T, received <-chan string) {
	t.Helper()
	select {
	case codec := <-received:
		if codec != webrtc.MimeTypeH264 {
			t.Fatalf("codec = %s, want H.264", codec)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the viewer received no video")
	}
}

func TestViewersReceiveWhatThePhonePublishes(t *testing.T) {
	s := newTestSFU(t)

	early := watcher(t, s, "R58M1")
	publish(t, s, keyframe())
	late := watcher(t, s, "R58M1")

	await(t, early)
	await(t, late)
}

func TestStreamsAreKeptApartByDevice(t *testing.T) {
	s := newTestSFU(t)
	other := watcher(t, s, "00008120")

	publish(t, s, keyframe())
	await(t, watcher(t, s, "R58M1"))

	select {
	case <-other:
		t.Fatal("a viewer of another device received this device's screen")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestOffersThatAreNotSDPAreRefused(t *testing.T) {
	s := newTestSFU(t)

	if _, err := s.Publish(t.Context(), "R58M1", "hello"); err == nil || !strings.Contains(err.Error(), "read offer") {
		t.Fatalf("Publish error = %v", err)
	}
	if _, err := s.Watch(t.Context(), "R58M1", "hello"); err == nil || !strings.Contains(err.Error(), "read offer") {
		t.Fatalf("Watch error = %v", err)
	}
}

func TestClosedSFURefusesNewPeers(t *testing.T) {
	s := newTestSFU(t)
	s.Close()

	if _, err := s.Watch(t.Context(), "R58M1", "v=0"); !errors.Is(err, errClosed) {
		t.Fatalf("Watch error = %v, want errClosed", err)
	}
}
