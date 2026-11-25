package store

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"regexp"
	"strconv"
	"unicode/utf8"

	"github.com/stn1slv/outlookcache/internal/hxstore"
)

// Body storage kinds.
const (
	BodyInline   = "inline"   // HTML in the item's block
	BodyExternal = "efmdata"  // HTML in a Files/S0/<dir>/EFMData/<n>.dat file
	BodyNone     = "none"     // no body stored (preview only)
	BodyUnparsed = "unparsed" // the body record has a layout the reader does not know
)

// Body record of an item: the unframed head of the item's block, before
// the first object framing. Offsets are relative to the UTF-16 item class
// in the head (at +1893 of the payload in most blocks).
const (
	bodyClassFrom  = 1893 // the class is never earlier (whole store)
	bodyPrefixAt   = -214 // u32 size of a field that can precede the body
	bodyMainAt     = -210 // u32: flags in the high byte, body size below
	bodySecondAt   = -202 // u32: flags and size of a second HTML field
	bodyFlagInline = 0x80
	bodyFlagFile   = 0x82
	bodySizeMask   = 0xffffff
)

// BodyRef is where a message body is stored.
type BodyRef struct {
	Kind string
	HTML []byte   // BodyInline: the HTML document
	Dir  string   // BodyExternal: the account folder number under Files/S0
	File []string // BodyExternal: EFMData file names, best first
}

var (
	efmPathRe = regexp.MustCompile(`/Files/S0/([0-9]{1,9})/EFMData/([0-9]{1,12}\.dat)$`)
	htmlEnd   = []byte("</html>")
)

// ParseBody reads the body record from the decoded payload of an item's
// block. It never fails: a layout it does not know gives BodyUnparsed.
func ParseBody(payload []byte) BodyRef {
	head := len(payload)
	hxstore.ForEachFraming(payload, func(f hxstore.Framing) { head = min(head, f.Offset) })
	hd := payload[:head]
	cls := classAt(hd)
	if cls < 0 {
		return BodyRef{Kind: BodyUnparsed}
	}
	main := binary.LittleEndian.Uint32(hd[cls+bodyMainAt:])
	n := int(main & bodySizeMask)
	switch main >> 24 {
	case 0:
		if n == 0 {
			return BodyRef{Kind: BodyNone}
		}
	case bodyFlagFile:
		return externalBody(hd[cls:])
	case bodyFlagInline:
		if html := inlineBody(hd, cls, n); html != nil {
			return BodyRef{Kind: BodyInline, HTML: html}
		}
	}
	return BodyRef{Kind: BodyUnparsed}
}

// classAt returns the offset of the first UTF-16 item class ("IPM." or
// "REPORT.") at or after bodyClassFrom in the head, or -1. Text before
// that offset is never taken as the class.
func classAt(hd []byte) int {
	tail := hd[min(len(hd), bodyClassFrom):]
	at := -1
	for _, prefix := range []string{"IPM.", "REPORT."} {
		if k := bytes.Index(tail, encodeU16(prefix)); k >= 0 && (at < 0 || k < at) {
			at = k
		}
	}
	if at < 0 {
		return -1
	}
	return bodyClassFrom + at
}

// inlineBody finds the n-byte HTML document that follows the class. The
// fields after the class have no terminators, and another HTML field can
// come before the body, so the candidates are: the start of the byte run,
// the start plus the sizes of the fields at bodyPrefixAt and bodySecondAt,
// and every window that ends at a "</html>". The first candidate that is a
// whole HTML document wins.
func inlineBody(hd []byte, cls, n int) []byte {
	if n == 0 {
		return nil
	}
	r := runStart(hd, cls)
	if r < 0 {
		return nil
	}
	size := func(at int) int {
		v := binary.LittleEndian.Uint32(hd[cls+at:])
		if f := v >> 24; f != 0 && f != bodyFlagInline {
			return 0
		}
		return int(v & bodySizeMask)
	}
	s0, s2 := size(bodyPrefixAt), size(bodySecondAt)
	for _, p := range []int{r, r + s0, r + s2, r + s0 + s2} {
		if p+n <= len(hd) && isHTMLDoc(hd[p:p+n]) {
			return hd[p : p+n]
		}
	}
	// Fallback: a window that ends at a "</html>" and starts a document.
	low := asciiLower(hd)
	for o := r; ; {
		k := bytes.Index(low[o:], htmlEnd)
		if k < 0 {
			return nil
		}
		e := o + k + len(htmlEnd)
		o = e
		for t := 0; t <= 4; t++ { // trailing white space inside the size
			if p := e + t - n; p >= r && p+n <= len(hd) && docStart(low[p:]) && isHTMLDoc(hd[p:p+n]) {
				return hd[p : p+n]
			}
		}
	}
}

