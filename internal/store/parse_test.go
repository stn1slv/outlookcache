package store

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stn1slv/outlookcache/internal/hxstore"
)

// textItem builds an item object with a length table and a string run.
// sizes maps a length-table offset to the string whose size it holds.
func textItem(sizes map[int]string, before, after []string) []byte {
	head := make([]byte, itemMinLen)
	for off, s := range sizes {
		binary.LittleEndian.PutUint16(head[off:], uint16(u16Size(s)))
	}
	run := bytes.Join([][]byte{u64(tItem), {0, 0, 0xc7, 0, 0, 0, 0, 0, 0, 0, 0, 0}, u64(tConv)}, nil)
	for _, s := range append(append(before, "IPM.Note"), after...) {
		run = bytes.Join([][]byte{run, encodeU16(s), {0, 0}}, nil)
	}
	return bytes.Join([][]byte{head, run, make([]byte, 4)}, nil)
}

func TestParseItemTextSecondLayout(t *testing.T) {
	t.Parallel()
	obj := textItem(map[int]string{
		lenSenderAddr: "alice@example.com", lenSenderName: "Alice Example", lenMessageID: "<m1@example.com>",
		lenSubject: "Quarterly numbers", lenTopic: "RE: Quarterly numbers", lenPreview: "short preview",
	},
		[]string{"Quarterly numbers", "alice@example.com", "Bob Other", "Alice Example"},
		[]string{"<m1@example.com>", "<parent@example.com>", "short preview", "RE: Quarterly numbers"})
	got, ok := ParseItemText(obj, tItem, tConv)
	require.True(t, ok)
	assert.Equal(t, ItemText{
		SenderAddr: "alice@example.com", SenderName: "Alice Example", Class: "IPM.Note",
		MessageID: "<m1@example.com>", Preview: "short preview", Subject: "Quarterly numbers",
	}, got, "subject found before the class")
}

func TestParseItemTextEqualSizes(t *testing.T) {
	t.Parallel()
	// Subject and preview have the same size; the preview comes first.
	obj := textItem(map[int]string{lenSubject: "subject abc", lenPreview: "preview xyz"},
		nil, []string{"preview xyz", "subject abc"})
	got, ok := ParseItemText(obj, tItem, tConv)
	require.True(t, ok)
	assert.Equal(t, "preview xyz", got.Preview)
	assert.Equal(t, "subject abc", got.Subject)

	// No subject entry: the topic is used.
	obj = textItem(map[int]string{lenTopic: "only a topic"}, nil, []string{"only a topic"})
	got, ok = ParseItemText(obj, tItem, tConv)
	require.True(t, ok)
	assert.Equal(t, "only a topic", got.Subject)

	_, ok = ParseItemText(obj, tItem, tConv+1)
	assert.False(t, ok, "another conversation ID does not match the anchor")
	_, ok = ParseItemText(obj[:100], tItem, tConv)
	assert.False(t, ok, "short object")
}

func TestParseRecipientsRejectsNoise(t *testing.T) {
	t.Parallel()
	key := []byte{0x0e, 0x7c, 0x01, 0x12, 0x00}
	obj := bytes.Join([][]byte{
		{0x40, 0x00, 0x01, 0x00},                                                          // a value of 64: "@" alone is not an address
		key, encodeU16("dave@example.com"), {0, 0}, encodeU16("erin@example.com"), {0, 0}, // name that looks like an address
		key, encodeU16("frank@mailhost"), {0, 0}, {0x05, 0x00, 0x00, 0x80, 0, 0}, // host without a dot; binary after it
	}, nil)
	assert.Equal(t, []Recipient{
		{Name: "erin@example.com", Address: "dave@example.com"},
		{Address: "frank@mailhost"},
	}, ParseRecipients(obj))
}

func TestParseAttachment(t *testing.T) {
	t.Parallel()
	key := []byte{0x0e, 0x7c, 0x01, 0x12, 0x00}
	sized := func(parts ...[]byte) []byte {
		size := binary.LittleEndian.AppendUint32(make([]byte, attachSizeAt), 42)
		return bytes.Join(append([][]byte{size}, parts...), nil)
	}
	tests := []struct {
		name string
		obj  []byte
		want string
	}{
		{"address before the name", sized(encodeU16("alice@example.com"), []byte{0, 0}, key, encodeU16("report.pdf"), []byte{0, 0}), "report.pdf"},
		{"cache path first", sized(key, encodeU16("/Files/S0/2/Attachments/0/report[1].pdf"), []byte{0, 0}, key, encodeU16("report.pdf"), []byte{0, 0}), "report.pdf"},
		{"at sign in the name", sized(key, encodeU16("icon@2x.png"), []byte{0, 0}), "icon@2x.png"},
		{"colon in the name", sized(key, encodeU16("10:30 notes.txt"), []byte{0, 0}), "10:30 notes.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			a, ok := ParseAttachment(tt.obj)
			require.True(t, ok)
			assert.Equal(t, Attachment{Name: tt.want, Size: 42}, a)
		})
	}
	_, ok := ParseAttachment(sized(key, encodeU16("README"), []byte{0, 0}))
	assert.False(t, ok, "a name without an extension is not found")
}

func TestReadFolderRecords(t *testing.T) {
	t.Parallel()
	p := bytes.Join([][]byte{make([]byte, 10), folderRecord(tRoot, tInbox, tRoot, "Budget"), make([]byte, 10)}, nil)
	got := readFolderRecords(p, nil)
	require.Len(t, got, 1)
	assert.Equal(t, folderRec{id: tInbox, parent: tRoot, owner: tRoot, name: "Budget"}, got[0])
	assert.Empty(t, readFolderRecords(p, idSpace{tRoot: true}), "folder ID not in the store")
}

func FuzzParsers(f *testing.F) {
	f.Add([]byte{0x40, 0, 0x2e, 0})
	f.Add(folderRecord(tRoot, tInbox, tRoot, "Budget"))
	f.Add(textItem(map[int]string{lenSubject: "s"}, nil, []string{"s"}))
	f.Fuzz(func(_ *testing.T, b []byte) {
		ParseRecipients(b)
		ParseAttachment(b)
		ParseItemText(b, tItem, tConv)
		readFolderRecords(b, nil)
		readRootSlots(b, tRoot)
		filesDirs(b)
		hxstore.ForEachFraming(b, func(hxstore.Framing) {})
	})
}
