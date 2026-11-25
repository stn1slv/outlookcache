package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stn1slv/outlookcache/internal/hxtest"
	"github.com/stn1slv/outlookcache/internal/service"
)

func sampleStore(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, hxtest.WriteSample(dir, time.Now()))
	return filepath.Join(dir, "HxStore.hxd")
}

func runCLI(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	return runCLIWithInput(t, "", args...)
}

func runCLIWithInput(t *testing.T, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	root := NewRootCommand("test", &out, &errOut)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	err = root.ExecuteContext(context.Background())
	return out.String(), errOut.String(), err
}

const stats = "0 record(s) skipped, 0 damaged section(s), 0 undecoded block(s)"

func TestListCommands(t *testing.T) {
	t.Parallel()
	store := sampleStore(t)
	tests := []struct {
		args      []string
		wantCount int
		wantFirst map[string]any
		summary   string
	}{
		{[]string{"accounts"}, 1, map[string]any{"id": "18750001", "address": "bob@example.com", "dir": "7"}, "# 1 result(s), " + stats},
		{[]string{"folders", "--account", "bob@example.com"}, 4, map[string]any{"id": "18750010", "name": "Inbox", "total": 2.0, "unread": 1.0, "unconfirmed": 1.0}, "# 4 result(s), " + stats + ", 1 unconfirmed"},
		{[]string{"messages"}, 3, map[string]any{"id": "18800003", "folder_role": "sent", "body_kind": "none"}, "# 3 result(s), " + stats + ", 1 unconfirmed"},
		{[]string{"messages", "--folder", "inbox", "--unread"}, 1, map[string]any{"id": "18800001", "subject": "Quarterly numbers", "read": false, "importance": "high"}, ""},
		{[]string{"messages", "--confirmed-only"}, 2, nil, ""},
		{[]string{"messages", "--days", "1"}, 2, nil, ""},
		{[]string{"messages", "--limit", "1"}, 1, nil, "# 1 result(s) (limit reached, more exist), " + stats + ", 0 unconfirmed"},
		{[]string{"search", "LUNCH"}, 1, map[string]any{"id": "18800002", "confirmed": false}, ""},
		{[]string{"search", "older text"}, 0, nil, ""},
		{[]string{"search", "older text", "--body"}, 1, nil, ""},
		{[]string{"thread", "18800101"}, 2, map[string]any{"id": "18800001"}, "# 2 result(s), " + stats + ", 0 unconfirmed"},
		{[]string{"thread", "18800101", "--limit", "1"}, 1, map[string]any{"id": "18800003"}, "# 1 result(s) (limit reached, more exist), " + stats + ", 0 unconfirmed"},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
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
				assert.Equal(t, tt.summary+"\n", stderr)
			} else {
				assert.Contains(t, stderr, "record(s) skipped")
			}
		})
	}
}

func TestReadCommand(t *testing.T) {
	t.Parallel()
	store := sampleStore(t)
	stdout, stderr, err := runCLI(t, "read", "18800001", "--max-content", "0", "--store", store)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(stdout), &m), stdout)
	assert.Equal(t, map[string]any{"kind": "inline", "source": "inline", "text": "The numbers are attached.", "quote_removed": true, "truncated": false}, m["body"])
	assert.Equal(t, "<18800001@example.com>", m["message_id"])
	assert.Contains(t, stdout, `"<18800001@example.com>"`, "JSON output does not escape < as \\u003c")
	assert.Equal(t, "# 1 result(s), "+stats+", 0 unconfirmed\n", stderr)

	stdout, _, err = runCLI(t, "read", "18800001", "--quoted", "--max-content", "8", "--store", store)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal([]byte(stdout), &m), stdout)
	body, ok := m["body"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "The numb", body["text"])
	assert.Equal(t, true, body["truncated"])

	_, _, err = runCLI(t, "read", "18800001", "--account", "bob@example.com", "--store", store)
	require.NoError(t, err, "the message belongs to this account")
}

func TestErrors(t *testing.T) {
	t.Parallel()
	store := sampleStore(t)
	tests := []struct {
		name string
		args []string
		want error
	}{
		{"missing store", []string{"accounts", "--store", filepath.Join(t.TempDir(), "HxStore.hxd")}, service.ErrStoreMissing},
		{"unknown message", []string{"read", "18800099", "--store", store}, service.ErrNotFound},
		{"read with an unknown account", []string{"read", "18800001", "--account", "x@example.com", "--store", store}, service.ErrNotFound},
		{"thread with an unknown account", []string{"thread", "18800101", "--account", "x@example.com", "--store", store}, service.ErrNotFound},
		{"unknown folder", []string{"messages", "--folder", "nope", "--store", store}, service.ErrNotFound},
		{"unknown account", []string{"messages", "--account", "x@example.com", "--store", store}, service.ErrNotFound},
		{"negative limit", []string{"messages", "--limit", "-1", "--store", store}, ErrInvalidFlag},
		{"negative days", []string{"messages", "--days", "-1", "--store", store}, ErrInvalidFlag},
		{"negative max content", []string{"read", "18800001", "--max-content", "-1", "--store", store}, ErrInvalidFlag},
		{"empty search", []string{"search", " ", "--store", store}, service.ErrInvalidArgument},
		{"zero cache ttl", []string{"mcp", "--cache-ttl", "0s", "--store", store}, ErrInvalidFlag},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stdout, _, err := runCLI(t, tt.args...)
			require.ErrorIs(t, err, tt.want)
			assert.Empty(t, stdout)
			assert.NotContains(t, err.Error(), "x@example.com", "errors do not repeat addresses")
		})
	}
	_, _, err := runCLI(t, "read", "--store", store)
	require.Error(t, err, "a missing argument")
}