// runStart returns the first offset after the class where a UTF-8 field
// starts: a '<' after a zero byte, followed by a byte that is not zero (so
// not UTF-16 text).
func runStart(hd []byte, cls int) int {
	for p := cls; p+1 < len(hd); p++ {
		if hd[p] == '<' && hd[p-1] == 0 && hd[p+1] != 0 {
			return p
		}
	}
	return -1
}

// isHTMLDoc reports a complete HTML document in valid UTF-8: it starts with
// '<' and, without trailing white space, ends with </html>.
func isHTMLDoc(b []byte) bool {
	t := bytes.TrimRight(b, " \t\r\n")
	return len(t) > len(htmlEnd) && t[0] == '<' && bytes.Equal(asciiLower(t[len(t)-len(htmlEnd):]), htmlEnd) && utf8.Valid(t)
}

// docStart reports lower-case bytes that start an HTML document.
func docStart(low []byte) bool {
	return bytes.HasPrefix(low, []byte("<html")) || bytes.HasPrefix(low, []byte("<!doctype"))
}

// externalBody reads the EFMData paths stored after the class. Only the
// folder number and the file name are kept; both are checked against a
// strict pattern, and paths in another folder than the first are ignored.
func externalBody(hd []byte) BodyRef {
	ref := BodyRef{Kind: BodyExternal}
	seen := map[string]bool{}
	nd := encodeU16("/EFMData/")
	for off := 0; ; {
		k := bytes.Index(hd[off:], nd)
		if k < 0 {
			break
		}
		at := off + k
		off = at + len(nd)
		s := at
		for s-2 >= 0 && binary.LittleEndian.Uint16(hd[s-2:]) >= 0x20 && at-s < 4096 {
			s -= 2
		}
		path, _, ok := utf16At(hd, s, 4096)
		if !ok {
			continue
		}
		m := efmPathRe.FindStringSubmatch(path)
		if m == nil || ref.Dir != "" && m[1] != ref.Dir || seen[m[2]] {
			continue
		}
		ref.Dir = m[1]
		seen[m[2]] = true
		ref.File = append(ref.File, m[2])
	}
	if len(ref.File) == 0 {
		return BodyRef{Kind: BodyUnparsed}
	}
	return ref
}

// asciiLower lowers ASCII letters only, so offsets stay equal to the input.
func asciiLower(b []byte) []byte {
	out := make([]byte, len(b))
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return out
}

// Body returns where the body of message id is stored. It decodes the
// item's own block again (the store keeps the snapshot bytes for this) and
// returns BodyUnparsed when the message has no block keyed by its ID.
func (s *Store) Body(id string) (BodyRef, error) {
	v, err := strconv.ParseUint(id, 16, 64)
	if err != nil {
		return BodyRef{}, fmt.Errorf("%w: %q", ErrUnknownMessage, id)
	}
	known := false
	for i := range s.Messages {
		if s.Messages[i].ID == id {
			known = true
			break
		}
	}
	if !known {
		return BodyRef{}, fmt.Errorf("%w: %q", ErrUnknownMessage, id)
	}
	block, ok := s.bodyBlock[v]
	if !ok {
		return BodyRef{Kind: BodyUnparsed}, nil
	}
	b, err := hxstore.DecodeAt(s.data, block)
	if err != nil {
		return BodyRef{}, err
	}
	if b.Key != v { // never return another item's body record
		return BodyRef{Kind: BodyUnparsed}, nil
	}
	ref := ParseBody(b.Payload)
	ref.HTML = bytes.Clone(ref.HTML) // do not keep the whole block alive
	return ref, nil
}
