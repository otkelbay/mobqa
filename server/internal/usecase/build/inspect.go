package build

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/project"
)

// maxProfileSize bounds the embedded provisioning profile read into memory; real ones are a few KB.
const maxProfileSize = 1 << 20

var errNoPlist = errors.New("provisioning profile has no plist payload")

// inspect checks that the package matches its platform and returns the iOS signing profile, if any.
func inspect(archive *zip.Reader, platform project.Platform) (*Provisioning, error) {
	if platform == project.PlatformAndroid {
		if !hasEntry(archive, func(name string) bool { return name == "AndroidManifest.xml" }) {
			return nil, fault.WithDetail(fault.ErrInvalidInput, "the file is not an Android APK")
		}
		return nil, nil
	}
	if !hasEntry(archive, isAppBundleEntry) {
		return nil, fault.WithDetail(fault.ErrInvalidInput, "the file is not an iOS IPA")
	}
	for _, file := range archive.File {
		if isEmbeddedProfile(file.Name) {
			return readProfile(file)
		}
	}
	return nil, nil
}

func hasEntry(archive *zip.Reader, match func(name string) bool) bool {
	for _, file := range archive.File {
		if match(file.Name) {
			return true
		}
	}
	return false
}

func isAppBundleEntry(name string) bool {
	parts := strings.SplitN(name, "/", 3)
	return len(parts) >= 2 && parts[0] == "Payload" && strings.HasSuffix(parts[1], ".app")
}

func isEmbeddedProfile(name string) bool {
	dir, file := path.Split(name)
	return file == "embedded.mobileprovision" && isAppBundleEntry(dir) && strings.Count(dir, "/") == 2
}

func readProfile(file *zip.File) (*Provisioning, error) {
	invalid := fault.WithDetail(fault.ErrInvalidInput, "the IPA's provisioning profile cannot be read")
	if file.UncompressedSize64 > maxProfileSize {
		return nil, invalid
	}
	src, err := file.Open()
	if err != nil {
		return nil, invalid
	}
	defer func() { _ = src.Close() }()
	data, err := io.ReadAll(io.LimitReader(src, maxProfileSize))
	if err != nil {
		return nil, invalid
	}
	profile, err := parseProfile(data)
	if err != nil {
		return nil, invalid
	}
	return &profile, nil
}

// parseProfile reads a provisioning profile: a CMS envelope around an XML property list. The signature
// is not verified; the device that installs the build is the authority on that.
func parseProfile(data []byte) (Provisioning, error) {
	start, end := bytes.Index(data, []byte("<plist")), bytes.LastIndex(data, []byte("</plist>"))
	if start < 0 || end < start {
		return Provisioning{}, errNoPlist
	}
	decoder := xml.NewDecoder(bytes.NewReader(data[start : end+len("</plist>")]))
	root, err := decodePlist(decoder)
	if err != nil {
		return Provisioning{}, err
	}
	dict, _ := root.(map[string]any)
	expiry, _ := dict["ExpirationDate"].(string)
	expiresAt, err := time.Parse(time.RFC3339, expiry)
	if err != nil {
		return Provisioning{}, fmt.Errorf("provisioning expiration %q: %w", expiry, err)
	}
	profile := Provisioning{ExpiresAt: expiresAt.UTC()}
	profile.ProfileName, _ = dict["Name"].(string)
	if teams, ok := dict["TeamIdentifier"].([]any); ok && len(teams) > 0 {
		profile.TeamID, _ = teams[0].(string)
	}
	if entitlements, ok := dict["Entitlements"].(map[string]any); ok {
		appID, _ := entitlements["application-identifier"].(string)
		profile.BundleID = strings.TrimPrefix(appID, profile.TeamID+".")
	}
	return profile, nil
}

// decodePlist returns the first value inside <plist>: dicts become maps, arrays slices, booleans bools
// and every scalar its text.
func decodePlist(decoder *xml.Decoder) (any, error) {
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("read plist: %w", err)
		}
		if start, ok := token.(xml.StartElement); ok && start.Name.Local != "plist" {
			return decodeValue(decoder, start)
		}
	}
}

func decodeValue(decoder *xml.Decoder, start xml.StartElement) (any, error) {
	switch start.Name.Local {
	case "dict":
		return decodeDict(decoder)
	case "array":
		return decodeArray(decoder)
	case "true", "false":
		return start.Name.Local == "true", decoder.Skip()
	default:
		var text string
		if err := decoder.DecodeElement(&text, &start); err != nil {
			return nil, fmt.Errorf("read plist %s: %w", start.Name.Local, err)
		}
		return strings.TrimSpace(text), nil
	}
}

func decodeDict(decoder *xml.Decoder) (map[string]any, error) {
	dict := map[string]any{}
	key := ""
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("read plist dict: %w", err)
		}
		switch t := token.(type) {
		case xml.EndElement:
			return dict, nil
		case xml.StartElement:
			value, err := decodeValue(decoder, t)
			if err != nil {
				return nil, err
			}
			if t.Name.Local == "key" {
				key, _ = value.(string)
				continue
			}
			dict[key] = value
		}
	}
}

func decodeArray(decoder *xml.Decoder) ([]any, error) {
	var items []any
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("read plist array: %w", err)
		}
		switch t := token.(type) {
		case xml.EndElement:
			return items, nil
		case xml.StartElement:
			value, err := decodeValue(decoder, t)
			if err != nil {
				return nil, err
			}
			items = append(items, value)
		}
	}
}
