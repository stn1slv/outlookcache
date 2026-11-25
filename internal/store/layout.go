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
	"unicode"
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

// pickCurrent returns the copy with the highest stamp; among copies with
// that stamp, a copy in a direct index slot wins. confirmed: the returned
// copy is in a direct slot. ambiguous: the candidates the choice was made
// from (the direct copies when there are any, else all tied copies)
// differ in status or folder.
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
	pool := tied
	if len(direct) > 0 {
		pool, top, confirmed = direct, direct[0], true
	}
	for _, x := range pool {
		if x.status != top.status || x.folder != top.folder {
			ambiguous = true
		}
	}
	return top, confirmed, ambiguous
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

// ItemText holds the strings of an item. Empty means absent or not found.
type ItemText struct {
	SenderAddr, SenderName, Class, MessageID, Preview, Subject string
}

// ParseItemText reads the string run of an item object (item form) that
// follows the anchor [item ID][00 00 c7 00 + 8 zero bytes][conversation
// ID], where conv is the raw u64 at +436. It uses the length table: the
// class is the first string that starts with "IPM." or "REPORT."; every
// other field is the nearest unused string whose size matches its entry
// (sender before the class; Message-ID and preview after it; subject and
// topic after it first, then before it). Without a subject, the topic is
// used.
func ParseItemText(obj []byte, id, conv uint64) (ItemText, bool) {
	var t ItemText
	if len(obj) < itemMinLen {
		return t, false
	}
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
	// The preview comes before the subject after the class (positions +1
	// to +4 against +3 and later on the whole store), so it is taken first;
	// when both have the same size the earlier string is the preview.
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

var emailRe = regexp.MustCompile(`^[A-Za-z0-9._%+'=-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*$`)

// printable reports a non-empty string made only of printable characters.
func printable(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < 0x20 || !unicode.IsPrint(r) && r != ' ' {
			return false
		}
	}
	return true
}

// ParseRecipients reads a To/Cc/Bcc object. An entry is a sort key, then
// the address and the display name as adjacent NUL-terminated UTF-16LE
// strings. An address starts after a UTF-16 unit below 0x20 and must look
// like an e-mail address; the name is the string right after its
// terminator when it is printable. Repeated addresses count once.
func ParseRecipients(obj []byte) []Recipient {
	var out []Recipient
	seen := map[string]int{}
	for i := 0; i+4 <= len(obj); i++ {
		if obj[i] != '@' || obj[i+1] != 0 {
			continue
		}
		s := i
		for s-2 >= 0 && binary.LittleEndian.Uint16(obj[s-2:]) >= 0x20 && i-s < 512 {
			s -= 2
		}
		addr, next, ok := utf16At(obj, s, 512)
		if !ok || !emailRe.MatchString(addr) {
			continue
		}
		i = next - 1
		name, after, ok := utf16At(obj, next, 512)
		if ok && printable(name) {
			i = after - 1 // a name that is itself an address is not another entry
		} else {
			name = ""
		}
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

// fileNameRe is the shape of an attachment file name: no control
// characters, no path separators and an extension. macOS allows ':' and
// other characters that Windows does not, so they are accepted.
var fileNameRe = regexp.MustCompile(`^[^\x00-\x1f/\\]{1,255}\.[A-Za-z0-9]{1,10}$`)

// ParseAttachment reads a 0x168 object: the file name is the first string
// that starts right after a UTF-16 unit from 0x01 to 0x1f (the end of a
// sort key) and has the shape of a file name; the size is the u32 at +670.
// A string that only ends in a file name, such as a cache path, is not
// taken. A file name without an extension is not found (UNVERIFIED whether
// Outlook stores such names differently).
func ParseAttachment(obj []byte) (Attachment, bool) {
	for i := 2; i+2 <= len(obj); i++ {
		if obj[i-1] != 0 || obj[i-2] == 0 || obj[i-2] >= 0x20 || obj[i] == 0 && obj[i+1] == 0 {
			continue
		}
		s, _, ok := utf16At(obj, i, 260)
		if !ok || !printable(s) || !fileNameRe.MatchString(s) {
			continue
		}
		a := Attachment{Name: s}
		if len(obj) >= attachSizeAt+4 {
			a.Size = int64(binary.LittleEndian.Uint32(obj[attachSizeAt:]))
		}
		return a, true
	}
	return Attachment{}, false
}
