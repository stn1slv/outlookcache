package store

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stn1slv/outlookcache/internal/hxstore"
	"github.com/stn1slv/outlookcache/internal/hxtest"
)

const (
	tRoot   = 0x18750001
	tInbox  = 0x18750010
	tSent   = 0x18750020
	tItem   = 0x18800001
	tConv   = 0x18800002
	tParent = 0x18800000 // answered item
)

func u64(v uint64) []byte { return binary.LittleEndian.AppendUint64(nil, v) }
func u32(v uint32) []byte { return binary.LittleEndian.AppendUint32(nil, v) }

// framing returns an object of the given type with body bytes after the
// 18-byte framing header, padded to at least n bytes.
func framing(id uint64, typ uint16, n int, body []byte) []byte {
	b := make([]byte, max(n, 18+len(body)))
	copy(b, hxstore.FramingMagic())
	binary.LittleEndian.PutUint64(b[4:], id)
	binary.LittleEndian.PutUint16(b[12:], typ)
	binary.LittleEndian.PutUint32(b[14:], uint32(len(b)))
	copy(b[18:], body)
	return b
}

func slotRef(id, owner uint64) []byte {
	return bytes.Join([][]byte{{0x4d, 0}, make([]byte, 8), u64(id), u32(2), u64(owner), u64(id), u64(owner)}, nil)
}

func folderRecord(parent, id, owner uint64, name string) []byte {
	return bytes.Join([][]byte{
		recHead, u64(parent),
		{0x0e, 0x7c, 0x0e, 0x0a, 0x01, 0x12, 0x00},
		encodeU16(name),
		{0, 0},
		{0x01, 0x8c, 0x05, 0, 0, 0x05, 0x00, 0xd8, 0x04, 0x8c, 0x05, 0, 0, 0, 0, 0x4d},
		nineZ,
		u64(id), u32(2), u64(owner), make([]byte, 16),
	}, nil)
}

func rootPayload() []byte {
	head := make([]byte, 2048)
	copy(head[861:], slotRef(tInbox, tRoot))
	copy(head[1549:], slotRef(tSent, tRoot))
	rest := bytes.Join([][]byte{
		folderRecord(tRoot, tInbox, tRoot, "Inbox"),
		encodeU16("/Users/x/Files/S0/7/EFMData/1.dat"),
		{0, 0},
		framing(tRoot, 0x02e0, 40, nil),
	}, nil)
	return bytes.Join([][]byte{head, rest}, nil)
}

func ticksOf(t time.Time) []byte {
	return u64(uint64(t.UnixNano()/100) + ticksUnixEpoch)
}

func itemPayload(sent time.Time) []byte {
	obj := make([]byte, 1800)
	copy(obj[itemStampAt:], []byte{9, 0, 0, 0, 0, 0})
	copy(obj[itemConvAt:], u64(tConv))
	copy(obj[itemReceivedAt:], ticksOf(sent.Add(time.Minute)))
	copy(obj[itemSentAt:], ticksOf(sent))
	copy(obj[itemReplyToAt:], u64(tParent))
	copy(obj[itemFolderAt:], u64(tInbox))
	obj[itemImportanceAt] = 2
	obj[itemAttachAt] = 0x40
	obj[itemStatusAt] = 0x80 // unread
	put := func(off int, s string) { binary.LittleEndian.PutUint16(obj[off:], uint16(u16Size(s))) }
	put(lenSenderAddr, "alice@example.com")
	put(lenSenderName, "Alice Example")
	put(lenMessageID, "<m1@example.com>")
	put(lenSubject, "Quarterly numbers")
	put(lenPreview, "short preview")
	run := bytes.Join([][]byte{u64(tItem), {0, 0, 0xc7, 0, 0, 0, 0, 0, 0, 0, 0, 0}, u64(tConv)}, nil)
	for _, s := range []string{"alice@example.com", "Alice Example", "IPM.Note", "<m1@example.com>", "short preview", "Quarterly numbers", "Quarterly numbers"} {
		run = bytes.Join([][]byte{run, encodeU16(s), {0, 0}}, nil)
	}
	body := bytes.Join([][]byte{obj[18:], run, make([]byte, 8)}, nil)
	to := bytes.Join([][]byte{{0x80, 0, 0x0e, 0x7c, 0x01, 0x12, 0x00}, encodeU16("bob@example.com"), {0, 0}, encodeU16("Bob Other"), {0, 0}}, nil)
	att := make([]byte, 700)
	copy(att[100:], bytes.Join([][]byte{{0x0e, 0x7c, 0x01, 0x12, 0x00}, encodeU16("report.pdf"), {0, 0}}, nil))
	binary.LittleEndian.PutUint32(att[attachSizeAt:], 1234)
	return bytes.Join([][]byte{
		framing(tItem, typeMessage, 0, body),
		framing(tItem, typeTo, 0, to),
		framing(tItem, typeAttachment, 0, att[18:]),
		framing(tConv, 0x2ec, 40, nil),
		framing(tParent, 0x2ec, 40, nil),
	}, nil)
}

