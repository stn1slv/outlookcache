package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stn1slv/outlookcache/internal/hxtest"
)

func u64(v uint64) []byte { return binary.LittleEndian.AppendUint64(nil, v) }
func u32(v uint32) []byte { return binary.LittleEndian.AppendUint32(nil, v) }

// folderRecord builds a synthetic form 1 child folder record.
func folderRecord(parent, id, owner uint64, sortKey []byte, name string) []byte {
	return bytes.Join([][]byte{
		{0xe3, 0x01},
		make([]byte, 8), u64(parent), sortKey, encodeU16(name),
		{0, 0},
		{0x01, 0x8c, 0x05, 0, 0, 0x05, 0x00, 0xd8, 0x04, 0x8c, 0x05, 0, 0, 0, 0, 0x4d},
		make([]byte, 9),
		u64(id), u32(2), u64(owner), make([]byte, 16),
	}, nil)
}

func TestParseFolderRecords(t *testing.T) {
	t.Parallel()
	const parent, id, owner = 0x18757ba5, 0x1880955b, 0x18757ba5
	// The sort key holds a zero byte inside, as real keys do.
	key := []byte{0x0e, 0x7c, 0x00, 0x0a, 0x0e, 0x0a, 0x01, 0x12, 0x00}
	p := append([]byte{1, 2, 3}, folderRecord(parent, id, owner, key, "oc-probe-folder")...)
	recs := parseFolderRecords(p, 4096)
	require.Len(t, recs, 1)
	assert.Equal(t, folderRec{id: id, parent: parent, owner: owner, name: "oc-probe-folder", block: 4096}, recs[0])

	bad := folderRecord(parent, id, owner, key, "oc-probe-folder")
	bad[len(bad)-16-8-4-8-9-1] = 0x4c // break the 0x4d marker
	assert.Empty(t, parseFolderRecords(bad, 0))
	noHead := folderRecord(parent, id, owner, key, "oc-probe-folder")
	noHead[0] = 0
	assert.Empty(t, parseFolderRecords(noHead, 0))
}

func TestTreeNamePrintsOnlyWholeSafeNames(t *testing.T) {
	t.Parallel()
	r := testRedactor()
	assert.Equal(t, `"oc-probe-folder"`, treeName(r, "oc-probe-folder"))
	assert.Equal(t, `"Inbox"`, treeName(r, "Inbox"))
	for _, real := range []string{"oc-probe-folder secret plans", "oc-probe Bob", "Budget", "Отчёты"} {
		got := treeName(r, real)
		assert.NotContains(t, got, real)
		assert.NotContains(t, got, "secret")
		assert.NotContains(t, got, "Bob")
	}
}

func TestNameFindPrintsNoText(t *testing.T) {
	t.Parallel()
	frame := bytes.Join([][]byte{
		objMagic, u64(0x18800001),
		{0xc7, 0x00},
		u32(0), // length patched below
		encodeU16("Bob Secret"), encodeU16("Alice Example"), encodeU16(" sent mail"),
		u32(0x80000000 | uint32(len(encodeU16("Alice Example"))+2)),
		{0, 0},
		encodeU16("Alice Example"),
		{0, 0},
	}, nil)
	binary.LittleEndian.PutUint32(frame[14:], uint32(len(frame)))
	data := hxtest.File(hxtest.Block(8, 0x18800001, frame))
	var out bytes.Buffer
	require.NoError(t, nameFind(data, "alice example", &out))
	got := out.String()
	for _, secret := range []string{
		"Alice", "alice", "Bob", "Secret", "sent",
		hex.EncodeToString(encodeU16("Bo")), hex.EncodeToString(encodeU16("et")), hex.EncodeToString(encodeU16(" s")),
	} {
		assert.NotContains(t, strings.ToLower(got), strings.ToLower(secret))
	}
	assert.Contains(t, got, "frame[id=18800001 type=0x00c7")
	assert.Contains(t, got, "prefix=length-prefixed")
	assert.Contains(t, got, "prefix=-")
}

func TestFindNameOneByteAtEnd(t *testing.T) {
	t.Parallel()
	p := []byte("xxzz")
	counts := map[string]int{}
	require.NotPanics(t, func() { findName(p, asciiLower(p), 0, needle{encUTF8, []byte("z"), "z"}, counts) })
	total := 0
	for _, n := range counts {
		total += n
	}
	assert.Equal(t, 2, total, "adjacent one-byte matches")
}

