package main

import (
	"encoding/binary"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

// Everything printed by hxdiscover goes into a terminal transcript, and the
// store holds real mail. Text is therefore printed only as a redacted shape,
// unless it contains the synthetic probe marker or is on a small list of
// schema words.

var (
	emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[A-Za-z]{2,}$`)
	guidRe  = regexp.MustCompile(`^[{<]?[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}[}>]?$`)
	hexRe   = regexp.MustCompile(`^[0-9A-Fa-f]{16,}$`)
	b64Re   = regexp.MustCompile(`^[A-Za-z0-9+/=_-]{24,}$`)
	msgIDRe = regexp.MustCompile(`^<[^<>\s]+@[^<>\s]+>$`)
)

var schemaWords = map[string]bool{
	"Anonymous": true, "SMTP": true, "EX": true, "Inbox": true, "Drafts": true,
	"Sent Items": true, "Deleted Items": true, "Junk Email": true, "Archive": true,
	"Outbox": true, "Conversation History": true, "Notes": true, "Calendar": true,
	"Contacts": true, "Tasks": true,
}

type redactor struct {
	marker string // lower case; empty disables the exception
	now    time.Time
}

// show returns s when it is safe to print, otherwise its shape.
func (r redactor) show(s string) string {
	if r.marker != "" && strings.Contains(strings.ToLower(s), r.marker) {
		return fmt.Sprintf("%q", s)
	}
	if safeItemClass(s) || schemaWords[s] {
		return fmt.Sprintf("%q", s)
	}
	return shape(s)
}

// classSegments are the only words allowed in a printed item class. The
// store often writes the next field directly after the class with no
// separator, so a class match can carry a person's name; only classes made
// entirely of these segments are printed.
var classSegments = map[string]bool{
	"IPM": true, "IPF": true, "Note": true, "Schedule": true, "Meeting": true, "Request": true,
	"Resp": true, "Pos": true, "Neg": true, "Tent": true, "Canceled": true, "Notification": true,
	"Forward": true, "Rules": true, "OofTemplate": true, "Microsoft": true, "NDR": true,
	"Voicemail": true, "UM": true, "CA": true, "Appointment": true, "Contact": true, "Task": true,
	"StickyNote": true, "Post": true, "DistList": true, "Activity": true, "Sharing": true,
	"Outlook": true, "Recall": true, "Configuration": true, "Imap": true, "Document": true,
	"SMIME": true, "MultipartSigned": true, "Recurrence": true, "Exception": true,
}

func safeItemClass(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) < 2 || (parts[0] != "IPM" && parts[0] != "IPF") {
		return false
	}
	for _, p := range parts {
		if !classSegments[p] {
			return false
		}
	}
	return true
}

func shape(s string) string {
	kind := "text"
	t := strings.TrimSpace(s)
	switch {
	case msgIDRe.MatchString(t):
		kind = "message_id"
	case emailRe.MatchString(t):
		kind = "email"
	case guidRe.MatchString(t):
		kind = "guid"
	case hexRe.MatchString(t):
		kind = "hex"
	case strings.HasPrefix(t, "http://") || strings.HasPrefix(t, "https://"):
		kind = "url"
	case strings.Contains(strings.ToLower(t), "<html") || strings.Contains(strings.ToLower(t), "<div"):
		kind = "html"
	case b64Re.MatchString(t):
		kind = "base64"
	case isDigits(t):
		kind = "number"
	}
	return fmt.Sprintf("<len=%d words=%d at=%t kind=%s>", utf8.RuneCountInString(s), len(strings.Fields(s)), strings.Contains(s, "@"), kind)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// tickWindow bounds the .NET tick values that are treated as dates.
const (
	ticksUnixEpoch = 621355968000000000
	ticksMin       = 635556672000000000 // 2015-01-01
	ticksMax       = 639343584000000000 // 2027-01-01
)

// ticksToTime converts .NET ticks (100 ns since 0001-01-01 UTC) to time.
func ticksToTime(t uint64) time.Time {
	d := int64(t) - ticksUnixEpoch
	return time.Unix(d/10_000_000, (d%10_000_000)*100).UTC()
}

// tickNote describes a plausible .NET tick at b[0:8]. Dates within the last
// three days are printed (they belong to the synthetic probe messages);
// older dates are only marked as present.
func (r redactor) tickNote(b []byte) (string, bool) {
	if len(b) < 8 {
		return "", false
	}
	v := binary.LittleEndian.Uint64(b)
	if v < ticksMin || v > ticksMax {
		return "", false
	}
	t := ticksToTime(v)
	if r.now.Sub(t) < 72*time.Hour && t.Sub(r.now) < time.Hour {
		return "tick=" + t.Format(time.RFC3339), true
	}
	return "tick(old)", true
}

// Token kinds.
const (
	tokU16 = "u16"
	tokU8  = "u8"
)

// token is one piece of a tokenized byte span.
type token struct {
	off  int
	kind string // u16, u8, bin
	text string
	raw  []byte
}

// tokenize splits b into UTF-16LE text runs (>= 3 chars), UTF-8 text runs
// (>= 4 bytes) and binary gaps.
func tokenize(b []byte) []token {
	var out []token
	binStart := 0
	flush := func(end int) {
		if end > binStart {
			out = append(out, token{off: binStart, kind: "bin", raw: b[binStart:end]})
		}
	}
	for i := 0; i < len(b); {
		if n, s := u16Run(b[i:]); n > 0 {
			flush(i)
			out = append(out, token{off: i, kind: tokU16, text: s})
			i += n
			binStart = i
			continue
		}
		if n := u8Run(b[i:]); n > 0 {
			flush(i)
			out = append(out, token{off: i, kind: tokU8, text: string(b[i : i+n])})
			i += n
			binStart = i
			continue
		}
		i++
	}
	flush(len(b))
	return out
}

func textRune(r rune) bool {
	if r >= 0x20 && r < 0x7f {
		return true
	}
	return r >= 0xa0 && r < 0x2000 && (unicode.IsLetter(r) || unicode.IsPunct(r) || unicode.IsSpace(r))
}

// u16Run returns the byte length and text of a UTF-16LE run at the start
// of b, or 0. At least half of the characters must be ASCII, which rejects
// most binary that happens to decode to letters.
func u16Run(b []byte) (int, string) {
	var units []uint16
	ascii := 0
	for i := 0; i+1 < len(b); i += 2 {
		u := binary.LittleEndian.Uint16(b[i:])
		if !textRune(rune(u)) {
			break
		}
		if u < 0x80 {
			ascii++
		}
		units = append(units, u)
	}
	if len(units) < 3 || ascii*2 < len(units) {
		return 0, ""
	}
	return len(units) * 2, string(utf16.Decode(units))
}

// u8Run returns the length of a printable UTF-8 run at the start of b, or 0.
func u8Run(b []byte) int {
	n := 0
	for n < len(b) {
		r, size := utf8.DecodeRune(b[n:])
		if r == utf8.RuneError || !textRune(r) && r != '\n' && r != '\r' && r != '\t' {
			break
		}
		n += size
	}
	if n < 4 {
		return 0
	}
	return n
}
