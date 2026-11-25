package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"strings"

	"github.com/stn1slv/outlookcache/internal/hxstore"
)

// bodyShape checks, for every current message, the block keyed by its
// item ID (the version in a direct index slot, else the last one): whether
// an unframed UTF-16 item class followed by a NUL-terminated byte string
// exists outside the object framings, what that string starts with, and
// whether a u32 (size | 0x80000000) equal to its byte size appears before
// the class. It also counts EFMData paths in the block. Counts only.
func bodyShape(data []byte, w io.Writer) error {
	blocks, _, err := ownBlocks(data)
	if err != nil {
		return err
	}
	efm := encodeU16("/EFMData/")
	c := map[string]int{"messages with an own block": len(blocks)}
	for _, pl := range blocks {
		head := len(pl)
		hxstore.ForEachFraming(pl, func(f hxstore.Framing) { head = min(head, f.Offset) })
		hd := pl[:head]
		if n := bytes.Count(pl, efm); n > 0 {
			c[fmt.Sprintf("EFMData paths in block: %d", min(n, 4))]++
		}
		var bodies [][]byte
		classes := 0
		for _, t := range tokenize(hd) {
			switch {
			case t.kind == tokU16 && (strings.HasPrefix(t.text, "IPM.") || strings.HasPrefix(t.text, "REPORT.")):
				classes++
			case t.kind == tokU8 && len(t.text) > 0 && t.text[0] == '<':
				bodies = append(bodies, []byte(t.text))
			}
		}
		c[fmt.Sprintf("head: classes=%d html strings=%d", min(classes, 3), min(len(bodies), 3))]++
		for _, bd := range bodies {
			flag := binary.LittleEndian.AppendUint32(nil, uint32(len(bd))|0x80000000)
			c[fmt.Sprintf("  html size %s, size|0x80000000 in head=%t", sizeBucket(len(bd)), bytes.Contains(hd, flag))]++
		}
		if len(bodies) == 2 {
			a, b := bodies[0], bodies[1]
			rel := "different"
			switch {
			case bytes.Equal(a, b):
				rel = "equal"
			case bytes.Equal(bytes.TrimSpace(a), bytes.TrimSpace(b)):
				rel = "equal after trim"
			case len(a) > len(b):
				rel = "first longer"
			case len(a) < len(b):
				rel = "second longer"
			}
			c["two html strings: "+rel]++
		}
	}
	printCounts(w, "inline body shape (counts only)", c)
	return nil
}

func sizeBucket(n int) string {
	switch {
	case n < 1024:
		return "<1 KB"
	case n < 16<<10:
		return "1-16 KB"
	case n < 64<<10:
		return "16-64 KB"
	default:
		return ">=64 KB"
	}
}
