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
)

// ErrUnstable reports a source file that kept changing during every copy attempt.
var ErrUnstable = errors.New("source changed during copy")

// Attempts is how many times Copy retries when the source changes.
const Attempts = 3

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
// the path of the copy. It opens src read-only. When the size or mtime of
// src changes while copying, it tries again, up to Attempts times. The copy
// can still hold a block that Outlook rewrote in place without changing the
// mtime; callers detect that through the per-block checksums.
func (s *Snapshot) Copy(ctx context.Context, src string) (string, error) {
	dst := filepath.Join(s.Dir, filepath.Base(src))
	for range Attempts {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		before, err := os.Stat(src)
		if err != nil {
			return "", fmt.Errorf("stat source: %w", err)
		}
		if err := copyFile(ctx, src, dst); err != nil {
			return "", err
		}
		after, err := os.Stat(src)
		if err != nil {
			return "", fmt.Errorf("stat source: %w", err)
		}
		if same(before, after) {
			return dst, nil
		}
	}
	return "", fmt.Errorf("%w: %s", ErrUnstable, src)
}

func same(a, b os.FileInfo) bool {
	return a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

func copyFile(ctx context.Context, src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer func() { _ = in.Close() }() // read-only: a close error loses nothing
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create copy: %w", err)
	}
	if _, err := io.Copy(out, ctxReader{ctx, in}); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy %s: %w", filepath.Base(src), err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close copy: %w", err)
	}
	return nil
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
