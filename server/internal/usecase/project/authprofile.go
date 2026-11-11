package project

import (
	"regexp"
	"unicode/utf8"

	"github.com/mobqa/backend/internal/fault"
)

// AuthMethod is how a test signs in to the app under test.
type AuthMethod string

const (
	AuthNone      AuthMethod = "none"
	AuthPassword  AuthMethod = "password"
	AuthPhoneOTP  AuthMethod = "phone_otp"
	AuthEmailOTP  AuthMethod = "email_otp"
	AuthGoogleSSO AuthMethod = "google_sso"
	AuthAppleSSO  AuthMethod = "apple_sso"
)

type SSOProvider string

const (
	SSOGoogle   SSOProvider = "google"
	SSOApple    SSOProvider = "apple"
	SSOFacebook SSOProvider = "facebook"
	SSOCustom   SSOProvider = "custom"
)

const maxNotesLength = 2000

// variableNamePattern is the shape of an environment variable name the runtime can export.
var variableNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// AuthProfile is the project's login recipe, which the authoring agent follows. It names environment
// variables instead of holding credentials, which live only in encrypted environments.
type AuthProfile struct {
	Method      AuthMethod
	LoginVar    string
	PasswordVar string
	OTP         *OTPSource
	SSO         *SSO
	Notes       string
}

// OTPSource says where one-time codes come from: a fixed code that test builds accept, or the API action
// whose extraction reads the code the app's backend sent.
type OTPSource struct {
	Override string
	ActionID string
}

// SSO is passed through the provider's native account picker with an account already on the device;
// web login pages are never automated.
type SSO struct {
	Provider   SSOProvider
	AccountVar string
}

func (p AuthProfile) Validate() error {
	switch p.Method {
	case AuthNone, AuthPassword, AuthPhoneOTP, AuthEmailOTP, AuthGoogleSSO, AuthAppleSSO:
	default:
		return fault.WithDetail(fault.ErrInvalidInput, "unknown sign-in method")
	}
	for _, name := range []string{p.LoginVar, p.PasswordVar} {
		if err := validateVariableReference(name); err != nil {
			return err
		}
	}
	if utf8.RuneCountInString(p.Notes) > maxNotesLength {
		return fault.WithDetail(fault.ErrInvalidInput, "notes must be at most 2000 characters")
	}
	if p.SSO != nil {
		return p.SSO.validate()
	}
	return nil
}

func (s SSO) validate() error {
	switch s.Provider {
	case SSOGoogle, SSOApple, SSOFacebook, SSOCustom:
	default:
		return fault.WithDetail(fault.ErrInvalidInput, "unknown sso provider")
	}
	return validateVariableReference(s.AccountVar)
}

func validateVariableReference(name string) error {
	if name != "" && !variableNamePattern.MatchString(name) {
		return fault.WithDetail(fault.ErrInvalidInput, "variable names must be letters, digits and underscores")
	}
	return nil
}

// ValidVariableName reports whether name can be exported as an environment variable.
func ValidVariableName(name string) bool {
	return variableNamePattern.MatchString(name)
}
