// Package fault holds the error kinds every layer agrees on. Usecases wrap them with context;
// transport maps each kind to one status and, unless the error carries a detail, a fixed message.
package fault

import "errors"

var (
	ErrInvalidInput = errors.New("invalid input")
	ErrNotFound     = errors.New("not found")
	ErrConflict     = errors.New("conflict")
	ErrUnauthorized = errors.New("unauthorized")
	ErrForbidden    = errors.New("forbidden")
	ErrUnavailable  = errors.New("unavailable")
)

type detailed struct {
	kind   error
	detail string
}

func (d detailed) Error() string { return d.detail }
func (d detailed) Unwrap() error { return d.kind }

// WithDetail attaches a message that is safe to show the client. The detail is always a constant
// written by us: it must never contain user input or internal state.
func WithDetail(kind error, detail string) error {
	return detailed{kind: kind, detail: detail}
}

// Detail returns the client-safe message carried anywhere in err's chain.
func Detail(err error) (string, bool) {
	var d detailed
	if errors.As(err, &d) {
		return d.detail, true
	}
	return "", false
}
