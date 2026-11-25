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
	bodyMinClassAt = 214
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
	if cls < bodyMinClassAt {
		return BodyRef{Kind: BodyUnparsed}
	}
	main := binary.LittleEndian.Uint32(hd[cls+bodyMainAt:])
	n := int(main & bodySizeMask)
	switch main >> 24 {
	case 0:
		return BodyRef{Kind: BodyNone}
	case bodyFlagFile:
		return externalBody(hd)
	case bodyFlagInline:
		if html := inlineBody(hd, cls, n); html != nil {
			return BodyRef{Kind: BodyInline, HTML: html}
		}
	}
	return BodyRef{Kind: BodyUnparsed}
}

// classAt returns the offset of the first UTF-16 item class ("IPM." or
// "REPORT.") in the head, or -1.
func classAt(hd []byte) int {
	at := -1
	for _, prefix := range []string{"IPM.", "REPORT."} {
		if k := bytes.Index(hd, encodeU16(prefix)); k >= 0 && (at < 0 || k < at) {
			at = k
		}
	}
	return at
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
	low := asciiLower(hd)
	for o := r; ; {
		k := bytes.Index(low[o:], htmlEnd)
		if k < 0 {
			return nil
		}
		e := o + k + len(htmlEnd)
		o = e
		for t := 0; t <= 4; t++ { // trailing white space inside the size
			if p := e + t - n; p >= r && p+n <= len(hd) && isHTMLDoc(hd[p:p+n]) {
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
// a tag and, without trailing white space, ends with </html>.
func isHTMLDoc(b []byte) bool {
	t := bytes.TrimSpace(b)
	return len(t) > len(htmlEnd) && t[0] == '<' && bytes.Equal(asciiLower(t[len(t)-len(htmlEnd):]), htmlEnd) && utf8.Valid(t)
}

// externalBody reads the EFMData paths stored in the head. Only the folder
// number and the file name are kept; both are checked against a strict
// pattern.
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
// block of the chosen item copy again; the store keeps the snapshot bytes
// for this.
func (s *Store) Body(id string) (BodyRef, error) {
	v, err := strconv.ParseUint(id, 16, 64)
	if err != nil {
		return BodyRef{}, fmt.Errorf("%w: %q", ErrUnknownMessage, id)
	}
	block, ok := s.itemBlock[v]
	if !ok {
		return BodyRef{}, fmt.Errorf("%w: %q", ErrUnknownMessage, id)
	}
	b, err := hxstore.DecodeAt(s.data, block)
	if err != nil {
		return BodyRef{}, err
	}
	return ParseBody(b.Payload), nil
}
