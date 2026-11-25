package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/stn1slv/outlookcache/internal/hxstore"
	"github.com/stn1slv/outlookcache/internal/store"
)

const (
	headBody1At = 1683 // u32: flags (high byte) and size of the first body copy
	headBody2At = 1691 // u32: the same for a second copy that follows it
	headClassAt = 1893 // UTF-16 item class
)

// findBody returns the start of a body of size n in the head: the first
// offset at or after the class where a '<' or a printable ASCII byte that
// is not UTF-16 starts and the next n bytes fit in the head.
func findBody(hd []byte, n int) int {
	for p := headClassAt; p+n <= len(hd); p++ {
		if hd[p] == '<' && p+1 < len(hd) && hd[p+1] != 0 && (p == 0 || hd[p-1] == 0) {
			return p
		}
	}
	return -1
}

// bodyRule tests the head size-field rule on every current message (the
// item's own block in a direct slot, else the last version) and prints
// counts only.
func bodyRule(data []byte, w io.Writer) error {
	s, err := store.Load(data, "HxStore.hxd")
	if err != nil {
		return err
	}
	ix, err := hxstore.ReadIndex(data)
	if err != nil {
		return err
	}
	want := map[uint64]bool{}
	for _, m := range s.Messages {
		var id uint64
		_, _ = fmt.Sscanf(m.ID, "%x", &id)
		want[id] = true
	}
	type pick struct {
		payload []byte
		direct  bool
	}
	blocks := map[uint64]pick{}
	if _, _, err := hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		if !want[b.Key] {
			return nil
		}
		d := ix.Direct(b.Offset)
		if old, ok := blocks[b.Key]; !ok || d || !old.direct {
			blocks[b.Key] = pick{b.Payload, d}
		}
		return nil
	}); err != nil {
		return err
	}
	c := map[string]int{}
	efm := encodeU16("/EFMData/")
	for _, p := range blocks {
		pl := p.payload
		head := len(pl)
		hxstore.ForEachFraming(pl, func(f hxstore.Framing) { head = min(head, f.Offset) })
		hd := pl[:head]
		if len(hd) < headClassAt+4 {
			c["head shorter than the class offset"]++
			continue
		}
		cls := -1
		for _, nd := range [][]byte{encodeU16("IPM."), encodeU16("REPORT.")} {
			if k := bytes.Index(hd, nd); k >= 0 && (cls < 0 || k < cls) {
				cls = k
			}
		}
		if cls < headClassAt-headBody1At {
			c["no class in head"]++
			continue
		}
		c[fmt.Sprintf("class at %d", min(cls, 9999)/10*10)]++
		bodyCandidates(hd, cls, c)
		shift := cls - headClassAt
		v1 := binary.LittleEndian.Uint32(hd[headBody1At+shift:])
		v2 := binary.LittleEndian.Uint32(hd[headBody2At+shift:])
		flags, n := v1>>24, int(v1&0xffffff)
		hasEFM := bytes.Contains(pl, efm)
		c[fmt.Sprintf("flags %#x, efmdata path in block=%t", flags, hasEFM)]++
		if flags != 0x80 || n == 0 {
			continue
		}
		at := findBody(hd[shift:], n)
		if at >= 0 {
			at += shift
		}
		if at < 0 {
			c["inline: body start not found"]++
			continue
		}
		body := hd[at : at+n]
		low := strings.ToLower(string(body))
		end := strings.TrimSpace(low)
		c[fmt.Sprintf("inline: start=%d-bucket utf8=%t ends with '>'=%t html tag=%t", at/100*100, utf8.Valid(body),
			strings.HasSuffix(end, ">"), strings.HasPrefix(low, "<html") || strings.HasPrefix(low, "<div") || strings.HasPrefix(low, "<!doctype"))]++
		if !strings.HasSuffix(end, ">") {
			tail := "other"
			switch {
			case strings.Contains(low, "</html>"):
				tail = "has </html> inside"
			case strings.Contains(low, "</body>"):
				tail = "has </body> inside"
			case !strings.Contains(low, "<html"):
				tail = "no <html at all"
			}
			full := bytes.Index(hd[at:], []byte("</html>"))
			if full > 0 {
				e := at + full + len("</html>")
				for e < len(hd) && (hd[e] == '\r' || hd[e] == '\n') {
					e++
				}
				doc := hd[at:e]
				u16n := len(utf16.Encode([]rune(string(doc))))
				c[fmt.Sprintf("  size vs doc: bytes=%t runes=%t utf16units=%t ascii=%t", len(doc) == n, utf8.RuneCount(doc) == n, u16n == n, utf8.RuneCount(doc) == len(doc))]++
				gap := hd[at+n : at+full]
				lastc := "letter/digit"
				switch l := hd[at+n-1]; {
				case l == ' ' || l == '\n' || l == '\r' || l == '\t':
					lastc = "space"
				case l == '>' || l == '<' || l == '"' || l == '=' || l == ';':
					lastc = "markup"
				case l >= 0x80:
					lastc = "non-ascii byte"
				}
				c[fmt.Sprintf("  last char %s; NUL between body end and </html>: %t; next bytes %x", lastc, bytes.IndexByte(gap, 0) >= 0, hd[at+n:min(len(hd), at+n+2)])]++
				if len(doc) > n {
					pre := strings.ToLower(string(doc[:len(doc)-n]))
					c[fmt.Sprintf("  aligned to end: body starts with '<' at end-n: %t; prefix starts %q; prefix len %s", doc[len(doc)-n] == '<', pre[:min(5, len(pre))], sizeBucket(len(pre)))]++
				}
				if d0 := len(doc) - n; d0 > 0 {
					for _, form := range []uint32{uint32(d0) | 0x80000000, uint32(d0)} {
						fb := binary.LittleEndian.AppendUint32(nil, form)
						for o := 0; ; {
							k := bytes.Index(hd[o:at], fb)
							if k < 0 {
								break
							}
							c[fmt.Sprintf("  prefix size %#x-form at class%+d", form>>24, o+k-cls)]++
							o += k + 1
						}
					}
				}
				d := len(doc) - n
				u16d := u16n - n
				c[fmt.Sprintf("  diff: doc-n mod 65536 == 0: %t; doc-n bucket %s; utf16-n mod 65536==0: %t; doc>=64K: %t", d%65536 == 0, sizeBucket(d), u16d%65536 == 0, len(doc) >= 65536)]++
				n2 := int(v2 & 0xffffff)
				r := float64(n) / float64(len(doc))
				c[fmt.Sprintf("  doc len == size2: %t; size/doc ratio %.1f; v2 flags %#x", len(doc) == n2, r, v2>>24)]++
			}
			c[fmt.Sprintf("not '>' end: %s; size %s; </html> after the size: %t", tail, sizeBucket(n), full > n)]++
		}
		c[fmt.Sprintf("inline: next byte after body is 0=%t, second size %s", at+n < len(hd) && hd[at+n] == 0, cmpSize(v1, v2))]++
		if v2>>24 == 0x80 && int(v2&0xffffff) > 0 {
			m := int(v2 & 0xffffff)
			if at+n+m <= len(hd) {
				c[fmt.Sprintf("inline: second copy fits, starts with '<'=%t, same bytes=%t", hd[at+n] == '<', bytes.Equal(body, hd[at+n:at+n+m]))]++
			} else {
				c["inline: second copy does not fit"]++
			}
		}
	}
	printCounts(w, "body size rule (counts only)", c)
	return nil
}

