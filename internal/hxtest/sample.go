package hxtest

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"time"
	"unicode/utf16"
)

// IDs of the sample store written by WriteSample.
const (
	SampleRoot     = 0x18750001
	SampleInbox    = 0x18750010
	SampleSent     = 0x18750020
	SampleDeleted  = 0x18750030
	SampleProjects = 0x18750040 // custom folder under Inbox
	SampleM1       = 0x18800001 // Inbox, unread, inline body with a quote, attachment
	SampleM2       = 0x18800002 // Inbox, read, body in EFMData, not in a direct slot
	SampleM3       = 0x18800003 // Sent, no stored body, same conversation as M1
	SampleConv1    = 0x18800101
	SampleConv2    = 0x18800102
	SampleDir      = "7"
	SampleEFMFile  = "42.dat"
	SampleAddress  = "bob@example.com" // the account's own address
	sampleName     = "Bob Other"
)

// IDs of the second account written by WriteSampleTwoAccounts. It has no
// Sent mail, so it has no address.
const (
	SampleRoot2    = 0x18760001
	SampleInbox2   = 0x18760010
	sampleSent2    = 0x18760020
	sampleDeleted2 = 0x18760030
	SampleM4       = 0x18800004 // Inbox of the second account, unread, no stored body
	SampleConv3    = 0x18800103
)

// Sample bodies (synthetic).
const (
	SampleBody1 = `<html><body><p>The numbers are attached.</p><div id="divRplyFwdMsg"><b>From:</b> Carol</div><p>older text</p></body></html>`
	SampleBody2 = `<html><head><style>p{}</style></head><body><p>Lunch at noon on Friday?</p></body></html>`
)

// Layout of the synthetic records; the store package reads the same
// offsets (see docs/STORAGE.md).
const (
	itemLen        = 1800
	itemStampAt    = 360
	itemConvAt     = 436
	itemReceivedAt = 536
	itemSentAt     = 976
	itemFolderAt   = 1156
	itemImportance = 1456
	itemAttachAt   = 1758
	itemStatusAt   = 1762
	lenMessageID   = 1480
	lenSubject     = 1512
	lenPreview     = 1556
	lenSenderName  = 1640
	lenSenderAddr  = 1648
	attachSizeAt   = 670
	bodyClassAt    = 1893
	bodyMainAt     = 1683
	ticksEpoch     = 621355968000000000
	typeMessage    = 0xc7
	typeTo         = 0xcc
	typeAttachment = 0x168
)

func u16(s string) []byte {
	var b []byte
	for _, u := range utf16.Encode([]rune(s)) {
		b = binary.LittleEndian.AppendUint16(b, u)
	}
	return b
}

func u16z(s string) []byte { return append(u16(s), 0, 0) }

func le64(v uint64) []byte { return binary.LittleEndian.AppendUint64(nil, v) }

// Framing returns an object: `06 00 70 05`, ID, type, length, then body,
// padded to at least n bytes.
func Framing(id uint64, typ uint16, n int, body []byte) []byte {
	b := make([]byte, max(n, 18+len(body)))
	copy(b, []byte{0x06, 0x00, 0x70, 0x05})
	binary.LittleEndian.PutUint64(b[4:], id)
	binary.LittleEndian.PutUint16(b[12:], typ)
	binary.LittleEndian.PutUint32(b[14:], uint32(len(b)))
	copy(b[18:], body)
	return b
}

func slotRef(id, owner uint64) []byte {
	return bytes.Join([][]byte{{0x4d, 0}, make([]byte, 8), le64(id), {2, 0, 0, 0}, le64(owner), le64(id), le64(owner)}, nil)
}

func folderRecord(parent, id, owner uint64, name string) []byte {
	return bytes.Join([][]byte{
		{0xe3, 0x01, 0, 0, 0, 0, 0, 0, 0, 0},
		le64(parent),
		{0x0e, 0x7c, 0x0e, 0x0a, 0x01, 0x12, 0x00},
		u16z(name),
		{0x01, 0x8c, 0x05, 0, 0, 0x05, 0x00, 0xd8, 0x04, 0x8c, 0x05, 0, 0, 0, 0, 0x4d},
		make([]byte, 9), le64(id),
		{2, 0, 0, 0},
		le64(owner), make([]byte, 16),
	}, nil)
}

