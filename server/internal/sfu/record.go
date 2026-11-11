package sfu

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4/pkg/media/h264writer"

	"github.com/mobqa/backend/internal/usecase/agent"
)

var (
	errNoFFmpeg = errors.New("recording needs ffmpeg")
	errNoFrames = errors.New("no video arrived while recording")
)

// Recording films a device's stream. It keeps the H.264 the phone sends as it is and, when stopped, has
// ffmpeg wrap it in an mp4 without transcoding, so recording costs the hub almost nothing.
type Recording struct {
	stream  *stream
	ffmpeg  string
	raw     *os.File
	started time.Time
	mu      sync.Mutex
	writer  *h264writer.H264Writer
	frames  int
	failure error
}

// Record starts filming the device's stream. It reports agent.ErrNotStreaming when the device's phone is
// not publishing.
func (s *SFU) Record(serial string) (agent.Recording, error) {
	if s.ffmpeg == "" {
		return nil, errNoFFmpeg
	}
	s.mu.Lock()
	st, ok := s.streams[serial]
	s.mu.Unlock()
	if !ok || !st.publishing() {
		return nil, agent.ErrNotStreaming
	}
	raw, err := os.CreateTemp("", "mobqa-recording-*.h264")
	if err != nil {
		return nil, fmt.Errorf("create raw recording: %w", err)
	}
	r := &Recording{
		stream: st, ffmpeg: s.ffmpeg, raw: raw, started: time.Now(), writer: h264writer.NewWith(raw),
	}
	st.addRecording(r)
	st.requestKeyframe()
	return r, nil
}

// write keeps one packet; a recording that cannot write stops keeping them and reports why when stopped.
func (r *Recording) write(packet *rtp.Packet) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failure != nil {
		return
	}
	if err := r.writer.WriteRTP(packet); err != nil {
		r.failure = fmt.Errorf("write raw recording: %w", err)
		return
	}
	if packet.Marker {
		r.frames++
	}
}

// Stop ends the recording and writes it as an mp4 next to the raw stream.
func (r *Recording) Stop(ctx context.Context) (agent.Video, error) {
	elapsed := time.Since(r.started)
	frames, err := r.end()
	defer func() { _ = os.Remove(r.raw.Name()) }()
	if err != nil {
		return agent.Video{}, err
	}
	if frames == 0 {
		return agent.Video{}, errNoFrames
	}
	path := strings.TrimSuffix(r.raw.Name(), ".h264") + ".mp4"
	// Raw H.264 carries no timestamps; the phone's average frame rate makes the video as long as the
	// recording was.
	rate := strconv.FormatFloat(float64(frames)/elapsed.Seconds(), 'f', 3, 64)
	command := exec.CommandContext(ctx, r.ffmpeg, "-y", "-loglevel", "error", "-r", rate, "-i", r.raw.Name(),
		"-c", "copy", "-movflags", "+faststart", path)
	if out, err := command.CombinedOutput(); err != nil {
		_ = os.Remove(path)
		return agent.Video{}, fmt.Errorf("ffmpeg: %w: %s", err, out)
	}
	return agent.Video{Path: path, Duration: elapsed}, nil
}

func (r *Recording) Discard() {
	_, _ = r.end()
	_ = os.Remove(r.raw.Name())
}

// end stops keeping packets and closes the raw stream, returning how many frames it holds.
func (r *Recording) end() (int, error) {
	r.stream.dropRecording(r)
	r.mu.Lock()
	defer r.mu.Unlock()
	closeErr := r.writer.Close()
	if r.failure != nil {
		return 0, r.failure
	}
	if closeErr != nil {
		return 0, fmt.Errorf("close raw recording: %w", closeErr)
	}
	return r.frames, nil
}