func TestLoadSyntheticStore(t *testing.T) {
	t.Parallel()
	sent := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	data := hxtest.FileWithIndex([][]byte{
		hxtest.Block(8, tRoot, rootPayload()),
		hxtest.Block(8, tInbox, framing(tInbox, 0x1e3, 40, nil)),
		hxtest.Block(8, tSent, framing(tSent, 0x1e3, 40, nil)),
		hxtest.Block(8, tItem, itemPayload(sent)),
	}, []bool{true, true, true, false})
	s, err := Load(data, "t.hxd")
	require.NoError(t, err)

	require.Len(t, s.Accounts, 1)
	a := s.Accounts[0]
	assert.Equal(t, "18750001", a.ID)
	assert.Equal(t, "7", a.Dir)
	assert.Equal(t, map[string]string{"inbox": "18750010", "sent": "18750020"}, a.Roles)

	byID := map[string]Folder{}
	for _, f := range s.Folders {
		byID[f.ID] = f
	}
	assert.Equal(t, Folder{ID: "18750010", AccountID: "18750001", Name: "Inbox", Role: "inbox"}, byID["18750010"])
	assert.Equal(t, "sent", byID["18750020"].Role)

	require.Len(t, s.Messages, 1)
	m := s.Messages[0]
	assert.Equal(t, "18800001", m.ID)
	assert.Equal(t, "18800002", m.ConversationID)
	assert.Equal(t, "18800000", m.ReplyToID)
	assert.Equal(t, "18750010", m.FolderID)
	assert.Equal(t, "18750001", m.AccountID)
	assert.Equal(t, "Quarterly numbers", m.Subject)
	assert.Equal(t, &Recipient{Name: "Alice Example", Address: "alice@example.com"}, m.From)
	assert.Equal(t, []Recipient{{Name: "Bob Other", Address: "bob@example.com"}}, m.To)
	assert.Equal(t, "short preview", m.Preview)
	assert.Equal(t, "<m1@example.com>", m.MessageID)
	assert.Equal(t, "IPM.Note", m.ItemClass)
	require.NotNil(t, m.Sent)
	assert.True(t, m.Sent.Equal(sent))
	assert.True(t, m.Received.Equal(sent.Add(time.Minute)))
	assert.False(t, m.Read)
	assert.Equal(t, "high", m.Importance)
	assert.True(t, m.HasAttachments)
	assert.Equal(t, []Attachment{{Name: "report.pdf", Size: 1234}}, m.Attachments)
	assert.False(t, m.Confirmed, "the item block has no direct slot")
	assert.Equal(t, 1, s.Stats.Unconfirmed)
	assert.Empty(t, s.Stats.Damage)
}

func TestLoadRejectsOtherVersion(t *testing.T) {
	t.Parallel()
	data := hxtest.FileWithIndex(nil, nil)
	data[8] = 'j'
	_, err := Load(data, "t.hxd")
	require.ErrorIs(t, err, ErrUnsupportedVersion)
}

func TestPickCurrent(t *testing.T) {
	t.Parallel()
	older := itemCopy{stamp: 1, status: 0x82, direct: true}
	newer := itemCopy{stamp: 2, status: 0x80}
	c, confirmed, ambiguous := pickCurrent([]itemCopy{older, newer})
	assert.Equal(t, newer, c, "the stamp decides; the index lags")
	assert.False(t, confirmed)
	assert.False(t, ambiguous)

	tieDirect := itemCopy{stamp: 3, status: 0x82, direct: true}
	tieOther := itemCopy{stamp: 3, status: 0x80}
	c, confirmed, _ = pickCurrent([]itemCopy{tieOther, tieDirect})
	assert.Equal(t, tieDirect, c)
	assert.True(t, confirmed)

	_, _, ambiguous = pickCurrent([]itemCopy{{stamp: 4, status: 0x80}, {stamp: 4, status: 0x82}})
	assert.True(t, ambiguous)
}

func TestReadRecipientsDedupes(t *testing.T) {
	t.Parallel()
	obj := bytes.Join([][]byte{
		{0x0e, 0x7c, 0x01, 0x12, 0x00},
		encodeU16("alice@example.com"),
		{0, 0},
		encodeU16("Alice"),
		{0, 0},
		{0x02, 0, 0, 0},
		encodeU16("alice@example.com"),
		{0, 0},
		{0x03, 0, 0, 0},
		encodeU16("carol@example.com"),
		{0, 0},
		{0, 0},
	}, nil)
	assert.Equal(t, []Recipient{{"Alice", "alice@example.com"}, {"", "carol@example.com"}}, readRecipients(obj))
}

func TestTicksRejectsImplausible(t *testing.T) {
	t.Parallel()
	assert.Nil(t, ticks(make([]byte, 8), 0))
	assert.Nil(t, ticks(ticksOf(time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)), 0))
	got := ticks(ticksOf(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)), 0)
	require.NotNil(t, got)
	assert.Equal(t, 2026, got.Year())
}
