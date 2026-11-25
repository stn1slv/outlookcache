package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testRedactor() redactor { return redactor{marker: markerPrefix, now: time.Now()} }

func TestRedactorHidesRealText(t *testing.T) {
	t.Parallel()
	r := testRedactor()
	tests := map[string]bool{ // input -> may be printed as is
		"IPM.Note":                         true,
		"IPM.Schedule.Meeting.Request":     true,
		"IPM.NoteAlice_Example":            false, // a name glued to the class
		"IPM.Schedule.Meeting.Resp.PosBob": false,
		"alice@example.com":                false,
		"Quarterly report":                 false,
		"Отчёт за квартал":                 false,
		"oc-probe-1 plain":                 true,
		"Inbox":                            true,
	}
	for in, printable := range tests {
		got := r.show(in)
		assert.Equal(t, printable, strings.Contains(got, in), "%q -> %q", in, got)
	}
}

func TestMarkerExceptionHidesGluedText(t *testing.T) {
	t.Parallel()
	r := testRedactor()
	for _, in := range []string{
		"Alice Example\x00oc-probe-3 large\x00Bob Secret",
		"oc-probe-1 subjectAlice Example alice@example.com",
		"<div>oc-probe-1 body alpha</div><p>Best regards, Bob Secret</p>",
		"oc-probe-1 plainИван Петров",
	} {
		got := r.show(in)
		for _, secret := range []string{"Alice", "Bob", "alice@", "Иван", "regards"} {
			assert.NotContains(t, got, secret, "%q -> %q", in, got)
		}
		assert.Contains(t, got, "oc-probe-")
	}
}

func TestCheckMarker(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{"", "a", "report", "Quarterly"} {
		_, err := checkMarker(bad)
		require.Error(t, err, bad)
	}
	m, err := checkMarker("OC-Probe-5 body")
	require.NoError(t, err)
	assert.Equal(t, "oc-probe-5 body", m)
}

func TestMaskHexHidesTextInAnyScript(t *testing.T) {
	t.Parallel()
	secrets := [][]byte{
		encodeU16("Иван Петров"),
		[]byte("Иван Петров"),
		encodeU16("Al"),
		[]byte("Alice"),
		encodeU16("O’Brien"),
		encodeU16("Ελένη"),
	}
	for _, sec := range secrets {
		in := append(append([]byte{0x01, 0x00, 0x00, 0x00}, sec...), 0xff, 0xfe, 0x00, 0x01)
		got := maskHex(in)
		assert.NotContains(t, got, hex.EncodeToString(sec), "%q leaked", sec)
		assert.Contains(t, got, "[txt:")
	}
	// Binary fields stay readable.
	assert.Equal(t, "0100000014088200", maskHex([]byte{1, 0, 0, 0, 0x14, 0x08, 0x82, 0}))
	var id [8]byte
	binary.LittleEndian.PutUint64(id[:], 0x18757bbd)
	assert.Equal(t, hex.EncodeToString(id[:]), maskHex(id[:]))
}

func TestDumpHidesNonLatinNames(t *testing.T) {
	t.Parallel()
	payload := bytes.Join([][]byte{
		{0x06, 0x00, 0x70, 0x05},
		encodeU16("Отчёт за квартал"),
		{0, 0, 0x01, 0x02},
		[]byte("Ім'я Прізвище"),
		{0, 0},
		encodeU16("oc-probe-9 subject"),
	}, nil)
	var out bytes.Buffer
	dump(&out, testRedactor(), payload, 0, 0)
	got := out.String()
	for _, sec := range []string{"Отчёт", "Прізвище", hex.EncodeToString(encodeU16("Отчёт")), hex.EncodeToString([]byte("Ім"))} {
		assert.NotContains(t, got, sec)
	}
	assert.Contains(t, got, "oc-probe-9 subject")
}

func TestTickNoteHidesRecentTimesByDefault(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 24, 21, 0, 0, 0, time.UTC)
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], uint64(ticksUnixEpoch+now.Add(-time.Hour).Unix()*ticksPerSecond))
	got, ok := redactor{now: now}.tickNote(b[:])
	require.True(t, ok)
	assert.Equal(t, "tick(recent)", got)
	got, _ = redactor{now: now, showTicks: true}.tickNote(b[:])
	assert.Equal(t, "tick=2026-11-24T20:00:00Z", got)
	// The window follows r.now instead of a fixed year.
	later := now.AddDate(2, 0, 0)
	binary.LittleEndian.PutUint64(b[:], uint64(ticksUnixEpoch+later.Unix()*ticksPerSecond))
	_, ok = redactor{now: later}.tickNote(b[:])
	assert.True(t, ok)
}
