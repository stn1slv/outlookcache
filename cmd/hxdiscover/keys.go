package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"regexp"
	"sort"

	"github.com/stn1slv/outlookcache/internal/hxstore"
)

var identRe = regexp.MustCompile(`[A-Za-z][A-Za-z0-9_]{2,40}`)

// commonKeys lists identifier-like words in the UTF-16 and UTF-8 text of
// objects of one type, and prints only words found in at least minObjs
// distinct objects. Words that occur in few objects (names, addresses,
// subjects) are never printed; only their count is.
func commonKeys(data []byte, typ uint16, minObjs int, w io.Writer) error {
	seen := map[string]map[uint64]bool{}
	objs := map[uint64]bool{}
	_, _, err := hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		p := b.Payload
		for off := 0; ; {
			k := bytes.Index(p[off:], objMagic)
			if k < 0 || off+k+18 > len(p) {
				return nil
			}
			o := off + k
			off = o + 4
			n := int(binary.LittleEndian.Uint32(p[o+14:]))
			if binary.LittleEndian.Uint16(p[o+12:]) != typ || n < 18 || o+n > len(p) {
				continue
			}
			id := binary.LittleEndian.Uint64(p[o+4:])
			objs[id] = true
			obj := p[o : o+n]
			var texts []string
			for _, t := range tokenize(obj) {
				texts = append(texts, t.text)
			}
			for _, t := range texts {
				for _, word := range identRe.FindAllString(t, -1) {
					if seen[word] == nil {
						seen[word] = map[uint64]bool{}
					}
					seen[word][id] = true
				}
			}
		}
	})
	if err != nil {
		return err
	}
	var common []string
	rare := 0
	for word, ids := range seen {
		if len(ids) >= minObjs {
			common = append(common, fmt.Sprintf("%4d %s", len(ids), word))
		} else {
			rare++
		}
	}
	sort.Strings(common)
	_, _ = fmt.Fprintf(w, "type=0x%x objects=%d common words (in >= %d objects)=%d rare words (not printed)=%d\n", typ, len(objs), minObjs, len(common), rare)
	for _, c := range common {
		_, _ = fmt.Fprintln(w, c)
	}
	return nil
}
