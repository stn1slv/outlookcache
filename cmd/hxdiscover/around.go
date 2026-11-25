package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/stn1slv/outlookcache/internal/hxstore"
)

// around dumps a redacted window around the first occurrences of a u64
// value that directly follows a 0x4d folder type marker.
func around(data []byte, r redactor, v uint64, before, after, maxHits int, w io.Writer) error {
	var pat [8]byte
	binary.LittleEndian.PutUint64(pat[:], v)
	shown := 0
	_, _, err := hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		p := b.Payload
		for off := 0; shown < maxHits; {
			k := bytes.Index(p[off:], pat[:])
			if k < 0 {
				return nil
			}
			at := off + k
			off = at + 8
			if at < 10 || p[at-10] != 0x4d {
				continue
			}
			shown++
			_, _ = fmt.Fprintf(w, "\n=== block@%d key=%x hit@%d\n", b.Offset, b.Key, at)
			lo, hi := max(0, at-before), min(len(p), at+after)
			dump(w, r, p[lo:hi], lo, at)
		}
		if shown >= maxHits {
			return hxstore.ErrStop
		}
		return nil
	})
	return err
}
