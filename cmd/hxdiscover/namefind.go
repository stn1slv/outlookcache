package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/stn1slv/outlookcache/internal/hxstore"
)

// nameFind counts where a known name (given by the developer, UTF-16LE or
// UTF-8, ASCII case ignored) occurs. It prints only the framing ID and type that contain
// it, the length prefix seen before it and the object IDs in the 48 bytes
// before it, never the text.
func nameFind(data []byte, name string, w io.Writer) error {
	counts := map[string]int{}
	_, _, err := hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		p := asciiLower(b.Payload) // case-insensitive for ASCII; offsets stay the same
		low := string(asciiLower([]byte(name)))
		for enc, needle := range map[string][]byte{"utf16": encodeU16(low), "utf8": []byte(low)} {
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

// addrCount counts current message-form 0xc7 copies (highest stamp) per
// folder and how many of them contain the given text (ASCII case ignored,
// UTF-16LE) before the given offset, to test where an account's own
// address is stored. Only counts are printed.
func addrCount(data []byte, text string, w io.Writer) error {
	needle := encodeU16(string(asciiLower([]byte(text))))
	type cp struct {
		stamp  uint64
		folder uint64
		has    bool
		pos    int
	}
	best := map[uint64]cp{}
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
			if binary.LittleEndian.Uint16(p[o+12:]) != 0xc7 || n < 1800 || o+n > len(p) ||
				!isID(binary.LittleEndian.Uint64(p[o+32:])) || !isID(binary.LittleEndian.Uint64(p[o+1166:])) {
				continue
			}
			var sb [8]byte
			copy(sb[:6], p[o+370:])
			c := cp{stamp: binary.LittleEndian.Uint64(sb[:]), folder: binary.LittleEndian.Uint64(p[o+1166:])}
			c.pos = bytes.Index(asciiLower(p[o:o+n]), needle)
			c.has = c.pos >= 0
			id := binary.LittleEndian.Uint64(p[o+4:])
			if old, ok := best[id]; !ok || c.stamp > old.stamp {
				best[id] = c
			}
		}
	})
	if err != nil {
		return err
	}
	counts := map[string]int{}
	for _, c := range best {
		counts[fmt.Sprintf("folder %x total", c.folder)]++
		if c.has {
			counts[fmt.Sprintf("folder %x contains text", c.folder)]++
		}
	}
	printCounts(w, "messages per folder (text not printed)", counts)
	return nil
}
