// Package project manages the apps under test: their settings, login recipe and members, and decides
// who may act on a project.
package project

import (
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/mobqa/backend/internal/fault"
)

type Platform string

const (
	PlatformAndroid Platform = "android"
	PlatformIOS     Platform = "ios"
)

const (
	maxNameLength        = 80
	maxDescriptionLength = 2000
	// maxPackageNameLength is Android's limit for an application id; iOS bundle ids are shorter.
	maxPackageNameLength = 255

	defaultColor     = "#EFF4FF"
	defaultTextColor = "#1D4ED8"
)

var (
	// packageNamePattern accepts reverse-DNS identifiers valid as both Android application ids and
	// iOS bundle ids.
	packageNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*(\.[A-Za-z0-9_-]+)+$`)
	colorPattern       = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
)

type Appearance struct {
	Initial   string
	Color     string
	TextColor string
}

type Project struct {
	ID          string
	Name        string
	Description string
	Platforms   []Platform
	PackageName string
	Appearance  Appearance
	AuthProfile AuthProfile
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type Member struct {
	UserID    string
	Email     string
	Name      string
	AvatarURL string
	Role      Role
	JoinedAt  time.Time
}

// Detail is a project as its members see it.
type Detail struct {
	Project Project
	Role    Role
	Members []Member
}

func validateName(name string) error {
	if strings.TrimSpace(name) != name || name == "" || utf8.RuneCountInString(name) > maxNameLength {
		return fault.WithDetail(fault.ErrInvalidInput, "name must be 1 to 80 characters without surrounding spaces")
	}
	return nil
}

func validateDescription(description string) error {
	if utf8.RuneCountInString(description) > maxDescriptionLength {
		return fault.WithDetail(fault.ErrInvalidInput, "description must be at most 2000 characters")
	}
	return nil
}

func validatePlatforms(platforms []Platform) error {
	if len(platforms) == 0 {
		return fault.WithDetail(fault.ErrInvalidInput, "choose at least one platform")
	}
	for i, platform := range platforms {
		if platform != PlatformAndroid && platform != PlatformIOS {
			return fault.WithDetail(fault.ErrInvalidInput, "platform must be android or ios")
		}
		if slices.Contains(platforms[:i], platform) {
			return fault.WithDetail(fault.ErrInvalidInput, "each platform may be listed once")
		}
	}
	return nil
}

func validatePackageName(name string) error {
	if name != "" && (len(name) > maxPackageNameLength || !packageNamePattern.MatchString(name)) {
		return fault.WithDetail(fault.ErrInvalidInput, "package name must be a reverse-DNS id such as com.example.app")
	}
	return nil
}

func (a Appearance) Validate() error {
	if count := utf8.RuneCountInString(a.Initial); count < 1 || count > 2 || strings.TrimSpace(a.Initial) != a.Initial {
		return fault.WithDetail(fault.ErrInvalidInput, "initial must be one or two characters")
	}
	if !colorPattern.MatchString(a.Color) || !colorPattern.MatchString(a.TextColor) {
		return fault.WithDetail(fault.ErrInvalidInput, "colors must be hex values such as #1D4ED8")
	}
	return nil
}

func defaultAppearance(name string) Appearance {
	first, _ := utf8.DecodeRuneInString(name)
	return Appearance{Initial: string(unicode.ToUpper(first)), Color: defaultColor, TextColor: defaultTextColor}
}
