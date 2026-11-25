package main

import (
	"bytes"
	"fmt"
	"io"

	"github.com/stn1slv/outlookcache/internal/hxstore"
)

// htmlTags are lower-case starts of HTML elements that mark body text.
var htmlTags = []string{"<html", "<body", "<div", "<p>", "<p ", "<br", "<table", "<span"}

// hasHTML reports whether b holds an HTML tag as ASCII/UTF-8 or UTF-16LE.
func hasHTML(b []byte) (u8, u16 bool) {
	low := asciiLower(b)
	for _, t := range htmlTags {
		u8 = u8 || bytes.Contains(low, []byte(t))
		u16 = u16 || bytes.Contains(low, encodeU16(t))
	}
	return u8, u16
}

// bodyFind locates the probe marker in decoded payloads and prints where
// each hit sits: block type and key, the object framing that holds it
// (type, ID, offset in the object, object length), the text encoding and
// whether the same object (or the bytes around a hit outside framings)
// holds HTML tags. It prints no text.
func bodyFind(data []byte, maxLines int, w io.Writer) error {
	needles := map[string][]byte{tokU16: encodeU16(markerPrefix), tokU8: []byte(markerPrefix)}
	counts := map[string]int{}
	lines := 0
	_, _, err := hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		low := asciiLower(b.Payload)
		var frames []hxstore.Framing
		hxstore.ForEachFraming(b.Payload, func(f hxstore.Framing) { frames = append(frames, f) })
		for enc, nd := range needles {
			for off := 0; ; {
				k := bytes.Index(low[off:], nd)
				if k < 0 {
					break
				}
				hit := off + k
				off = hit + len(nd)
				where := "outside framings"
				span := b.Payload[max(0, hit-4096):min(len(b.Payload), hit+4096)]
				line := ""
				for _, f := range frames {
					if hit >= f.Offset && hit < f.Offset+len(f.Data) {
						where = fmt.Sprintf("object type %#x", f.Type)
						span = f.Data
						line = fmt.Sprintf("id=%x rel=%d objlen=%d", f.ID, hit-f.Offset, len(f.Data))
						break
					}
				}
				h8, h16 := hasHTML(span)
				key := fmt.Sprintf("block type %d, %s, %s, html8=%t html16=%t", b.Type, where, enc, h8, h16)
				counts[key]++
				if (h8 || h16) && lines < maxLines {
					lines++
					_, _ = fmt.Fprintf(w, "hit block@%d key=%x type=%d %s %s %s html8=%t html16=%t\n",
						b.Offset, b.Key, b.Type, where, line, enc, h8, h16)
				}
			}
		}
		return nil
	})
	printCounts(w, "marker hits by place (counts only)", counts)
	return err
}
