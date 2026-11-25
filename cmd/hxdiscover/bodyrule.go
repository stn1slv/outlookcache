package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"strconv"
	"unicode/utf8"

	"github.com/stn1slv/outlookcache/internal/hxstore"
	"github.com/stn1slv/outlookcache/internal/store"
)

// headClassAt is the usual offset of the UTF-16 item class in the body
// record of an item's block.
const headClassAt = 1893

// ownBlocks returns, for every current message, the payload of the block
// keyed by its item ID (the version in a direct index slot, else the last
// one).
func ownBlocks(data []byte) (map[uint64][]byte, *store.Store, error) {
	s, err := store.Load(data, "HxStore.hxd")
	if err != nil {
		return nil, nil, err
	}
	ix, err := hxstore.ReadIndex(data)
	if err != nil {
		return nil, nil, err
	}
	want := map[uint64]bool{}
	for _, m := range s.Messages {
		if id, err := strconv.ParseUint(m.ID, 16, 64); err == nil {
			want[id] = true
		}
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
		return nil, nil, err
	}
	out := make(map[uint64][]byte, len(blocks))
	for k, p := range blocks {
		out[k] = p.payload
	}
	return out, s, nil
}

// bodyRule runs the shipped body parser (store.ParseBody) on the own
// block of every current message and prints counts only: where the class
// sits, the flag byte of the size field, the body kind, and for inline
// bodies whether the result is a whole UTF-8 HTML document.
func bodyRule(data []byte, w io.Writer) error {
	blocks, _, err := ownBlocks(data)
	if err != nil {
		return err
	}
	c := map[string]int{"messages with an own block": len(blocks)}
	for _, pl := range blocks {
		head := len(pl)
		hxstore.ForEachFraming(pl, func(f hxstore.Framing) { head = min(head, f.Offset) })
		hd := pl[:head]
		cls := -1
		for _, nd := range [][]byte{encodeU16("IPM."), encodeU16("REPORT.")} {
			if k := bytes.Index(hd, nd); k >= 0 && (cls < 0 || k < cls) {
				cls = k
			}
		}
		switch {
		case cls < 0:
			c["class: none in head"]++
		case cls < headClassAt:
			c["class: before +1893"]++
		case cls == headClassAt:
			c["class: at +1893"]++
		default:
			c["class: after +1893"]++
		}
		if cls >= headClassAt {
			c[fmt.Sprintf("size field flags %#x", binary.LittleEndian.Uint32(hd[cls-210:])>>24)]++
		}
		ref := store.ParseBody(pl)
		c["kind: "+ref.Kind]++
		if ref.Kind == store.BodyInline {
			c[fmt.Sprintf("inline: whole document=%t utf8=%t size %s", isHTMLDoc(ref.HTML), utf8.Valid(ref.HTML), sizeBucket(len(ref.HTML)))]++
		}
	}
	printCounts(w, "body rule (counts only)", c)
	return nil
}

// isHTMLDoc reports a whole HTML document: it starts with '<' and, without
// trailing white space, ends with </html> (ASCII case-insensitive).
func isHTMLDoc(b []byte) bool {
	t := bytes.TrimRight(b, " \t\r\n")
	return len(t) > 7 && t[0] == '<' && bytes.HasSuffix(asciiLower(t), []byte("</html>"))
}
