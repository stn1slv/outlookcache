package main

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRedactorHidesRealText(t *testing.T) {
	t.Parallel()
	r := redactor{marker: "oc-probe", now: time.Now()}
	tests := map[string]bool{ // input -> may be printed as is
		"IPM.Note":                         true,
		"IPM.Schedule.Meeting.Request":     true,
		"IPM.NoteAlice_Example":            false, // a name glued to the class
		"IPM.Schedule.Meeting.Resp.PosBob": false,
		"alice@example.com":                false,
		"Quarterly report":                 false,
		"oc-probe-1 plain":                 true,
		"Inbox":                            true,
	}
	for in, printable := range tests {
		got := r.show(in)
		assert.Equal(t, printable, strings.Contains(got, in), "%q -> %q", in, got)
	}
}

func TestSafeHexHidesText(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "<text-like 16 bytes>", safeHex(encodeU16("Alice Ex")))
	assert.Equal(t, "<text-like 10 bytes>", safeHex([]byte("Alice Exam")))
	assert.Equal(t, "0100000014088200", safeHex([]byte{1, 0, 0, 0, 0x14, 0x08, 0x82, 0}))
}
