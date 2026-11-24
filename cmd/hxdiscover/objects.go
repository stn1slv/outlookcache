package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/stn1slv/outlookcache/internal/hxstore"
)

var objMagic = []byte{0x06, 0x00, 0x70, 0x05}

// objects tests the framing hypothesis "06 00 70 05, u64 id, u16 type,
// u32 length" over all payloads and prints a type histogram and how many
// blocks start with an object right after the 16-byte payload header.
func objects(data []byte, w io.Writer) error {
	types := map[uint16]int{}
	status, flag, sizes := map[string]int{}, map[string]int{}, map[string]int{}
	var hits, fits, atStart, blocks, keyMatch int
	_, _, err := hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		blocks++
		p := b.Payload
		if len(p) >= 20 && bytes.Equal(p[16:20], objMagic) {
			atStart++
			if binary.LittleEndian.Uint64(p[20:]) == b.Key {
				keyMatch++
			}
		}
		for off := 0; ; {
			k := bytes.Index(p[off:], objMagic)
			if k < 0 || off+k+18 > len(p) {
				return nil
			}
			o := off + k
			off = o + 4
			hits++
			typ := binary.LittleEndian.Uint16(p[o+12:])
			n := int(binary.LittleEndian.Uint32(p[o+14:]))
			if n > 0 && o+n <= len(p) {
				fits++
				types[typ]++
				if typ == 0xc7 && n > 1800 {
					status[fmt.Sprintf("0x%02x", p[o+1772])]++
					flag[fmt.Sprintf("0x%02x", p[o+1742])]++
					sizes[fmt.Sprintf("%d", n/1000*1000)]++
				}
			}
		}
	})
	_, _ = fmt.Fprintf(w, "blocks=%d object_at_payload+16=%d (key==id %d) magic_hits=%d length_fits=%d\n", blocks, atStart, keyMatch, hits, fits)
	m := map[string]int{}
	for t, c := range types {
		m[fmt.Sprintf("0x%04x", t)] = c
	}
	printCounts(w, "0xc7 status byte at +1772", status)
	printCounts(w, "0xc7 flag byte at +1742", flag)
	printCounts(w, "0xc7 object size (rounded down to 1000)", sizes)
	printCounts(w, "object types (top)", m)
	return err
}
