package service

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stn1slv/outlookcache/internal/hxtest"
	"github.com/stn1slv/outlookcache/internal/store"
)

var (
	now     = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	options = Options{Now: func() time.Time { return now }}
	quiet   = slog.New(slog.NewTextHandler(io.Discard, nil))
)

func sample(t *testing.T) *Snapshot {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, hxtest.WriteSample(dir, now))
	s, err := Load(context.Background(), filepath.Join(dir, "HxStore.hxd"), quiet)
	require.NoError(t, err)
	return s
}

func ids(rows []MessageRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

const (
	m1 = "18800001"
	m2 = "18800002"
	m3 = "18800003"
)

func TestAccountsAndFolders(t *testing.T) {
	t.Parallel()
	s := sample(t)
	ctx := context.Background()

	accs, err := s.Accounts("BOB@example.com")
	require.NoError(t, err)
	require.Len(t, accs, 1)
	assert.Equal(t, "18750001", accs[0].ID)
	_, err = s.Accounts("nobody@example.com")
	require.ErrorIs(t, err, ErrNotFound)

	folders, err := s.Folders(ctx, "")
	require.NoError(t, err)
	byName := map[string]FolderRow{}
	for _, f := range folders {
		byName[f.Name+"/"+f.Role] = f
	}
	assert.Equal(t, 2, byName["Inbox/inbox"].Total)
	assert.Equal(t, 1, byName["Inbox/inbox"].Unread)
	assert.Equal(t, 1, byName["Sent Items/sent"].Total)
	assert.Equal(t, "18750010", byName["Projects/"].ParentID)
	assert.Contains(t, byName, "/deleted", "a role folder without a stored name")
}

func TestMessages(t *testing.T) {
	t.Parallel()
	s := sample(t)
	ctx := context.Background()
	tests := []struct {
		name string
		f    Filter
		want []string
	}{
		{"all, newest first", Filter{}, []string{m3, m1, m2}},
		{"folder by role", Filter{Folder: "INBOX"}, []string{m1, m2}},
		{"folder by name", Filter{Folder: "sent items"}, []string{m3}},
		{"folder by id", Filter{Folder: "18750020"}, []string{m3}},
		{"unread", Filter{Unread: true}, []string{m1}},
		{"days", Filter{Days: 1}, []string{m3, m1}},
		{"confirmed only", Filter{ConfirmedOnly: true}, []string{m3, m1}},
		{"limit", Filter{Limit: 1}, []string{m3}},
		{"account by address", Filter{Account: "bob@example.com", Folder: "inbox"}, []string{m1, m2}},
		{"empty folder", Filter{Folder: "projects"}, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rows, err := s.Messages(ctx, tt.f, options)
			require.NoError(t, err)
			assert.Equal(t, tt.want, ids(rows))
		})
	}
	_, err := s.Messages(ctx, Filter{Folder: "nope"}, options)
	require.ErrorIs(t, err, ErrNotFound)
	_, err = s.Messages(ctx, Filter{Account: "18750099"}, options)
	require.ErrorIs(t, err, ErrNotFound)

	rows, err := s.Messages(ctx, Filter{Folder: "inbox", Limit: 1}, options)
	require.NoError(t, err)
	assert.Equal(t, "Inbox", rows[0].FolderName)
	assert.Equal(t, "inbox", rows[0].FolderRole)
	assert.False(t, rows[0].Read)
}

func TestSearch(t *testing.T) {
	t.Parallel()
	s := sample(t)
	ctx := context.Background()
	for query, want := range map[string][]string{
		"QUARTERLY":         {m3, m1},
		"carol@example.com": {m2},
		"report.pdf":        {m1},
		"noon":              {m2}, // in the preview
		"friday":            {m2},
		"older text":        {},
	} {
		rows, err := s.Search(ctx, query, false, Filter{}, options)
		require.NoError(t, err, query)
		assert.Equal(t, want, ids(rows), query)
	}
	rows, err := s.Search(ctx, "older text", true, Filter{}, options)
	require.NoError(t, err)
	assert.Equal(t, []string{m1}, ids(rows), "body search includes quoted text")
	_, err = s.Search(ctx, "  ", false, Filter{}, options)
	require.ErrorIs(t, err, ErrInvalidArgument)
}

