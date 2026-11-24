package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/stn1slv/outlookcache/internal/hxstore"
)

type c7copy struct {
	stamp        uint64
	status, flag byte
}

// check splits 0xc7 objects into standalone (framing id == block key) and
// embedded, prints status/flag histograms per form, tests whether copies
// with the same highest stamp agree, and lists failing block types.
func check(data []byte, w io.Writer) error {
	hist := map[string]int{}
	copies := map[uint64][]c7copy{}
	st, damage, err := hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		p := b.Payload
		for off := 0; ; {
			k := bytes.Index(p[off:], objMagic)
			if k < 0 || off+k+18 > len(p) {
				return nil
			}
			o := off + k
			off = o + 4
			id := binary.LittleEndian.Uint64(p[o+4:])
			typ := binary.LittleEndian.Uint16(p[o+12:])
			n := int(binary.LittleEndian.Uint32(p[o+14:]))
			if typ != 0xc7 || o+n > len(p) || n < 1800 {
				continue
			}
			form, shift := "standalone", 0
			if id != b.Key {
				form, shift = "embedded", -10
			}
			s := p[o+1772+shift]
			f := p[o+1742+shift]
			hist[fmt.Sprintf("%s status=0x%02x", form, s)]++
			hist[fmt.Sprintf("%s flag=0x%02x", form, f)]++
			var sb [8]byte
			copy(sb[:6], p[o+370+shift:])
			copies[id] = append(copies[id], c7copy{binary.LittleEndian.Uint64(sb[:]), s, f})
		}
	})
	if err != nil {
		return err
	}
	printCounts(w, "0xc7 by form", hist)
	ids, conflicts, stamps := 0, 0, map[uint64]bool{}
	for _, cs := range copies {
		ids++
		var top c7copy
		for _, c := range cs {
			if c.stamp > top.stamp {
				top = c
			}
		}
		stamps[top.stamp] = true
		for _, c := range cs {
			if c.stamp == top.stamp && (c.status != top.status || c.flag != top.flag) {
				conflicts++
				break
			}
		}
	}
	_, _ = fmt.Fprintf(w, "0xc7 ids=%d ids_with_conflicting_top_copies=%d distinct_top_stamps=%d\n", ids, conflicts, len(stamps))
	types := map[string]int{}
	for _, d := range damage {
		h := data[d.Offset : d.Offset+0x28]
		types[fmt.Sprintf("type=%d %s", binary.LittleEndian.Uint32(h[0x10:]), d.Reason)]++
	}
	_, _ = fmt.Fprintf(w, "valid=%d\n", st.Valid)
	printCounts(w, "failing blocks", types)
	return nil
}
