package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/stn1slv/outlookcache/internal/hxstore"
)

// values finds little-endian u32 values in payloads that contain the
// marker and prints where they sit relative to the enclosing object.
func values(data []byte, marker, list string, w io.Writer) error {
	var want []uint32
	for _, s := range strings.Split(list, ",") {
		v, err := strconv.ParseUint(s, 10, 32)
		if err != nil {
			return err
		}
		want = append(want, uint32(v))
	}
	needle := encodeU16(marker)
	counts := map[string]int{}
	_, _, err := hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		p := b.Payload
		if len(needle) > 0 && !bytes.Contains(asciiLower(p), needle) {
			return nil
		}
		for _, v := range want {
			var pat [4]byte
			binary.LittleEndian.PutUint32(pat[:], v)
			for off := 0; ; {
				k := bytes.Index(p[off:], pat[:])
				if k < 0 {
					break
				}
				at := off + k
				off = at + 1
				obj := bytes.LastIndex(p[:at], objMagic)
				desc := "no object"
				if obj >= 0 && obj+14 <= len(p) {
					desc = fmt.Sprintf("type=0x%04x +%d", binary.LittleEndian.Uint16(p[obj+12:]), at-obj)
				}
				counts[fmt.Sprintf("%d in %s", v, desc)]++
			}
		}
		return nil
	})
	printCounts(w, "value positions", counts)
	return err
}