func rootPayload(root, inbox, sent, deleted uint64, dir string, extra ...[]byte) []byte {
	head := make([]byte, 2048)
	copy(head[861:], slotRef(inbox, root))
	copy(head[1549:], slotRef(sent, root))
	copy(head[429:], slotRef(deleted, root))
	parts := make([][]byte, 0, 5+len(extra))
	parts = append(parts, head, folderRecord(root, inbox, root, "Inbox"), folderRecord(root, sent, root, "Sent Items"))
	parts = append(parts, extra...)
	parts = append(parts, u16z("/Users/x/Files/S0/"+dir+"/EFMData/1.dat"), Framing(root, 0x02e0, 40, nil))
	return bytes.Join(parts, nil)
}

type sampleItem struct {
	id, conv, folder   uint64
	read, attach, high bool
	from, fromName     string
	to                 [][2]string // address, name
	subject, preview   string
	received           time.Time
	attachName         string
	head               []byte // body record before the framings
}

func ticks(t time.Time) []byte { return le64(uint64(t.UnixNano()/100) + ticksEpoch) }

func (it sampleItem) payload() []byte {
	obj := make([]byte, itemLen)
	copy(obj[itemStampAt:], []byte{9, 0, 0, 0, 0, 0})
	copy(obj[itemConvAt:], le64(it.conv))
	copy(obj[itemReceivedAt:], ticks(it.received))
	copy(obj[itemSentAt:], ticks(it.received.Add(-time.Minute)))
	copy(obj[itemFolderAt:], le64(it.folder))
	obj[itemImportance] = 1
	if it.high {
		obj[itemImportance] = 2
	}
	if it.attach {
		obj[itemAttachAt] = 0x40
	}
	if it.read {
		obj[itemStatusAt] = 0x02
	}
	msgID := fmt.Sprintf("<%x@example.com>", it.id)
	size := func(off int, s string) {
		binary.LittleEndian.PutUint16(obj[off:], uint16(2*len(utf16.Encode([]rune(s)))+2))
	}
	size(lenSenderAddr, it.from)
	size(lenSenderName, it.fromName)
	size(lenMessageID, msgID)
	size(lenSubject, it.subject)
	size(lenPreview, it.preview)
	run := bytes.Join([][]byte{le64(it.id), {0, 0, typeMessage, 0, 0, 0, 0, 0, 0, 0, 0, 0}, le64(it.conv)}, nil)
	for _, s := range []string{it.from, it.fromName, "IPM.Note", msgID, it.preview, it.subject, it.subject} {
		run = append(run, u16z(s)...)
	}
	parts := [][]byte{it.head, Framing(it.id, typeMessage, 0, bytes.Join([][]byte{obj[18:], run, make([]byte, 8)}, nil))}
	var to []byte
	for _, r := range it.to {
		to = bytes.Join([][]byte{to, {0x80, 0, 0x0e, 0x7c, 0x01, 0x12, 0x00}, u16z(r[0]), u16z(r[1])}, nil)
	}
	parts = append(parts, Framing(it.id, typeTo, 0, to))
	if it.attachName != "" {
		att := make([]byte, 700)
		copy(att[100:], bytes.Join([][]byte{{0x0e, 0x7c, 0x01, 0x12, 0x00}, u16z(it.attachName)}, nil))
		binary.LittleEndian.PutUint32(att[attachSizeAt:], 1234)
		parts = append(parts, Framing(it.id, typeAttachment, 0, att[18:]))
	}
	return bytes.Join(parts, nil)
}

// bodyHead builds the body record of an item block: the size field, the
// UTF-16 class at +1893, then the fields.
func bodyHead(flags uint32, size int, fields ...[]byte) []byte {
	head := make([]byte, bodyClassAt)
	binary.LittleEndian.PutUint32(head[bodyMainAt:], flags<<24|uint32(size))
	return bytes.Join(append([][]byte{head, u16z("IPM.Note")}, append(fields, []byte{0, 0})...), nil)
}

// WriteSample writes a synthetic store to dir/HxStore.hxd and the EFMData
// file of message M2 to dir/Files/S0/<SampleDir>/EFMData. Message times are
// relative to now.
func WriteSample(dir string, now time.Time) error { return writeSample(dir, now, false) }

