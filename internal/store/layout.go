// Package store reads mail records from the decoded blocks of an
// HxStore.hxd file (store format version 'i'): accounts, folders and
// message items. Offsets and rules come from docs/STORAGE.md; each one was
// checked on probe messages and on a whole real store.
package store

import (
	"bytes"
	"encoding/binary"
	"regexp"
	"strings"
	"unicode/utf16"
)

// Object types.
const (
	typeMessage    = 0xc7  // item (message) and conversation forms
	typeTo         = 0xcc  // To list of an item
	typeCc         = 0xcd  // Cc list
	typeBcc        = 0xce  // Bcc list
	typeAttachment = 0x168 // one attachment or inline image of an item
)

// Item-form offsets in a 0xc7 object (the conversation form is 10 bytes
// higher and has the account root at +32).
const (
	itemMinLen       = 1790
	convAccountAt    = 32
	itemStampAt      = 360 // 6 bytes
	itemReceivedAt   = 536 // .NET ticks
	itemSentAt       = 976 // .NET ticks
	itemFolderAt     = 1156
	itemConvAt       = 436
	itemReplyToAt    = 1044 // item ID of the answered message (replies)
	itemImportanceAt = 1456
	itemAttachAt     = 1758 // bit 0x40 = has attachments
	itemStatusAt     = 1762 // bit 0x02 = read
	attachSizeAt     = 670  // u32 in a 0x168 object
)

// Length table of the string run (u16 = UTF-16 bytes + 2; 0 or 2 = absent).
const (
	lenMessageID  = 1480
	lenSubject    = 1512
	lenPreview    = 1556
	lenSenderName = 1640
	lenSenderAddr = 1648
	lenTopic      = 1684
)

// idSpace tells object IDs apart from other u64 values. It is filled from
// the IDs of all framings of the store (see Load), so no fixed range is
// assumed.
type idSpace map[uint64]bool

// has reports whether v is a known object ID. A nil set accepts any
// non-zero value (used before all IDs are known).
func (s idSpace) has(v uint64) bool { return v != 0 && (s == nil || s[v]) }

// itemCopy is the compact metadata of one copy of an item.
type itemCopy struct {
	id, conv, folder uint64
	stamp            uint64
	status           byte
	block            int64
	frame            int // framing offset in the block payload
	direct           bool
}

// pickCurrent returns the copy with the highest stamp; on a stamp tie the
// copy in a direct index slot wins. confirmed: the chosen copy is in a
// direct slot. ambiguous: tied copies differ in status or folder and the
// index does not pick exactly one.
func pickCurrent(cs []itemCopy) (c itemCopy, confirmed, ambiguous bool) {
	top := cs[0]
	for _, x := range cs {
		if x.stamp > top.stamp {
			top = x
		}
	}
	var tied, direct []itemCopy
	for _, x := range cs {
		if x.stamp == top.stamp {
			tied = append(tied, x)
			if x.direct {
				direct = append(direct, x)
			}
		}
	}
	if len(direct) == 1 {
		return direct[0], true, false
	}
	for _, x := range tied {
		if x.status != top.status || x.folder != top.folder {
			ambiguous = true
		}
	}
	return top, len(direct) > 1, ambiguous
}

// utf16At decodes a NUL-terminated UTF-16LE string that starts at off and
// returns it with the offset after the terminator. ok is false when no
// terminator is found within max units.
func utf16At(b []byte, off, maxUnits int) (s string, next int, ok bool) {
	e := off
	for e+2 <= len(b) && (b[e] != 0 || b[e+1] != 0) {
		e += 2
		if (e-off)/2 > maxUnits {
			return "", 0, false
		}
	}
	if e+2 > len(b) {
		return "", 0, false
	}
	return decodeU16(b[off:e]), e + 2, true
}

func decodeU16(b []byte) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[2*i:])
	}
	return string(utf16.Decode(u))
}

func u16Size(s string) int { return 2*len(utf16.Encode([]rune(s))) + 2 }

// itemText holds the strings of an item. Empty means absent or not found.
type itemText struct {
	SenderAddr, SenderName, Class, MessageID, Preview, Subject string
}

