package snapshot_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stn1slv/outlookcache/internal/snapshot"
)

func writeSource(t *testing.T) (string, []byte, time.Time) {
	t.Helper()
	src := filepath.Join(t.TempDir(), "HxStore.hxd")
	data := bytes.Repeat([]byte("synthetic store "), 4096)
	require.NoError(t, os.WriteFile(src, data, 0o600))
	old := time.Date(2026, 3, 29, 1, 30, 0, 0, time.UTC)
	require.NoError(t, os.Chtimes(src, old, old))
	return src, data, old
}

func TestCopyLeavesSourceUntouched(t *testing.T) {
	t.Parallel()
	src, data, mtime := writeSource(t)
	entries, err := os.ReadDir(filepath.Dir(src))
	require.NoError(t, err)

	s, err := snapshot.New()
	require.NoError(t, err)
	info, err := os.Stat(s.Dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())

	dst, err := s.Copy(t.Context(), src)
	require.NoError(t, err)
	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, data, got)

	after, err := os.ReadFile(src)
	require.NoError(t, err)
	assert.Equal(t, data, after)
	st, err := os.Stat(src)
	require.NoError(t, err)
	assert.True(t, st.ModTime().Equal(mtime))
	entriesAfter, err := os.ReadDir(filepath.Dir(src))
	require.NoError(t, err)
	assert.Len(t, entriesAfter, len(entries), "no files created next to the source")

	require.NoError(t, s.Close())
	assert.NoDirExists(t, s.Dir)
}

func TestCopyCancelled(t *testing.T) {
	t.Parallel()
	src, _, _ := writeSource(t)
	s, err := snapshot.New()
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = s.Copy(ctx, src)
	require.ErrorIs(t, err, context.Canceled)
	require.NoError(t, s.Close())
	assert.NoDirExists(t, s.Dir)
}