func TestReadLiveIndexRejectsBadHeader(t *testing.T) {
	t.Parallel()
	const size = indexSlots * indexSlotSize
	data := make([]byte, 0x100+2*size+0x100)
	binary.LittleEndian.PutUint64(data[hdrAreaA:], 0x100)
	for _, b := range []uint64{0, 0x80, 0x100, 0x200, 0x100 + size - 1, 1 << 62, ^uint64(0)} {
		binary.LittleEndian.PutUint64(data[hdrAreaB:], b)
		require.NotPanics(t, func() {
			_, err := readLiveIndex(data)
			assert.Error(t, err, "b=%#x", b)
		})
	}
	binary.LittleEndian.PutUint64(data[hdrAreaB:], 0x100+size)
	binary.LittleEndian.PutUint32(data[0x100+5*7:], 0x30)   // slot 7 -> block at 0x3000
	binary.LittleEndian.PutUint32(data[0x100+5*8+1:], 0x40) // not on a slot boundary
	li, err := readLiveIndex(data)
	require.NoError(t, err)
	assert.True(t, li.indexed(0x3000, 0))
	assert.False(t, li.indexed(0x3000, 1))
	assert.False(t, li.indexed(0x4000, 0), "value not at a slot start")
	assert.False(t, li.indexed(0x3010, 0), "not on a 256-byte boundary")
}

func TestCurrentCopyRule(t *testing.T) {
	t.Parallel()
	li := liveIndex{areas: [2]map[uint32]bool{{0x30: true, 0x50: true}, {}}}
	indexed := c7copy{block: 0x3000, stamp: 1, status: 0x82}
	newer := c7copy{block: 0x4000, stamp: 2, status: 0x80}
	c, ok, amb := li.current([]c7copy{indexed, newer})
	assert.Equal(t, newer, c, "a newer unindexed copy wins (index lag)")
	assert.False(t, ok)
	assert.False(t, amb)

	tieIndexed := c7copy{block: 0x5000, stamp: 3, status: 0x82}
	tieOther := c7copy{block: 0x6000, stamp: 3, status: 0x80}
	c, ok, amb = li.current([]c7copy{tieOther, tieIndexed})
	assert.Equal(t, tieIndexed, c, "the index breaks a stamp tie")
	assert.True(t, ok)
	assert.False(t, amb)

	tieA := c7copy{block: 0x7000, stamp: 4, status: 0x80}
	tieB := c7copy{block: 0x8000, stamp: 4, status: 0x82}
	_, ok, amb = li.current([]c7copy{tieA, tieB})
	assert.False(t, ok)
	assert.True(t, amb, "same stamp, different read state, no index help")
}

func TestTextUnit(t *testing.T) {
	t.Parallel()
	for _, r := range "Aя7中" {
		assert.True(t, textUnit(uint16(r)), "%q", r)
	}
	for _, v := range []uint16{0, 0x0002, 0x0300, 0x2000} {
		assert.False(t, textUnit(v), "%#x", v)
	}
}

func TestMatchCommandsNeedEnv(t *testing.T) { //nolint:paralleltest // t.Setenv
	t.Setenv(matchEnv, "")
	f := filepath.Join(t.TempDir(), "x.hxd")
	require.NoError(t, os.WriteFile(f, hxtest.File(), 0o600))
	for _, c := range []string{"namefind", "addrcount"} {
		err := run(context.Background(), []string{c, "-file", f}, &bytes.Buffer{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), matchEnv)
	}
	err := run(context.Background(), []string{"framesin", "-file", f}, &bytes.Buffer{})
	assert.ErrorContains(t, err, "-block")
}

func TestProbeSubject(t *testing.T) {
	t.Parallel()
	assert.True(t, probeSubject(append([]byte{5, 0, 0, 0}, encodeU16("RE: oc-probe-6 fields")...)))
	assert.True(t, probeSubject(encodeU16("oc-probe-1 plain")))
	assert.False(t, probeSubject(encodeU16("Quarterly report from oc-probe team")))
	assert.False(t, probeSubject([]byte("oc-probe-1 plain"))) // UTF-8 body text is not a subject
}

func TestProbeTicksNeedsOptIn(t *testing.T) {
	t.Parallel()
	err := probeTicks(nil, 1, false, time.Now(), &bytes.Buffer{})
	assert.ErrorContains(t, err, "-ticks")
}