func TestEnvironment(t *testing.T) {
	store := sampleStore(t)
	t.Setenv(envStore, store)
	t.Setenv(envMaxContent, "3")
	stdout, _, err := runCLI(t, "read", "18800002")
	require.NoError(t, err)
	assert.Contains(t, stdout, `"text": "Lun"`, "both variables apply")

	for _, bad := range []string{"abc", "-1"} {
		t.Setenv(envMaxContent, bad)
		_, _, err = runCLI(t, "accounts")
		require.ErrorIs(t, err, ErrInvalidFlag)
		assert.Contains(t, err.Error(), envMaxContent, "the error names the variable")
		_, _, err = runCLI(t, "accounts", "--max-content", "10")
		require.NoError(t, err, "the flag overrides a bad variable")
	}
}

func TestEnvironmentErrorsApplyOnlyToTheirFlag(t *testing.T) {
	store := sampleStore(t)
	t.Setenv(envStore, store)

	t.Setenv(envCacheTTL, "abc")
	_, _, err := runCLI(t, "accounts")
	require.NoError(t, err, "commands without --cache-ttl ignore its variable")
	_, _, err = runCLI(t, "mcp")
	require.ErrorIs(t, err, ErrInvalidFlag)
	assert.Contains(t, err.Error(), envCacheTTL)
	_, _, err = runCLI(t, "mcp", "--max-content", "10")
	require.ErrorIs(t, err, ErrInvalidFlag, "--max-content does not hide a bad TTL variable")
	_, _, err = runCLI(t, "mcp", "--cache-ttl", "1m")
	require.NoError(t, err, "the flag overrides its variable")

	for _, bad := range []string{"0", "-5", "99999999999"} {
		t.Setenv(envCacheTTL, bad)
		_, _, err = runCLI(t, "mcp")
		require.ErrorIs(t, err, ErrInvalidFlag, bad)
		assert.Contains(t, err.Error(), envCacheTTL, "the error names the variable")
	}

	t.Setenv(envCacheTTL, "60")
	t.Setenv(envMaxContent, "abc")
	_, _, err = runCLI(t, "mcp", "--cache-ttl", "1m")
	require.ErrorIs(t, err, ErrInvalidFlag, "--cache-ttl does not hide a bad max content variable")
	assert.Contains(t, err.Error(), envMaxContent)
}

func TestMCPCommand(t *testing.T) {
	t.Parallel()
	store := sampleStore(t)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	var errOut bytes.Buffer
	root := NewRootCommand("test", outW, &errOut)
	root.SetIn(inR)
	root.SetArgs([]string{"mcp", "--store", store})
	done := make(chan error, 1)
	go func() {
		done <- root.ExecuteContext(context.Background())
		_ = outW.Close()
	}()

	lines := bufio.NewScanner(outR)
	exchange := func(requests ...string) string {
		t.Helper()
		for _, r := range requests {
			_, err := io.WriteString(inW, r+"\n")
			require.NoError(t, err)
		}
		require.True(t, lines.Scan(), "a reply")
		var frame map[string]any
		require.NoError(t, json.Unmarshal(lines.Bytes(), &frame), "stdout holds only protocol frames")
		assert.Equal(t, "2.0", frame["jsonrpc"])
		return lines.Text()
	}
	exchange(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`)
	reply := exchange(`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_accounts","arguments":{}}}`)
	assert.Contains(t, reply, "bob@example.com")

	require.NoError(t, inW.Close())
	assert.False(t, lines.Scan(), "nothing more on stdout")
	require.NoError(t, <-done, "the server stops cleanly at the end of input")
	assert.Empty(t, errOut.String())
}

func TestMCPCommandStopsCleanlyOnCancel(t *testing.T) {
	t.Parallel()
	store := sampleStore(t)
	r, w := io.Pipe()
	defer func() { _ = w.Close() }()
	var out, errOut bytes.Buffer
	root := NewRootCommand("test", &out, &errOut)
	root.SetIn(r)
	root.SetArgs([]string{"mcp", "--store", store})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err, "a stop signal is not an error")
	case <-time.After(5 * time.Second):
		t.Fatal("the server did not stop")
	}
}