// readItemText reads the string run that follows the anchor
// [item ID][00 00 c7 00 + 8 zero bytes][conversation ID], using the length
// table: the class is the first string that starts with "IPM." or
// "REPORT."; every other field is the first unused string whose size
// matches its table entry (sender before the class; Message-ID and preview
// after it; subject after it first, then before it). When no string
// matches the subject entry, the conversation topic is used.
func readItemText(obj []byte, id, conv uint64) (itemText, bool) {
	var t itemText
	anchor := binary.LittleEndian.AppendUint64(nil, id)
	anchor = append(anchor, 0, 0, typeMessage, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	anchor = binary.LittleEndian.AppendUint64(anchor, conv)
	p := bytes.Index(obj, anchor)
	if p < 0 {
		return t, false
	}
	var run []string
	for p += len(anchor); len(run) < 32; {
		s, next, ok := utf16At(obj, p, 32768)
		if !ok {
			break
		}
		run = append(run, s)
		p = next
	}
	classAt := -1
	for k, s := range run {
		if strings.HasPrefix(s, "IPM.") || strings.HasPrefix(s, "REPORT.") {
			classAt = k
			break
		}
	}
	if classAt < 0 {
		return t, false
	}
	t.Class = run[classAt]
	u16 := func(off int) int { return int(binary.LittleEndian.Uint16(obj[off:])) }
	used := map[int]bool{classAt: true}
	find := func(from, to, length int, accept func(string) bool) string {
		if length <= 2 {
			return ""
		}
		step := 1
		if from > to {
			step = -1
		}
		for k := from; k != to; k += step {
			if k >= 0 && k < len(run) && !used[k] && u16Size(run[k]) == length && (accept == nil || accept(run[k])) {
				used[k] = true
				return run[k]
			}
		}
		return ""
	}
	either := func(length int) string {
		if s := find(classAt+1, len(run), length, nil); s != "" {
			return s
		}
		return find(classAt-1, -1, length, nil)
	}
	t.SenderAddr = find(classAt-1, -1, u16(lenSenderAddr), func(s string) bool { return strings.Contains(s, "@") })
	t.SenderName = find(classAt-1, -1, u16(lenSenderName), nil)
	t.MessageID = find(classAt+1, len(run), u16(lenMessageID), func(s string) bool {
		return strings.HasPrefix(s, "<") && strings.HasSuffix(s, ">")
	})
	t.Preview = find(classAt+1, len(run), u16(lenPreview), nil)
	t.Subject = either(u16(lenSubject))
	if topic := either(u16(lenTopic)); t.Subject == "" {
		t.Subject = topic
	}
	return t, true
}

// Recipient is one entry of a To, Cc or Bcc list.
type Recipient struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"address"`
}

// stringStarts returns the offsets where a NUL-terminated UTF-16 string
// may start: right after a 0x00 byte that follows a non-zero byte (the end
// of a sort key or of a length field) or right after a 0x0000 terminator.
func stringStarts(b []byte) []int {
	var out []int
	for i := 2; i < len(b)-1; i++ {
		if b[i-1] == 0 && (b[i-2] != 0 || i >= 3 && b[i-3] == 0) && b[i] != 0 {
			out = append(out, i)
		}
	}
	return out
}

// readRecipients reads a To/Cc/Bcc object: every NUL-terminated UTF-16
// string with "@" and no space is an address; the string right after its
// terminator is the display name. Repeated addresses count once.
func readRecipients(obj []byte) []Recipient {
	var out []Recipient
	seen := map[string]int{}
	for i := 0; i+4 <= len(obj); i++ {
		if obj[i] != '@' || obj[i+1] != 0 {
			continue
		}
		// Walk back to the start of the string.
		s := i
		for s-2 >= 0 && binary.LittleEndian.Uint16(obj[s-2:]) >= 0x20 && i-s < 512 {
			s -= 2
		}
		addr, next, ok := utf16At(obj, s, 512)
		if !ok || strings.ContainsAny(addr, " \t") || !strings.Contains(addr, "@") {
			continue
		}
		i = next - 1
		name, _, _ := utf16At(obj, next, 512)
		key := strings.ToLower(addr)
		if k, dup := seen[key]; dup {
			if out[k].Name == "" {
				out[k].Name = name
			}
			continue
		}
		seen[key] = len(out)
		out = append(out, Recipient{Name: name, Address: addr})
	}
	return out
}

// Attachment is one named attachment or inline image of an item.
type Attachment struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

var fileNameRe = regexp.MustCompile(`^[^/\\:*?"<>|@]{1,200}\.[A-Za-z0-9]{1,8}$`)

// readAttachment reads a 0x168 object: the first string that looks like a
// file name and starts right after a sort-key terminator, and the u32 size
// at +670.
func readAttachment(obj []byte) (Attachment, bool) {
	for _, i := range stringStarts(obj) {
		if obj[i-2] == 0 {
			continue // after a terminator, not after a sort key
		}
		s, _, ok := utf16At(obj, i, 260)
		if ok && fileNameRe.MatchString(s) {
			a := Attachment{Name: s}
			if len(obj) >= attachSizeAt+4 {
				a.Size = int64(binary.LittleEndian.Uint32(obj[attachSizeAt:]))
			}
			return a, true
		}
	}
	return Attachment{}, false
}
