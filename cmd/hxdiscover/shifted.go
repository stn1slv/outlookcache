package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/stn1slv/outlookcache/internal/hxstore"
)

func isID(v uint64) bool { return v >= 0x18000000 && v < 0x19000000 }

// shifted looks at message-form 0xc7 copies whose u64 at +1166 is not an
// object ID. For each one it searches +1166±128 for a known folder ID (one
// seen at +1166 in a normal copy) and counts the deltas with the object
// size, to learn whether the field moves.
func shifted(data []byte, w io.Writer) error {
	type obj struct {
		id uint64
		b  []byte
	}
	var all []obj
	folders := map[uint64]bool{}
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
			if binary.LittleEndian.Uint16(p[o+12:]) != 0xc7 || o+n > len(p) || n < 1800 || !isID(binary.LittleEndian.Uint64(p[o+32:])) {
				continue
			}
			c := p[o : o+n]
			all = append(all, obj{binary.LittleEndian.Uint64(p[o+4:]), c})
			if f := binary.LittleEndian.Uint64(c[1166:]); isID(f) {
				folders[f] = true
			}
		}
	})
	if err != nil {
		return err
	}
	good, bad := map[string]int{}, map[string]int{}
	ids := map[uint64]bool{}
	for _, o := range all {
		if isID(binary.LittleEndian.Uint64(o.b[1166:])) {
			good[fmt.Sprintf("size=%d", len(o.b))]++
			continue
		}
		ids[o.id] = true
		delta := notFound
		for d := 1; d <= 128 && delta == notFound; d++ {
			for _, s := range []int{d, -d} {
				i := 1166 + s
				if i >= 0 && i+8 <= len(o.b) && folders[binary.LittleEndian.Uint64(o.b[i:])] {
					delta = fmt.Sprintf("%+d", s)
					break
				}
			}
		}
		bad[fmt.Sprintf("size=%d delta=%s", len(o.b), delta)]++
	}
	_, _ = fmt.Fprintf(w, "message-form copies=%d known folders=%d copies with non-ID at +1166: ids=%d\n", len(all), len(folders), len(ids))
	printCounts(w, "normal copies by size", good)
	printCounts(w, "non-ID copies by size and delta to a known folder ID", bad)
	return nil
}

// notFound marks a value that a search did not find.
const notFound = "none"
