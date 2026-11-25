// Package snapshot copies a live Outlook store into a private temp folder,
// so that readers never open the original file and never create files
// next to it.
package snapshot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

var (
	// ErrUnstable reports a source file that kept changing during every copy attempt.
	ErrUnstable = errors.New("source changed during copy")
	// ErrExists reports a second copy with the same base name in one snapshot.
	ErrExists = errors.New("snapshot already holds a file with this name")
)

// Attempts is how many times Copy tries when the source changes.
const Attempts = 3

// retryPause is the wait between attempts, so a short write burst can end.
const retryPause = 200 * time.Millisecond

// afterCopy runs after each copy attempt; tests use it to change the source.
var afterCopy = func(string) {}

// Snapshot is a private copy of one or more files. Close removes it.
type Snapshot struct {
	Dir string
}

// New creates an empty snapshot folder. os.MkdirTemp creates it with
// permissions 0700.
func New() (*Snapshot, error) {
	dir, err := os.MkdirTemp("", "outlookcache-")
	if err != nil {
		return nil, fmt.Errorf("create snapshot dir: %w", err)
	}
	return &Snapshot{Dir: dir}, nil
}

// Close removes the snapshot folder and everything in it.
func (s *Snapshot) Close() error {
	if s == nil || s.Dir == "" {
		return nil
	}
	return os.RemoveAll(s.Dir)
}

// Copy copies src into the snapshot folder under its base name and returns
// the path of the copy. It opens src read-only and compares size and mtime
// of the open file before and after the copy; when they change it tries
// again, up to Attempts times. Even a stable copy can mix blocks written at
// different moments, because Outlook rewrites the file in place: the
// per-block checksums catch a torn block, not a mix of valid blocks.
func (s *Snapshot) Copy(ctx context.Context, src string) (string, error) {
	dst := filepath.Join(s.Dir, filepath.Base(src))
	if _, err := os.Lstat(dst); err == nil {
		return "", fmt.Errorf("%w: %s", ErrExists, filepath.Base(src))
	}
	for attempt := range Attempts {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(retryPause):
			}
		}
		stable, err := copyFile(ctx, src, dst)
		if err != nil {
			_ = os.Remove(dst)
			return "", err
		}
		if stable {
			return dst, nil
		}
	}
	_ = os.Remove(dst)
	return "", fmt.Errorf("%w: %s", ErrUnstable, src)
}

func same(a, b os.FileInfo) bool {
	return a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

// copyFile copies src to dst and reports whether src stayed unchanged.
func copyFile(ctx context.Context, src, dst string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	in, err := os.Open(src)
	if err != nil {
		return false, fmt.Errorf("open source: %w", err)
	}
	defer func() { _ = in.Close() }() // read-only: a close error loses nothing
	before, err := in.Stat()
	if err != nil {
		return false, fmt.Errorf("stat source: %w", err)
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return false, fmt.Errorf("create copy: %w", err)
	}
	if _, err := io.Copy(out, ctxReader{ctx, in}); err != nil {
		_ = out.Close()
		return false, fmt.Errorf("copy %s: %w", filepath.Base(src), err)
	}
	if err := out.Close(); err != nil {
		return false, fmt.Errorf("close copy: %w", err)
	}
	afterCopy(src)
	after, err := in.Stat()
	if err != nil {
		return false, fmt.Errorf("stat source: %w", err)
	}
	return same(before, after), nil
}

// ctxReader stops a long copy when the context is cancelled.
type ctxReader struct {
	ctx context.Context //nolint:containedctx // scoped to a single io.Copy call
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
