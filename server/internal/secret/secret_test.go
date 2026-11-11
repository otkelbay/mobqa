package secret_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/mobqa/backend/internal/secret"
)

func newBox(t *testing.T) *secret.Box {
	t.Helper()
	box, err := secret.NewBox(bytes.Repeat([]byte{7}, secret.KeySize))
	if err != nil {
		t.Fatalf("NewBox: %v", err)
	}
	return box
}

func TestSealOpenRoundTrip(t *testing.T) {
	box := newBox(t)

	sealed := box.Seal("hunter2", []byte("env-1/PASSWORD"))
	opened, err := box.Open(sealed, []byte("env-1/PASSWORD"))

	if err != nil || opened != "hunter2" {
		t.Fatalf("Open = %q, %v", opened, err)
	}
	if again := box.Seal("hunter2", []byte("env-1/PASSWORD")); again == sealed {
		t.Fatal("sealing twice produced the same ciphertext")
	}
}

func TestOpenRejectsMovedCiphertext(t *testing.T) {
	box := newBox(t)
	sealed := box.Seal("hunter2", []byte("env-1/PASSWORD"))

	if _, err := box.Open(sealed, []byte("env-2/PASSWORD")); err == nil {
		t.Fatal("opened a value sealed for another place")
	}
}

func TestOpenRejectsMalformedValues(t *testing.T) {
	box := newBox(t)
	for _, sealed := range []string{"plaintext", "v1:***", "v1:AAAA"} {
		if _, err := box.Open(sealed, nil); !errors.Is(err, secret.ErrMalformed) {
			t.Fatalf("Open(%q) = %v, want ErrMalformed", sealed, err)
		}
	}
}

func TestNewBoxRejectsShortKey(t *testing.T) {
	if _, err := secret.NewBox([]byte("short")); err == nil {
		t.Fatal("accepted a short key")
	}
}
