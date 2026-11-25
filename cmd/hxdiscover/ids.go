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

// ids prints, for each 0xc7 framing that contains the marker, the u64
// values in the object-ID range found at each 8-byte-aligned-or-not
// position before the marker, so stable identity fields can be spotted.
func ids(data []byte, marker string, w io.Writer) error {
	needle := encodeU16(marker)
	type row struct {
		block           int64
		key, frame      uint64
		size, markerRel int
		stamp           uint64
		status          byte
		refs            []string
	}
	var rows []row
	_, _, err := hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		p := b.Payload
		for off := 0; ; {
			k := bytes.Index(p[off:], objMagic)
			if k < 0 || off+k+18 > len(p) {
				return nil
			}
			o := off + k
			off = o + 4
			if binary.LittleEndian.Uint16(p[o+12:]) != 0xc7 {
				continue
			}
			n := int(binary.LittleEndian.Uint32(p[o+14:]))
			if o+n > len(p) || n < 1800 {
				continue
			}
			obj := p[o : o+n]
			m := bytes.Index(asciiLower(obj), needle)
			if m < 0 {
				continue
			}
			var sb [8]byte
			copy(sb[:6], obj[370:])
			r := row{
				block: b.Offset, key: b.Key, frame: binary.LittleEndian.Uint64(obj[4:]), size: n, markerRel: m,
				stamp: binary.LittleEndian.Uint64(sb[:]), status: obj[1772],
			}
			for i := 18; i+8 <= m; i++ {
				v := binary.LittleEndian.Uint64(obj[i:])
				if isID(v) {
					r.refs = append(r.refs, fmt.Sprintf("+%d:%x", i, v))
				}
			}
			rows = append(rows, r)
		}
	})
	sort.Slice(rows, func(i, j int) bool { return rows[i].stamp < rows[j].stamp })
	for _, r := range rows {
		_, _ = fmt.Fprintf(w, "@%d key=%x frame=%x size=%d marker+%d stamp=%x status=%02x\n    %s\n",
			r.block, r.key, r.frame, r.size, r.markerRel, r.stamp, r.status, strings.Join(r.refs, " "))
	}
	return err
}
