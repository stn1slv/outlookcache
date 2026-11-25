package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stn1slv/outlookcache/internal/hxtest"
)

func TestCacheReloads(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, hxtest.WriteSample(dir, now))
	path := filepath.Join(dir, "HxStore.hxd")
	c := NewCache(path, time.Minute, nil)
	clock := now
	c.now = func() time.Time { return clock }
	ctx := context.Background()

	a, err := c.Snapshot(ctx)
	require.NoError(t, err)
	b, err := c.Snapshot(ctx)
	require.NoError(t, err)
	assert.Same(t, a, b, "reused within the TTL")

	clock = clock.Add(2 * time.Minute)
	b, err = c.Snapshot(ctx)
	require.NoError(t, err)
	assert.NotSame(t, a, b, "loaded again after the TTL")

	require.NoError(t, os.Chtimes(path, now, now.Add(time.Hour)))
	d, err := c.Snapshot(ctx)
	require.NoError(t, err)
	assert.NotSame(t, b, d, "loaded again when the file changed")

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = c.Snapshot(canceled)
	require.ErrorIs(t, err, context.Canceled)

	_, err = NewCache(filepath.Join(dir, "missing.hxd"), time.Minute, nil).Snapshot(ctx)
	require.ErrorIs(t, err, ErrStoreMissing)
}
