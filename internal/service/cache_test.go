package service

import (
	"context"
	"os"
	"path/filepath"
	"sync"
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

func TestCacheLoadsOnceForConcurrentCalls(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, hxtest.WriteSample(dir, now))
	c := NewCache(filepath.Join(dir, "HxStore.hxd"), time.Minute, nil)

	const n = 8
	snaps := make([]*Snapshot, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() { snaps[i], errs[i] = c.Snapshot(context.Background()) })
	}
	wg.Wait()
	for i := range n {
		require.NoError(t, errs[i])
		assert.Same(t, snaps[0], snaps[i], "one load serves every waiting call")
	}
}

func TestCacheWaitEndsWithTheContext(t *testing.T) {
	t.Parallel()
	c := NewCache(filepath.Join(t.TempDir(), "HxStore.hxd"), time.Minute, nil)
	c.gate <- struct{}{} // a load is running
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := c.Snapshot(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
