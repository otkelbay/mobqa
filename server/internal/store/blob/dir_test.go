package blob_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/store/blob"
)

func openDir(t *testing.T) (*blob.Dir, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "blobs")
	dir, err := blob.OpenDir(path)
	if err != nil {
		t.Fatalf("OpenDir: %v", err)
	}
	t.Cleanup(func() { _ = dir.Close() })
	return dir, path
}

func TestDirRoundTrip(t *testing.T) {
	dir, _ := openDir(t)
	ctx := context.Background()

	size, err := dir.Put(ctx, "builds/p1/b1", strings.NewReader("package bytes"))
	if err != nil || size != 13 {
		t.Fatalf("Put = %d, %v", size, err)
	}
	file, err := dir.Open(ctx, "builds/p1/b1")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	tail := make([]byte, 5)
	if _, err := file.ReadAt(tail, 8); err != nil || string(tail) != "bytes" {
		t.Fatalf("ReadAt = %q, %v", tail, err)
	}
	_ = file.Close()

	if err := dir.Delete(ctx, "builds/p1/b1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := dir.Open(ctx, "builds/p1/b1"); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("Open after delete = %v", err)
	}
	if err := dir.Delete(ctx, "builds/p1/b1"); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("second Delete = %v", err)
	}
}

func TestDirRejectsKeysOutsideRoot(t *testing.T) {
	dir, _ := openDir(t)

	for _, key := range []string{"../escape", "/abs", "a/../../b", "a//b", "", "a/b.partial-x"} {
		if _, err := dir.Put(context.Background(), key, strings.NewReader("x")); !errors.Is(err, fault.ErrInvalidInput) {
			t.Fatalf("Put(%q) = %v, want ErrInvalidInput", key, err)
		}
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestDirLeavesNothingAfterFailedPut(t *testing.T) {
	dir, path := openDir(t)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := dir.Put(context.Background(), "builds/p1/b1", failingReader{}); err == nil {
		t.Fatal("Put with failing reader succeeded")
	}
	if _, err := dir.Put(cancelled, "builds/p1/b2", strings.NewReader("x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Put with cancelled context = %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(path, "builds", "p1"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("left behind %v (%v)", entries, err)
	}
}
