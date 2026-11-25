package snapshot

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This test changes the package-level hook, so it must not run in parallel.
func TestCopyUnstableSource(t *testing.T) { //nolint:paralleltest // mutates afterCopy
	src := filepath.Join(t.TempDir(), "HxStore.hxd")
	require.NoError(t, os.WriteFile(src, []byte("synthetic"), 0o600))
	calls := 0
	afterCopy = func(p string) {
		calls++
		next := time.Now().Add(time.Duration(calls) * time.Second)
		_ = os.Chtimes(p, next, next) // the source "changes" after every copy
	}
	t.Cleanup(func() { afterCopy = func(string) {} })

	s, err := New()
	require.NoError(t, err)
	defer func() { _ = s.Close() }()
	_, err = s.Copy(t.Context(), src)
	require.ErrorIs(t, err, ErrUnstable)
	assert.Equal(t, Attempts, calls)
	assert.NoFileExists(t, filepath.Join(s.Dir, "HxStore.hxd"), "a failed copy leaves no file")
}

func TestCopyRefusesSameName(t *testing.T) {
	t.Parallel()
	a := filepath.Join(t.TempDir(), "1.dat")
	b := filepath.Join(t.TempDir(), "1.dat")
	require.NoError(t, os.WriteFile(a, []byte("a"), 0o600))
	require.NoError(t, os.WriteFile(b, []byte("b"), 0o600))
	s, err := New()
	require.NoError(t, err)
	defer func() { _ = s.Close() }()
	_, err = s.Copy(t.Context(), a)
	require.NoError(t, err)
	_, err = s.Copy(t.Context(), b)
	require.ErrorIs(t, err, ErrExists)
}
