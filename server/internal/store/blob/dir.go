// Package blob stores large binary objects, such as app packages, outside the database.
package blob

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/build"
)

const (
	dirPerm  = 0o750
	filePerm = 0o640
)

// keyPattern is slash-separated segments of safe characters, so a key can never leave the root.
var keyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+(/[A-Za-z0-9_-]+)*$`)

// Dir keeps objects as files under one directory. os.Root confines every operation to it.
type Dir struct {
	root *os.Root
}

func OpenDir(dir string) (*Dir, error) {
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return nil, fmt.Errorf("create blob directory: %w", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open blob directory: %w", err)
	}
	return &Dir{root: root}, nil
}

func (d *Dir) Close() error {
	return d.root.Close()
}

func validKey(key string) error {
	if !keyPattern.MatchString(key) {
		return fmt.Errorf("blob key %q: %w", key, fault.ErrInvalidInput)
	}
	return nil
}

// Put writes content to a temporary file and renames it into place, so readers never see a partial
// object. Cancelling ctx stops the copy.
func (d *Dir) Put(ctx context.Context, key string, content io.Reader) (int64, error) {
	if err := validKey(key); err != nil {
		return 0, err
	}
	if err := d.root.MkdirAll(path.Dir(key), dirPerm); err != nil {
		return 0, fmt.Errorf("create blob parent: %w", err)
	}
	temp := key + ".partial-" + rand.Text()
	file, err := d.root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, filePerm)
	if err != nil {
		return 0, fmt.Errorf("create blob: %w", err)
	}
	size, err := io.Copy(file, contextReader{ctx: ctx, r: content})
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = d.root.Rename(temp, key)
	}
	if err != nil {
		_ = d.root.Remove(temp)
		return 0, fmt.Errorf("write blob: %w", err)
	}
	return size, nil
}

func (d *Dir) Open(_ context.Context, key string) (build.File, error) {
	if err := validKey(key); err != nil {
		return nil, err
	}
	file, err := d.root.Open(key)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("blob %s: %w", key, fault.ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("open blob: %w", err)
	}
	return file, nil
}

func (d *Dir) Delete(_ context.Context, key string) error {
	if err := validKey(key); err != nil {
		return err
	}
	err := d.root.Remove(key)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("blob %s: %w", key, fault.ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("delete blob: %w", err)
	}
	return nil
}

// contextReader ends a long copy when its context is cancelled, such as when the uploading client
// disconnects or the server shuts down.
type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (c contextReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
