package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/stn1slv/outlookcache/internal/hxstore"
)

// fieldCompare takes the last folder (class 0x4d) framing of type typ for every ID (framing ID
// equal to the block key, so the object is the main one of its block) and
// prints one line per ID with the u16 values at the offsets that differ
// between IDs. Text spans are printed as "txt".
func fieldCompare(data []byte, typ uint16, w io.Writer) error {
	objs := map[uint64][]byte{}
	_, _, err := hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		p := b.Payload
		for off := 0; ; {
			k := bytes.Index(p[off:], objMagic)
			if k < 0 || off+k+18 > len(p) {
				return nil
			}
			o := off + k
			off = o + 4
			id := binary.LittleEndian.Uint64(p[o+4:])
			n := int(binary.LittleEndian.Uint32(p[o+14:]))
			if binary.LittleEndian.Uint16(p[o+12:]) != typ || id != b.Key || n < 34 || o+n > len(p) || binary.LittleEndian.Uint16(p[o+28:]) != 0x4d {
				continue
			}
			objs[id] = p[o : o+n]
		}
	})
	if err != nil {
		return err
	}
	ids := make([]uint64, 0, len(objs))
	minLen := -1
	for id, o := range objs {
		ids = append(ids, id)
		if minLen < 0 || len(o) < minLen {
			minLen = len(o)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	masks := map[uint64][]bool{}
	for _, id := range ids {
		masks[id] = textMask(objs[id])
	}
	var offs []int
	for i := 18; i+2 <= minLen; i += 2 {
		first := binary.LittleEndian.Uint16(objs[ids[0]][i:])
		for _, id := range ids[1:] {
			if binary.LittleEndian.Uint16(objs[id][i:]) != first {
				offs = append(offs, i)
				break
			}
		}
	}
	_, _ = fmt.Fprintf(w, "type=0x%x ids=%d min_size=%d differing u16 offsets=%v\n", typ, len(ids), minLen, offs)
	for _, id := range ids {
		o, m := objs[id], masks[id]
		var sb strings.Builder
		for _, i := range offs {
			if m[i] || m[i+1] {
				sb.WriteString(" txt")
				continue
			}
			_, _ = fmt.Fprintf(&sb, " %04x", binary.LittleEndian.Uint16(o[i:]))
		}
		_, _ = fmt.Fprintf(w, "%x size=%d%s\n", id, len(o), sb.String())
	}
	return nil
}
