package hubws

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// VideosPath is where a hub sends the video of an attempt it filmed, at VideosPath + "<result>/<number>".
const VideosPath = "/hub/videos/"

type VideosConfig struct {
	// URL is the API's base URL, http(s) or ws(s).
	URL   string
	Token string
}

// Videos uploads the videos of filmed attempts to the API.
type Videos struct {
	base   string
	token  string
	client *http.Client
}

func NewVideos(cfg VideosConfig) *Videos {
	return &Videos{base: httpBase(cfg.URL), token: cfg.Token, client: &http.Client{}}
}

// Upload sends the mp4 at path as the video of the execution, whose ID names its result and attempt.
func (v *Videos) Upload(ctx context.Context, executionID, path string) error {
	if strings.Count(executionID, "/") != 1 {
		return fmt.Errorf("execution %q does not name a result and an attempt", executionID)
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open video: %w", err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("read video size: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, v.base+VideosPath+executionID, file)
	if err != nil {
		return fmt.Errorf("build video request: %w", err)
	}
	request.ContentLength = info.Size()
	request.Header.Set("Content-Type", "video/mp4")
	request.Header.Set("Authorization", "Bearer "+v.token)
	response, err := v.client.Do(request)
	if err != nil {
		return fmt.Errorf("upload video: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("upload video: the API answered %s", response.Status)
	}
	return nil
}
