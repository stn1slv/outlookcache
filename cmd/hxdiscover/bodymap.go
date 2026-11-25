package main

import (
	"fmt"
	"io"

	"github.com/stn1slv/outlookcache/internal/hxstore"
)

// bodyMap prints the layout of one block: object framings (offset, type,
// ID, length) and, between them, text runs longer than 3 characters with
// offset, encoding, length and shape. Only known item classes
// (safeItemClass) are printed as text.
func bodyMap(data []byte, blockOff int64, w io.Writer) error {
	b, err := hxstore.DecodeAt(data, blockOff)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "block@%d key=%x type=%d payload=%d\n", b.Offset, b.Key, b.Type, len(b.Payload))
	var frames []hxstore.Framing
	hxstore.ForEachFraming(b.Payload, func(f hxstore.Framing) { frames = append(frames, f) })
	fi := 0
	for _, t := range tokenize(b.Payload) {
		for fi < len(frames) && frames[fi].Offset <= t.off {
			f := frames[fi]
			_, _ = fmt.Fprintf(w, "%7d frame type=%#x id=%x len=%d (ends %d)\n", f.Offset, f.Type, f.ID, len(f.Data), f.Offset+len(f.Data))
			fi++
		}
		if t.kind == "bin" || len([]rune(t.text)) < 4 {
			continue
		}
		in := ""
		for _, f := range frames {
			if t.off >= f.Offset && t.off < f.Offset+len(f.Data) {
				in = " (in frame)"
			}
		}
		show := shape(t.text)
		if t.kind == tokU16 && safeItemClass(t.text) {
			show = fmt.Sprintf("%q", t.text)
		}
		_, _ = fmt.Fprintf(w, "%7d %s len=%d %s%s\n", t.off, t.kind, len(t.text), show, in)
	}
	for _, f := range frames[fi:] {
		_, _ = fmt.Fprintf(w, "%7d frame type=%#x id=%x len=%d (ends %d)\n", f.Offset, f.Type, f.ID, len(f.Data), f.Offset+len(f.Data))
	}
	return nil
}