func TestMessageBodies(t *testing.T) {
	t.Parallel()
	s := sample(t)
	ctx := context.Background()

	d, err := s.Message(ctx, m1, false, options)
	require.NoError(t, err)
	assert.Equal(t, Body{Kind: store.BodyInline, Source: "inline", Text: "The numbers are attached.", QuoteRemoved: true}, d.Body)
	assert.Equal(t, "Quarterly numbers", d.Subject)

	d, err = s.Message(ctx, m1, true, options)
	require.NoError(t, err)
	assert.Contains(t, d.Body.Text, "older text")
	assert.False(t, d.Body.QuoteRemoved)

	d, err = s.Message(ctx, m2, false, options)
	require.NoError(t, err)
	assert.Equal(t, Body{Kind: store.BodyExternal, Source: "efmdata:42.dat", Text: "Lunch at noon on Friday?"}, d.Body)

	d, err = s.Message(ctx, m3, false, options)
	require.NoError(t, err)
	assert.Equal(t, Body{Kind: store.BodyNone}, d.Body)
	assert.Equal(t, "Thanks, got it.", d.Preview)

	d, err = s.Message(ctx, m2, false, Options{MaxContent: 5})
	require.NoError(t, err)
	assert.Equal(t, "Lunch", d.Body.Text)
	assert.True(t, d.Body.Truncated)

	_, err = s.Message(ctx, "18800099", false, options)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestMissingEFMDataFileIsReported(t *testing.T) {
	t.Parallel()
	s := sample(t)
	s.Profile = t.TempDir() // no Files/S0 here
	d, err := s.Message(context.Background(), m2, false, options)
	require.NoError(t, err)
	assert.Equal(t, store.BodyExternal, d.Body.Kind)
	assert.Empty(t, d.Body.Text)
	assert.Contains(t, d.Body.Error, "no readable EFMData file")
}

func TestThread(t *testing.T) {
	t.Parallel()
	s := sample(t)
	ctx := context.Background()
	th, err := s.Thread(ctx, "18800101", false, 0, options)
	require.NoError(t, err)
	require.Len(t, th, 2)
	assert.Equal(t, m1, th[0].ID, "oldest first")
	assert.Equal(t, m3, th[1].ID)
	assert.Equal(t, "The numbers are attached.", th[0].Body.Text)

	th, err = s.Thread(ctx, "18800101", false, 1, options)
	require.NoError(t, err)
	require.Len(t, th, 1)
	assert.Equal(t, m3, th[0].ID, "the limit keeps the newest messages")

	_, err = s.Thread(ctx, "18800199", false, 0, options)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestLoadErrors(t *testing.T) {
	t.Parallel()
	_, err := Load(context.Background(), filepath.Join(t.TempDir(), "HxStore.hxd"), quiet)
	require.ErrorIs(t, err, ErrStoreMissing)

	dir := t.TempDir()
	require.NoError(t, hxtest.WriteSample(dir, now))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = Load(ctx, filepath.Join(dir, "HxStore.hxd"), quiet)
	require.ErrorIs(t, err, context.Canceled)
}

func TestCanceledQueries(t *testing.T) {
	t.Parallel()
	s := sample(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.Messages(ctx, Filter{}, options)
	require.ErrorIs(t, err, context.Canceled)
	_, err = s.Folders(ctx, "")
	require.ErrorIs(t, err, context.Canceled)
	_, err = s.Message(ctx, m1, false, options)
	require.ErrorIs(t, err, context.Canceled)
}
