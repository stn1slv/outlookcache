package hxtest_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stn1slv/outlookcache/internal/body"
	"github.com/stn1slv/outlookcache/internal/hxtest"
	"github.com/stn1slv/outlookcache/internal/store"
)

func TestWriteSampleLoads(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, hxtest.WriteSample(dir, time.Now()))
	data, err := os.ReadFile(filepath.Join(dir, "HxStore.hxd"))
	require.NoError(t, err)
	s, err := store.Load(data, "HxStore.hxd")
	require.NoError(t, err)

	require.Len(t, s.Accounts, 1)
	assert.Equal(t, hxtest.SampleDir, s.Accounts[0].Dir)
	assert.Equal(t, hxtest.SampleAddress, s.Accounts[0].Address)
	assert.Len(t, s.Folders, 4)
	require.Len(t, s.Messages, 3)
	kinds := map[string]string{}
	for _, m := range s.Messages {
		kinds[m.Subject+"/"+m.FolderID] = m.BodyKind
	}
	assert.Equal(t, map[string]string{
		"Quarterly numbers/18750010": store.BodyInline,
		"Lunch plan/18750010":        store.BodyExternal,
		"Quarterly numbers/18750020": store.BodyNone,
	}, kinds)

	ref, err := s.Body("18800002")
	require.NoError(t, err)
	html, src, err := body.HTML(ref, dir, 0)
	require.NoError(t, err)
	assert.Equal(t, hxtest.SampleBody2, string(html))
	assert.Equal(t, "efmdata:"+hxtest.SampleEFMFile, src)
}
