// Package build manages uploaded app packages (APK and IPA): it stores them, checks what they are,
// reads iOS signing details, picks the current build per platform and describes what changed between
// consecutive builds.
package build

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/project"
)

type Status string

const (
	// StatusProcessing means the build is usable but its change analysis has not finished.
	StatusProcessing Status = "processing"
	StatusReady      Status = "ready"
)

const (
	maxVersionLength  = 64
	maxFileNameLength = 120
)

// Provisioning is the non-secret part of the profile an iOS build is signed with.
type Provisioning struct {
	ProfileName string
	TeamID      string
	BundleID    string
	ExpiresAt   time.Time
}

func (p *Provisioning) expired(now time.Time) bool {
	return p != nil && !p.ExpiresAt.After(now)
}

// Analysis compares a build with the one uploaded before it on the same platform. An empty PreviousID
// marks the project's first build for that platform.
type Analysis struct {
	PreviousID string
	Changes    []string
}

type Build struct {
	ID          string
	ProjectID   string
	Platform    project.Platform
	Version     string
	BuildNumber string
	FileName    string
	Size        int64
	SHA256      string
	// BlobKey locates the package in blob storage; it never leaves the backend.
	BlobKey      string
	UploadedBy   string
	Status       Status
	Analysis     *Analysis
	IsCurrent    bool
	Provisioning *Provisioning
	UploadedAt   time.Time
}

func validateVersion(version, buildNumber string) error {
	for _, value := range []string{version, buildNumber} {
		if strings.TrimSpace(value) != value || utf8.RuneCountInString(value) > maxVersionLength {
			return fault.WithDetail(fault.ErrInvalidInput, "version and build number are at most 64 characters")
		}
	}
	if version == "" {
		return fault.WithDetail(fault.ErrInvalidInput, "version is required")
	}
	return nil
}

func validatePlatform(platform project.Platform) error {
	if platform != project.PlatformAndroid && platform != project.PlatformIOS {
		return fault.WithDetail(fault.ErrInvalidInput, "platform must be android or ios")
	}
	return nil
}

// downloadName turns an uploaded file name into one safe for a Content-Disposition header, falling back
// to the version when nothing usable remains.
func downloadName(fileName, version string, platform project.Platform) string {
	name := sanitize(fileName)
	if name == "" {
		name = sanitize(version)
	}
	if name == "" {
		name = "build"
	}
	if extension := packageExtension(platform); !strings.HasSuffix(strings.ToLower(name), extension) {
		name += extension
	}
	return name
}

func sanitize(name string) string {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, name)
	cleaned = strings.Trim(cleaned, ".-")
	if len(cleaned) > maxFileNameLength {
		cleaned = cleaned[len(cleaned)-maxFileNameLength:]
	}
	return cleaned
}

func packageExtension(platform project.Platform) string {
	if platform == project.PlatformIOS {
		return ".ipa"
	}
	return ".apk"
}
