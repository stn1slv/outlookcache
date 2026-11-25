package service

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"
)

// Cache keeps a decoded snapshot in memory for a long-running server. It
// loads the store again when the TTL has passed or when the size or
// modification time of HxStore.hxd changed. Only one load runs at a time.
type Cache struct {
	path   string
	ttl    time.Duration
	logger *slog.Logger

	gate   chan struct{}
	snap   *Snapshot
	sig    string
	loaded time.Time
	now    func() time.Time
}

// NewCache returns a cache for the store at path ("" for the default
// profile).
func NewCache(path string, ttl time.Duration, logger *slog.Logger) *Cache {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Cache{path: path, ttl: ttl, logger: logger, gate: make(chan struct{}, 1), now: time.Now}
}

// Snapshot returns the decoded store, loading it when needed.
func (c *Cache) Snapshot(ctx context.Context) (*Snapshot, error) {
	select {
	case c.gate <- struct{}{}:
		defer func() { <-c.gate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := c.path
	if path == "" {
		p, err := DefaultStorePath()
		if err != nil {
			return nil, err
		}
		path = p
	}
	sig := signature(path)
	if c.snap != nil && sig != "" && sig == c.sig && c.now().Sub(c.loaded) < c.ttl {
		return c.snap, nil
	}
	snap, err := Load(ctx, path, c.logger)
	if err != nil {
		return nil, err
	}
	c.snap, c.sig, c.loaded = snap, sig, c.now()
	return snap, nil
}

// signature is the size and modification time of the store file; "" when
// it cannot be read.
func signature(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d:%d", st.ModTime().UnixNano(), st.Size())
}
