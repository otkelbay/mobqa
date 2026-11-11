package sfu

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/pion/webrtc/v4/pkg/media/h264reader"

	"github.com/mobqa/backend/internal/usecase/agent"
)

// screenFrames encodes two seconds of a test pattern as the NAL units a phone would send.
func screenFrames(t *testing.T, ffmpeg string) [][]byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "screen.h264")
	encode := exec.CommandContext(t.Context(), ffmpeg, "-loglevel", "error", "-f", "lavfi", "-i",
		"testsrc=size=160x120:rate=25", "-t", "2", "-c:v", "libx264", "-g", "10", "-bsf:v", "h264_mp4toannexb",
		"-f", "h264", path)
	if out, err := encode.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg cannot encode H.264 here: %v: %s", err, out)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = file.Close() }()
	reader, err := h264reader.NewReader(file)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	var frames [][]byte
	for {
		nal, err := reader.NextNAL()
		if errors.Is(err, io.EOF) {
			return frames
		}
		if err != nil {
			t.Fatalf("NextNAL: %v", err)
		}
		frames = append(frames, nal.Data)
	}
}

func TestRecordingsOfAStreamBecomeMP4(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	s, err := newSFU(Config{FFmpeg: ffmpeg}, loopback(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("newSFU: %v", err)
	}
	t.Cleanup(s.Close)
	if _, err := s.Record("R58M1"); !errors.Is(err, agent.ErrNotStreaming) {
		t.Fatalf("Record before publishing error = %v, want ErrNotStreaming", err)
	}
	publish(t, s, screenFrames(t, ffmpeg))

	recording, err := s.Record("R58M1")
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	time.Sleep(1500 * time.Millisecond)
	video, err := recording.Stop(t.Context())

	if err != nil || video.Duration < 1500*time.Millisecond {
		t.Fatalf("Stop = %+v, %v", video, err)
	}
	t.Cleanup(func() { _ = os.Remove(video.Path) })
	content, err := os.ReadFile(video.Path)
	if err != nil || len(content) < 12 || !bytes.Equal(content[4:8], []byte("ftyp")) {
		t.Fatalf("recording is not an mp4: %d bytes, %v", len(content), err)
	}
}

func TestRecordingWithoutFFmpegIsRefused(t *testing.T) {
	s := newTestSFU(t)
	publish(t, s, keyframe())

	if _, err := s.Record("R58M1"); !errors.Is(err, errNoFFmpeg) {
		t.Fatalf("Record error = %v, want errNoFFmpeg", err)
	}
}

func TestDiscardedRecordingsLeaveNothingBehind(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	s, err := newSFU(Config{FFmpeg: ffmpeg}, loopback(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("newSFU: %v", err)
	}
	t.Cleanup(s.Close)
	publish(t, s, keyframe())
	recording, err := s.Record("R58M1")
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	recording.Discard()

	if _, err := os.Stat(recording.(*Recording).raw.Name()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("raw stream left behind: %v", err)
	}
}
