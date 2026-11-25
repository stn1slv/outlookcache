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
// unless it is synthetic probe text (see showMarker) or a schema word. Hex
// output masks every span that decodes as text (see maskHex).
//
// Known limit: scripts above U+2000 (CJK, Hangul) are not detected as text,
// because 8-byte object IDs often decode to CJK code points.

// markerPrefix is the start of every synthetic probe text. Markers that do
// not start with it are refused, so an ordinary word cannot turn the tool
// into a content printer.
const markerPrefix = "oc-probe"

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
	"Contacts": true, "Tasks": true, "Sent": true, "Deleted": true, "Junk": true, "Sync Issues": true,
	"RSS Feeds": true, "Scheduled": true, "Snoozed": true, "Conflicts": true, "Local Failures": true,
	"Server Failures": true, "Clutter": true, "Files": true, "Journal": true, "Yammer Root": true,
	"Social Activity Notifications": true, "Recoverable Items": true, "Junk E-mail": true, "Sent Mail": true,
	"Trash": true, "Spam": true, "Outbox ": true, "Templates": true, "Quick Step Settings": true,
	"Conversation Action Settings": true, "ExternalContacts": true, "PersonMetadata": true,
}

type redactor struct {
	marker    string // lower case; empty disables the exception
	now       time.Time
	showTicks bool // print exact recent tick times (they can belong to real mail)
}

// checkMarker validates and lower-cases a marker.
func checkMarker(m string) (string, error) {
	m = strings.ToLower(m)
	if !strings.HasPrefix(m, markerPrefix) {
		return "", fmt.Errorf("marker must start with %q", markerPrefix)
	}
	return m, nil
}

