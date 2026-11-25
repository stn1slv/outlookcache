package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/stn1slv/outlookcache/internal/hxstore"
)

// nameFind counts where a known folder name (given by the user, UTF-16LE,
// exact case) occurs. It prints only the framing ID and type that contain
// it, the length prefix seen before it and the object IDs in the 48 bytes
// before it, never the text.
func nameFind(data []byte, name string, w io.Writer) error {
	counts := map[string]int{}
	_, _, err := hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		p := b.Payload
		for enc, needle := range map[string][]byte{"utf16": encodeU16(name), "utf8": []byte(name)} {
			findName(p, b.Key, enc, needle, counts)
		}
		return nil
	})
	printCounts(w, "occurrences (name not printed)", counts)
	return err
}

func findName(p []byte, key uint64, enc string, needle []byte, counts map[string]int) {
	{
		for off := 0; ; {
			k := bytes.Index(p[off:], needle)
			if k < 0 {
				return
			}
			at := off + k
			off = at + 2
			frame := notFound
			if f := bytes.LastIndex(p[:at], objMagic); f >= 0 && f+18 <= len(p) {
				frame = fmt.Sprintf("id=%x type=0x%04x +%d", binary.LittleEndian.Uint64(p[f+4:]), binary.LittleEndian.Uint16(p[f+12:]), at-f)
			}
			prefix := "-"
			if at >= 6 {
				prefix = fmt.Sprintf("%08x", binary.LittleEndian.Uint32(p[at-6:]))
			}
			var refs []string
			for i := max(0, at-48); i+8 <= at; i++ {
				if v := binary.LittleEndian.Uint64(p[i:]); isID(v) {
					refs = append(refs, fmt.Sprintf("-%d:%x", at-i, v))
					i += 7
				}
			}
			counts[fmt.Sprintf("%s blockkey=%x frame[%s] lenprefix=%s refs=%v", enc, key, frame, prefix, refs)]++
		}
	}
}