// WriteSampleTwoAccounts writes the sample store plus a second account
// (SampleRoot2) with one message, SampleM4.
func WriteSampleTwoAccounts(dir string, now time.Time) error { return writeSample(dir, now, true) }

func writeSample(dir string, now time.Time, twoAccounts bool) error {
	efmPath := "/Users/x/Library/Files/S0/" + SampleDir + "/EFMData/" + SampleEFMFile
	items := []sampleItem{
		{
			id: SampleM1, conv: SampleConv1, folder: SampleInbox, attach: true, high: true,
			from: "alice@example.com", fromName: "Alice Example", to: [][2]string{{SampleAddress, sampleName}},
			subject: "Quarterly numbers", preview: "The numbers are attached.", received: now.Add(-time.Hour),
			attachName: "report.pdf", head: bodyHead(0x80, len(SampleBody1), []byte(SampleBody1)),
		},
		{
			id: SampleM2, conv: SampleConv2, folder: SampleInbox, read: true,
			from: "carol@example.com", fromName: "Carol Third", to: [][2]string{{SampleAddress, sampleName}},
			subject: "Lunch plan", preview: "Lunch at noon on Friday?", received: now.Add(-48 * time.Hour),
			head: bodyHead(0x82, 62, []byte{0x05, 0}, u16z(efmPath)),
		},
		{
			id: SampleM3, conv: SampleConv1, folder: SampleSent, read: true,
			from: SampleAddress, fromName: sampleName, to: [][2]string{{"alice@example.com", "Alice Example"}},
			subject: "Quarterly numbers", preview: "Thanks, got it.", received: now.Add(-30 * time.Minute),
			head: bodyHead(0, 0),
		},
	}
	blocks := make([][]byte, 0, 7+len(items))
	blocks = append(blocks,
		Block(8, SampleRoot, rootPayload(SampleRoot, SampleInbox, SampleSent, SampleDeleted, SampleDir,
			folderRecord(SampleInbox, SampleProjects, SampleRoot, "Projects"))),
		Block(8, SampleInbox, Framing(SampleInbox, 0x1e3, 40, nil)),
		Block(8, SampleSent, Framing(SampleSent, 0x1e3, 40, nil)),
		Block(8, SampleDeleted, Framing(SampleDeleted, 0x1e3, 40, nil)),
		Block(8, SampleProjects, Framing(SampleProjects, 0x1e3, 40, nil)),
		Block(8, SampleConv1, Framing(SampleConv1, 0x2ec, 40, nil)),
		Block(8, SampleConv2, Framing(SampleConv2, 0x2ec, 40, nil)),
	)
	if twoAccounts {
		blocks = append(blocks,
			Block(8, SampleRoot2, rootPayload(SampleRoot2, SampleInbox2, sampleSent2, sampleDeleted2, "8")),
			Block(8, SampleInbox2, Framing(SampleInbox2, 0x1e3, 40, nil)),
			Block(8, sampleSent2, Framing(sampleSent2, 0x1e3, 40, nil)),
			Block(8, sampleDeleted2, Framing(sampleDeleted2, 0x1e3, 40, nil)),
			Block(8, SampleConv3, Framing(SampleConv3, 0x2ec, 40, nil)),
		)
		items = append(items, sampleItem{
			id: SampleM4, conv: SampleConv3, folder: SampleInbox2,
			from: "dave@example.com", fromName: "Dave Fourth", to: [][2]string{{"erin@example.com", "Erin Fifth"}},
			subject: "Numbers for the other team", preview: "Other account numbers.", received: now.Add(-2 * time.Hour),
			head: bodyHead(0, 0),
		})
	}
	direct := make([]bool, len(blocks), len(blocks)+len(items))
	for i := range direct {
		direct[i] = true
	}
	for _, it := range items {
		blocks = append(blocks, Block(8, it.id, it.payload()))
		direct = append(direct, it.id != SampleM2)
	}
	if err := os.WriteFile(filepath.Join(dir, "HxStore.hxd"), FileWithIndex(blocks, direct), 0o600); err != nil {
		return err
	}
	efm := filepath.Join(dir, "Files", "S0", SampleDir, "EFMData")
	if err := os.MkdirAll(efm, 0o700); err != nil {
		return err
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write([]byte(SampleBody2)); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(efm, SampleEFMFile), gz.Bytes(), 0o600)
}
