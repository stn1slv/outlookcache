package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"strings"

	"github.com/stn1slv/outlookcache/internal/hxstore"
	"github.com/stn1slv/outlookcache/internal/store"
)

// bodyProbe prints, for each probe message, the text runs of the unframed
// head of every version of its block: classes as is; HTML strings as size
// and flags only (holds the marker, full document, reply header, quote).
func bodyProbe(data []byte, w io.Writer) error {
	s, err := store.Load(data, "HxStore.hxd")
	if err != nil {
		return err
	}
	ix, err := hxstore.ReadIndex(data)
	if err != nil {
		return err
	}
	probes := map[uint64]string{}
	for _, m := range s.Messages {
		if strings.HasPrefix(strings.ToLower(m.Subject), markerPrefix) {
			var id uint64
			_, _ = fmt.Sscanf(m.ID, "%x", &id)
			probes[id] = probeText(m.Subject)
		}
	}
	_, _, err = hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		subj, ok := probes[b.Key]
		if !ok {
			return nil
		}
		head := len(b.Payload)
		hxstore.ForEachFraming(b.Payload, func(f hxstore.Framing) { head = min(head, f.Offset) })
		var parts []string
		for _, t := range tokenize(b.Payload[:head]) {
			switch {
			case t.kind == tokU16 && (strings.HasPrefix(t.text, "IPM.") || strings.HasPrefix(t.text, "REPORT.")):
				parts = append(parts, fmt.Sprintf("@%d %q", t.off, t.text))
			case t.kind == tokU8 && strings.HasPrefix(t.text, "<"):
				low := strings.ToLower(t.text)
				hd := b.Payload[:head]
				var at []string
				for _, form := range []struct {
					name string
					b    []byte
				}{
					{"u32f", binary.LittleEndian.AppendUint32(nil, uint32(len(t.text))|0x80000000)},
					{"u32f+1", binary.LittleEndian.AppendUint32(nil, uint32(len(t.text)+1)|0x80000000)},
					{"u32", binary.LittleEndian.AppendUint32(nil, uint32(len(t.text)))},
					{"u32+1", binary.LittleEndian.AppendUint32(nil, uint32(len(t.text)+1))},
				} {
					for o := 0; ; {
						k := bytes.Index(hd[o:], form.b)
						if k < 0 {
							break
						}
						at = append(at, fmt.Sprintf("%s@%d", form.name, o+k))
						o += k + 1
					}
				}
				parts = append(parts, fmt.Sprintf("@%d html(%d marker=%t start=%q end=%q) sizes:%v", t.off, len(t.text),
					strings.Contains(low, markerPrefix), low[:min(6, len(low))], low[max(0, len(low)-9):], at))
			case t.kind == tokU8 && len(t.text) >= 4:
				parts = append(parts, fmt.Sprintf("@%d u8(%d)", t.off, len(t.text)))
			}
		}
		var fields []string
		for _, off := range []int{1563, 1683, 1687, 1691, 1695, 1811, 1823, 1885, 1889} {
			if off+4 <= head {
				fields = append(fields, fmt.Sprintf("%d:%#x", off, binary.LittleEndian.Uint32(b.Payload[off:])))
			}
		}
		_, _ = fmt.Fprintf(w, "%x %s direct=%t head=%d fields=%v\n  %s\n", b.Key, subj, ix.Direct(b.Offset), head, fields, strings.Join(parts, "\n  "))
		return nil
	})
	return err
}