// show returns s when it is safe to print, otherwise its shape.
func (r redactor) show(s string) string {
	if r.marker != "" {
		if i := strings.Index(strings.ToLower(s), r.marker); i >= 0 {
			return r.showMarker(s, i)
		}
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

// syntheticRune reports the characters used in the probe texts. Real
// fields glued to a probe (names, addresses, HTML) contain an upper-case
// letter, '@' or '<' early, which ends the printed part.
func syntheticRune(c rune) bool {
	return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.ContainsRune(" -.:,_", c)
}

// showMarker prints the synthetic text that starts at the marker, up to the
// first character outside syntheticRune and at most 80 characters. Text
// before the marker and after that point is only shaped.
func (r redactor) showMarker(s string, i int) string {
	tail := s[i:]
	if j := strings.IndexFunc(tail, func(c rune) bool { return !syntheticRune(c) }); j >= 0 {
		tail = tail[:j]
	}
	if n := []rune(tail); len(n) > 80 {
		tail = string(n[:80]) + "..."
	}
	out := fmt.Sprintf("%q", tail)
	if i > 0 {
		out = shape(s[:i]) + "+" + out
	}
	if rest := s[i+len(tail):]; rest != "" {
		out += "+" + shape(rest)
	}
	return out
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

// Bounds of the .NET tick values that are treated as dates. The upper bound
// is one year after r.now.
const (
	ticksUnixEpoch = 621355968000000000
	ticksMin       = 635556672000000000 // 2015-01-01
	ticksPerSecond = 10_000_000
)

// ticksToTime converts .NET ticks (100 ns since 0001-01-01 UTC) to time.
func ticksToTime(t uint64) time.Time {
	d := int64(t) - ticksUnixEpoch
	return time.Unix(d/ticksPerSecond, (d%ticksPerSecond)*100).UTC()
}

// tickNote describes a plausible .NET tick at b[0:8]. Exact times are
// printed only with showTicks and only for the last three days, because a
// window around a probe can include real mail.
func (r redactor) tickNote(b []byte) (string, bool) {
	if len(b) < 8 {
		return "", false
	}
	v := binary.LittleEndian.Uint64(b)
	ticksMax := uint64(ticksUnixEpoch + r.now.AddDate(1, 0, 0).Unix()*ticksPerSecond)
	if v < ticksMin || v > ticksMax {
		return "", false
	}
	t := ticksToTime(v)
	if r.now.Sub(t) >= 72*time.Hour {
		return "tick(old)", true
	}
	if r.showTicks {
		return "tick=" + t.Format(time.RFC3339), true
	}
	return "tick(recent)", true
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

// textRune reports characters that can be part of human text in any script
// below U+2000, plus typographic punctuation (U+2010 to U+206F).
func textRune(r rune) bool {
	if r >= 0x20 && r < 0x7f || r == '\n' || r == '\r' || r == '\t' {
		return true
	}
	if r >= 0x2010 && r <= 0x206f {
		return true
	}
	return r >= 0xa0 && r < 0x2000 && (unicode.IsLetter(r) || unicode.IsMark(r) || unicode.IsPunct(r) ||
		unicode.IsSpace(r) || unicode.IsDigit(r) || unicode.IsSymbol(r))
}

func letterRune(r rune) bool { return r < 0x2000 && unicode.IsLetter(r) }

// u16Run returns the byte length and text of a UTF-16LE run at the start
// of b, or 0. A run needs 3 text characters, 2 of them letters.
func u16Run(b []byte) (int, string) {
	var units []uint16
	letters := 0
	for i := 0; i+1 < len(b); i += 2 {
		u := binary.LittleEndian.Uint16(b[i:])
		if !textRune(rune(u)) {
			break
		}
		if letterRune(rune(u)) {
			letters++
		}
		units = append(units, u)
	}
	if len(units) < 3 || letters < 2 {
		return 0, ""
	}
	return len(units) * 2, string(utf16.Decode(units))
}

// u8Run returns the length of a UTF-8 text run at the start of b, or 0. A
// run needs 4 bytes and 2 letters.
func u8Run(b []byte) int {
	n, letters := 0, 0
	for n < len(b) {
		r, size := utf8.DecodeRune(b[n:])
		if r == utf8.RuneError || !textRune(r) {
			break
		}
		if letterRune(r) {
			letters++
		}
		n += size
	}
	if n < 4 || letters < 2 {
		return 0
	}
	return n
}

// textMask marks every byte that belongs to a span that decodes as text:
// UTF-16LE (both alignments) with at least 2 characters and 2 letters, or
// UTF-8 with at least 3 bytes and 2 letters. It errs toward hiding binary.
func textMask(b []byte) []bool {
	mask := make([]bool, len(b))
	for align := 0; align < 2; align++ {
		start, count, letters := align, 0, 0
		flush := func(end int) {
			if count >= 2 && letters >= 2 {
				for k := start; k < end; k++ {
					mask[k] = true
				}
			}
		}
		for i := align; i+1 < len(b); i += 2 {
			r := rune(binary.LittleEndian.Uint16(b[i:]))
			if textRune(r) {
				if count == 0 {
					start = i
				}
				count++
				if letterRune(r) {
					letters++
				}
				continue
			}
			flush(i)
			count, letters = 0, 0
		}
		flush(len(b) - (len(b)-align)%2)
	}
	for i := 0; i < len(b); {
		j, letters := i, 0
		for j < len(b) {
			r, size := utf8.DecodeRune(b[j:])
			if r == utf8.RuneError || !textRune(r) {
				break
			}
			if letterRune(r) {
				letters++
			}
			j += size
		}
		if j-i >= 3 && letters >= 2 {
			for k := i; k < j; k++ {
				mask[k] = true
			}
		}
		if j == i {
			j++
		}
		i = j
	}
	return mask
}

// maskHex prints b as hex with every text span replaced by [txt:N].
func maskHex(b []byte) string {
	return renderHex(b, textMask(b))
}

func renderHex(b []byte, mask []bool) string {
	var sb strings.Builder
	for i := 0; i < len(b); {
		if !mask[i] {
			_, _ = fmt.Fprintf(&sb, "%02x", b[i])
			i++
			continue
		}
		j := i
		for j < len(b) && mask[j] {
			j++
		}
		_, _ = fmt.Fprintf(&sb, "[txt:%d]", j-i)
		i = j
	}
	return sb.String()
}