func cmpSize(a, b uint32) string {
	switch {
	case b == 0:
		return "zero"
	case a == b:
		return "equal"
	default:
		return "different"
	}
}

// isHTMLDoc reports a complete HTML document: it starts with a tag and,
// without trailing white space, ends with </html>.
func isHTMLDoc(b []byte) bool {
	t := bytes.ToLower(bytes.TrimSpace(b))
	return bytes.HasPrefix(t, []byte("<")) && bytes.HasSuffix(t, []byte("</html>"))
}

// bodyCandidates checks the candidate body starts after the class: the run
// start, and the run start plus the sizes at class-214 and class-202.
func bodyCandidates(hd []byte, cls int, c map[string]int) {
	v1 := binary.LittleEndian.Uint32(hd[cls-210:])
	if v1>>24 != 0x80 {
		return
	}
	n := int(v1 & 0xffffff)
	r := findBody(hd[cls-headClassAt:], n)
	if r < 0 {
		c["rule2: no run start"]++
		return
	}
	r += cls - headClassAt
	sz := func(at int) int {
		v := binary.LittleEndian.Uint32(hd[at:])
		if v>>24 != 0x80 && v>>24 != 0 {
			return -1
		}
		return int(v & 0xffffff)
	}
	s0, s2 := sz(cls-214), sz(cls-202)
	for _, cand := range []struct {
		name string
		at   int
	}{{"run start", r}, {"after size@-214", r + s0}, {"after size@-202", r + s2}, {"after both", r + s0 + s2}} {
		if cand.at < r || cand.at+n > len(hd) {
			continue
		}
		if isHTMLDoc(hd[cand.at : cand.at+n]) {
			c["rule2: full document at "+cand.name]++
			return
		}
	}
	end := []byte("</html>")
	low := asciiLower(hd)
	for o := r; ; {
		k := bytes.Index(low[o:], end)
		if k < 0 {
			break
		}
		e := o + k + len(end)
		o = e
		for t := 0; t <= 4; t++ { // allow trailing white space inside the size
			if p := e + t - n; p >= r && p+n <= len(hd) && isHTMLDoc(hd[p:p+n]) {
				c[fmt.Sprintf("rule2: full document ending at a </html> (trailing %d)", t)]++
				return
			}
		}
	}
	tail := bytes.ToLower(bytes.TrimSpace(hd[r:min(len(hd), r+n)]))
	c[fmt.Sprintf("rule2: no full document; utf8=%t ends with '>'=%t has </html> after run start=%t", utf8.Valid(hd[r:min(len(hd), r+n)]),
		bytes.HasSuffix(tail, []byte(">")), bytes.Contains(low[r:], end))]++
}