func TestCheckpointStampIgnoresTopMatches(t *testing.T) {
	t.Parallel()
	li := liveIndex{areas: [2]map[uint32]bool{{}, {}}}
	copies := map[uint64][]c7copy{}
	for i := range 8 {
		li.areas[0][uint32(i+1)] = true
		copies[uint64(i)] = []c7copy{{block: int64(i+1) * 256, stamp: uint64(100 + i)}}
	}
	assert.Equal(t, uint64(102), checkpointStamp(copies, li), "the 5 highest are ignored")
	assert.Zero(t, checkpointStamp(map[uint64][]c7copy{1: {{block: 256, stamp: 9}}}, li), "too few indexed copies")
}

func TestScanItemsAcceptsShortItems(t *testing.T) {
	t.Parallel()
	obj := make([]byte, itemMinLen)
	copy(obj, objMagic)
	binary.LittleEndian.PutUint64(obj[4:], 0x18800010)
	binary.LittleEndian.PutUint16(obj[12:], 0xc7)
	binary.LittleEndian.PutUint32(obj[14:], uint32(len(obj)))
	binary.LittleEndian.PutUint64(obj[msgFolderAt-itemShift:], 0x187578ae)
	binary.LittleEndian.PutUint64(obj[itemConvAt:], 0x18800020)
	var got []c7copy
	require.NoError(t, scanItems(hxtest.File(hxtest.Block(8, 0x18800010, obj)), func(c c7copy, ok bool) {
		if ok {
			got = append(got, c)
		}
	}))
	require.Len(t, got, 1)
	assert.Equal(t, uint64(0x187578ae), got[0].folder)
	assert.Equal(t, uint64(0x18800020), got[0].conv)
}

// syntheticItem builds an item object with a length table and a string
// run in the second layout (subject before the class).
func syntheticItem(id, conv uint64, before, after []string) []byte {
	head := make([]byte, 1700)
	put := func(off int, s string) { binary.LittleEndian.PutUint16(head[off:], uint16(len(encodeU16(s))+2)) }
	put(lenSenderAddr, "alice@example.com")
	put(lenSenderName, "Alice Example")
	put(lenMessageID, "<m1@example.com>")
	put(lenSubject, "Quarterly numbers")
	put(lenTopic, "RE: Quarterly numbers")
	put(lenPreview, "short preview")
	var run []byte
	run = binary.LittleEndian.AppendUint64(run, id)
	run = append(run, 0, 0, 0xc7, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	run = binary.LittleEndian.AppendUint64(run, conv)
	for _, s := range append(append(before, "IPM.Note"), after...) {
		run = append(append(run, encodeU16(s)...), 0, 0)
	}
	run = append(run, 0, 0, 0, 0)
	return bytes.Join([][]byte{head, run}, nil)
}

func TestExtractTextUsesLengthTable(t *testing.T) {
	t.Parallel()
	obj := syntheticItem(0x18800001, 0x18800002,
		[]string{"Quarterly numbers", "alice@example.com", "Bob Other", "Alice Example"},
		[]string{"<m1@example.com>", "<parent@example.com>", "short preview", "RE: Quarterly numbers"})
	got, ok := extractText(obj, 0x18800001, 0x18800002)
	require.True(t, ok)
	assert.Equal(t, "IPM.Note", got.class)
	assert.Equal(t, "alice@example.com", got.senderAddr)
	assert.Equal(t, "Alice Example", got.senderName)
	assert.Equal(t, "<m1@example.com>", got.messageID)
	assert.Equal(t, "short preview", got.preview)
	assert.Equal(t, "Quarterly numbers", got.subject, "subject found before the class")
	assert.Equal(t, "RE: Quarterly numbers", got.topic)
}

func TestParseRecipients(t *testing.T) {
	t.Parallel()
	obj := bytes.Join([][]byte{
		{0x80, 0, 0, 0, 0x0e, 0x7c, 0x0e, 0x70, 0x01, 0x01, 0x00},
		encodeU16("alice@example.com"),
		{0, 0},
		encodeU16("Alice Example"),
		{0, 0},
		{0x01, 0x00, 0x05, 0x00, 0x00, 0x00},
		encodeU16("bob@example.com"),
		{0, 0},
		encodeU16("Bob Other"),
		{0, 0},
		{0x02, 0x00, 0x00, 0x00},
		encodeU16("alice@example.com"),
		{0, 0}, // repeated address
	}, nil)
	got := parseRecipients(obj)
	assert.Equal(t, []recipient{{"alice@example.com", "Alice Example"}, {"bob@example.com", "Bob Other"}}, got)
}
