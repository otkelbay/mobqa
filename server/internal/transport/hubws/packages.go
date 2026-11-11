package hubws

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/run"
)

// BuildsPath is where a hub downloads the builds of its project.
const BuildsPath = "/hub/builds/"

// keptPackages bounds the disk a hub spends on builds; runs mostly install the project's current one.
const keptPackages = 3

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type PackagesConfig struct {
	// URL is the API's base URL, http(s) or ws(s).
	URL   string
	Token string
	// Dir holds the downloaded builds.
	Dir string
}

// Packages downloads builds from the API into a directory. Files are named by content, so each build is
// downloaded once and a damaged download is never installed.
type Packages struct {
	base   string
	token  string
	dir    string
	client *http.Client
}

func NewPackages(cfg PackagesConfig) (*Packages, error) {
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("create package directory: %w", err)
	}
	return &Packages{base: httpBase(cfg.URL), token: cfg.Token, dir: cfg.Dir, client: &http.Client{}}, nil
}

// httpBase turns the API's base URL, which a hub may give as ws(s), into the http(s) one.
func httpBase(apiURL string) string {
	return strings.Replace(strings.Replace(apiURL, "wss://", "https://", 1), "ws://", "http://", 1)
}

// Fetch returns the path of the build's package, downloading it unless it is already on disk. The file
// extension follows the platform, as installers require.
func (p *Packages) Fetch(ctx context.Context, platform project.Platform, pkg run.Package) (string, error) {
	if !digestPattern.MatchString(pkg.SHA256) || pkg.Size <= 0 {
		return "", fault.WithDetail(fault.ErrInvalidInput, "a package needs a SHA-256 digest and a size")
	}
	extension := ".apk"
	if platform == project.PlatformIOS {
		extension = ".ipa"
	}
	path := filepath.Join(p.dir, pkg.SHA256+extension)
	if info, err := os.Stat(path); err == nil && info.Size() == pkg.Size {
		now := time.Now()
		return path, os.Chtimes(path, now, now)
	}
	if err := p.download(ctx, pkg, path); err != nil {
		return "", err
	}
	return path, p.evict()
}

func (p *Packages) download(ctx context.Context, pkg run.Package, path string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base+BuildsPath+url.PathEscape(pkg.BuildID), nil)
	if err != nil {
		return fmt.Errorf("build package request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+p.token)
	response, err := p.client.Do(request)
	if err != nil {
		return fmt.Errorf("download package: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download package: the API answered %s", response.Status)
	}
	temp, err := os.CreateTemp(p.dir, ".download-*")
	if err != nil {
		return fmt.Errorf("create package file: %w", err)
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	digest := sha256.New()
	written, err := io.Copy(io.MultiWriter(temp, digest), io.LimitReader(response.Body, pkg.Size+1))
	err = errors.Join(err, temp.Close())
	if err != nil {
		return fmt.Errorf("write package: %w", err)
	}
	if written != pkg.Size || hex.EncodeToString(digest.Sum(nil)) != pkg.SHA256 {
		return fmt.Errorf("download package: the file does not match build %s", pkg.BuildID)
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		return fmt.Errorf("keep package: %w", err)
	}
	return nil
}

// evict removes all but the most recently used packages.
func (p *Packages) evict() error {
	entries, err := os.ReadDir(p.dir)
	if err != nil {
		return fmt.Errorf("list packages: %w", err)
	}
	type kept struct {
		path string
		used time.Time
	}
	var packages []kept
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		packages = append(packages, kept{path: filepath.Join(p.dir, entry.Name()), used: info.ModTime()})
	}
	slices.SortFunc(packages, func(a, b kept) int { return cmp.Compare(b.used.UnixNano(), a.used.UnixNano()) })
	for _, old := range packages[min(len(packages), keptPackages):] {
		if err := os.Remove(old.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove package: %w", err)
		}
	}
	return nil
}
