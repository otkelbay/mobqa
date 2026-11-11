package app

import (
	"context"
	"log/slog"
	"os/exec"
	"runtime"
	"runtime/debug"

	"github.com/mobqa/backend/internal/clock"
	"github.com/mobqa/backend/internal/config"
	"github.com/mobqa/backend/internal/device/adb"
	"github.com/mobqa/backend/internal/device/devicectl"
	"github.com/mobqa/backend/internal/httpcall"
	"github.com/mobqa/backend/internal/sfu"
	"github.com/mobqa/backend/internal/transport/hubws"
	"github.com/mobqa/backend/internal/usecase/agent"
	"github.com/mobqa/backend/internal/usecase/project"
)

// RunHub keeps the machine's devices connected to the API until ctx ends or the API rejects the token.
func RunHub(ctx context.Context, cfg config.Hub, logger *slog.Logger) error {
	var discoverers []agent.Discoverer
	controllers := map[project.Platform]agent.Controller{}
	installers := map[project.Platform]agent.Installer{}
	capturers := map[project.Platform]agent.Capturer{}
	if path, err := exec.LookPath(cfg.ADBPath); err == nil {
		discoverers = append(discoverers, adb.New(path))
		controller := adb.NewController(path)
		controllers[project.PlatformAndroid], installers[project.PlatformAndroid] = controller, controller
		capturers[project.PlatformAndroid] = controller
	} else {
		logger.Info("android discovery disabled: adb not found", "adb_path", cfg.ADBPath)
	}
	if _, err := exec.LookPath("xcrun"); err == nil && runtime.GOOS == "darwin" {
		discoverers = append(discoverers, devicectl.New())
	} else {
		logger.Info("ios discovery disabled: needs macOS with Xcode")
	}
	packages, err := hubws.NewPackages(hubws.PackagesConfig{URL: cfg.APIURL, Token: cfg.Token, Dir: cfg.CacheDir})
	if err != nil {
		return err
	}
	ffmpeg, err := exec.LookPath(cfg.FFmpegPath)
	if err != nil {
		logger.Info("filming attempts disabled: ffmpeg not found", "ffmpeg_path", cfg.FFmpegPath)
	}
	streams, err := sfu.New(sfu.Config{ICEServers: cfg.ICEServers, FFmpeg: ffmpeg}, logger.With("component", "sfu"))
	if err != nil {
		return err
	}
	defer streams.Close()
	sender := httpcall.New()
	var runner agent.Runner = agent.NewPlayer(agent.PlayerConfig{Wait: cfg.ElementWait, Settle: cfg.Settle},
		agent.PlayerDependencies{
			Controllers: controllers, Installers: installers, Packages: packages, Sender: sender, Clock: clock.System{},
		})
	if ffmpeg != "" {
		runner = agent.NewFilm(runner, streams, hubws.NewVideos(hubws.VideosConfig{URL: cfg.APIURL, Token: cfg.Token}))
	}
	client := hubws.NewClient(
		hubws.ClientConfig{URL: cfg.APIURL, Token: cfg.Token, PollInterval: cfg.PollInterval},
		agent.New(agent.Config{
			Version: version(), Discoverers: discoverers, Controllers: controllers, Capturers: capturers, Sender: sender,
			Streamer: streams, Runner: runner,
		}),
		logger.With("component", "hubws"),
	)
	logger.Info("hub started", "api_url", cfg.APIURL)
	return client.Run(ctx)
}

func version() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		return info.Main.Version
	}
	return "unknown"
}
