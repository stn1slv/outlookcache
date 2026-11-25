package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stn1slv/outlookcache/internal/hxtest"
)

func sampleStore(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, hxtest.WriteSample(dir, time.Now()))
	return filepath.Join(dir, "HxStore.hxd")
}

func runCLI(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	root := NewRootCommand("test", &out, &errOut)
	root.SetArgs(args)
	err = root.ExecuteContext(context.Background())
	return out.String(), errOut.String(), err
}

func TestListCommands(t *testing.T) {
	t.Parallel()
	store := sampleStore(t)
	tests := []struct {
		args      []string
		wantCount int
		wantFirst map[string]any
		summary   string
	}{
		{[]string{"accounts"}, 1, map[string]any{"id": "18750001", "address": "bob@example.com", "dir": "7"}, "# 1 result(s), 0 record(s) skipped, 0 damaged section(s)\n"},
		{[]string{"folders", "--account", "bob@example.com"}, 4, map[string]any{"id": "18750010", "name": "Inbox", "total": 2.0, "unread": 1.0}, ""},
		{[]string{"messages"}, 3, map[string]any{"id": "18800003", "folder_role": "sent", "body_kind": "none"}, "# 3 result(s), 0 record(s) skipped, 0 damaged section(s), 1 unconfirmed\n"},
		{[]string{"messages", "--folder", "inbox", "--unread"}, 1, map[string]any{"id": "18800001", "subject": "Quarterly numbers", "read": false, "importance": "high"}, ""},
		{[]string{"messages", "--confirmed-only"}, 2, nil, ""},
		{[]string{"messages", "--limit", "1"}, 1, nil, ""},
		{[]string{"search", "LUNCH"}, 1, map[string]any{"id": "18800002", "confirmed": false}, ""},
		{[]string{"search", "older text"}, 0, nil, ""},
		{[]string{"search", "older text", "--body"}, 1, nil, ""},
		{[]string{"thread", "18800101"}, 2, map[string]any{"id": "18800001"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.args[0], func(t *testing.T) {
			t.Parallel()
			stdout, stderr, err := runCLI(t, append(tt.args, "--store", store)...)
			require.NoError(t, err)
			var rows []map[string]any
			require.NoError(t, json.Unmarshal([]byte(stdout), &rows), stdout)
			require.Len(t, rows, tt.wantCount)
			for k, v := range tt.wantFirst {
				assert.Equal(t, v, rows[0][k], k)
			}
			if tt.summary != "" {
				assert.Equal(t, tt.summary, stderr)
			} else {
				assert.Contains(t, stderr, "record(s) skipped")
			}
		})
	}
}

func TestReadCommand(t *testing.T) {
	t.Parallel()
	store := sampleStore(t)
	stdout, stderr, err := runCLI(t, "read", "18800001", "--store", store)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(stdout), &m), stdout)
	assert.Equal(t, map[string]any{"kind": "inline", "source": "inline", "text": "The numbers are attached.", "quote_removed": true, "truncated": false}, m["body"])
	assert.Equal(t, "<18800001@example.com>", m["message_id"])
	assert.Contains(t, stdout, `"<18800001@example.com>"`, "JSON output does not escape < as \\u003c")
	assert.Equal(t, "# 1 result(s), 0 record(s) skipped, 0 damaged section(s), 0 unconfirmed\n", stderr)

	stdout, _, err = runCLI(t, "read", "18800001", "--quoted", "--max-content", "8", "--store", store)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(stdout), &m), stdout)
	body, ok := m["body"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "The numb", body["text"])
	assert.Equal(t, true, body["truncated"])
}

func TestErrors(t *testing.T) {
	t.Parallel()
	store := sampleStore(t)
	for name, args := range map[string][]string{
		"missing store":    {"accounts", "--store", filepath.Join(t.TempDir(), "HxStore.hxd")},
		"unknown message":  {"read", "18800099", "--store", store},
		"unknown folder":   {"messages", "--folder", "nope", "--store", store},
		"unknown account":  {"messages", "--account", "x@example.com", "--store", store},
		"negative limit":   {"messages", "--limit", "-1", "--store", store},
		"empty search":     {"search", " ", "--store", store},
		"missing argument": {"read", "--store", store},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			stdout, _, err := runCLI(t, args...)
			require.Error(t, err)
			assert.Empty(t, stdout)
		})
	}
}

func TestStoreFromEnvironment(t *testing.T) {
	store := sampleStore(t)
	t.Setenv(envStore, store)
	stdout, _, err := runCLI(t, "accounts")
	require.NoError(t, err)
	assert.Contains(t, stdout, `"18750001"`)
}
