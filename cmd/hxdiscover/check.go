package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/stn1slv/outlookcache/internal/hxstore"
)

type c7copy struct {
	stamp        uint64
	status, flag byte
	folder       uint64
	block        int64
	key          uint64
}

// check splits 0xc7 objects into standalone (framing id == block key) and
// embedded, prints status/flag histograms per form, tests whether copies
// with the same highest stamp agree, and lists failing block types.
func check(data []byte, w io.Writer) error {
	hist := map[string]int{}
	copies := map[uint64][]c7copy{}
	st, damage, err := hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		p := b.Payload
		for off := 0; ; {
			k := bytes.Index(p[off:], objMagic)
			if k < 0 || off+k+18 > len(p) {
				return nil
			}
			o := off + k
			off = o + 4
			id := binary.LittleEndian.Uint64(p[o+4:])
			typ := binary.LittleEndian.Uint16(p[o+12:])
			n := int(binary.LittleEndian.Uint32(p[o+14:]))
			if typ != 0xc7 || o+n > len(p) || n < 1800 {
				continue
			}
			ref := binary.LittleEndian.Uint64(p[o+32:])
			if ref < 0x18000000 || ref >= 0x19000000 {
				hist["parent form (skipped)"]++
				continue
			}
			if !isID(binary.LittleEndian.Uint64(p[o+1166:])) {
				hist["other layout, no folder ID at +1166 (skipped)"]++
				continue
			}
			const form = "message"
			s := p[o+1772]
			f := p[o+1742]
			hist[fmt.Sprintf("%s status=0x%02x", form, s)]++
			hist[fmt.Sprintf("%s flag=0x%02x", form, f)]++
			var sb [8]byte
			copy(sb[:6], p[o+370:])
			folder := binary.LittleEndian.Uint64(p[o+1166:])
			copies[id] = append(copies[id], c7copy{binary.LittleEndian.Uint64(sb[:]), s, f, folder, b.Offset, b.Key})
		}
	})
	if err != nil {
		return err
	}
	printCounts(w, "0xc7 by form", hist)
	ids, conflicts, stamps := 0, 0, map[uint64]bool{}
	diffKinds := map[string]int{}
	zeroStamp := 0
	shown := 0
	for id, cs := range copies {
		ids++
		top := cs[0]
		for _, c := range cs {
			if c.stamp > top.stamp {
				top = c
			}
		}
		if top.stamp == 0 {
			zeroStamp++
		}
		stamps[top.stamp] = true
		for _, c := range cs {
			if c.stamp == top.stamp && (c.status != top.status || c.flag != top.flag || c.folder != top.folder) {
				conflicts++
				what := ""
				if c.status != top.status {
					what += fmt.Sprintf(" status %02x/%02x", c.status, top.status)
				}
				if c.flag != top.flag {
					what += " flag"
				}
				if c.folder != top.folder {
					what += " folder"
				}
				diffKinds[what]++
				if shown < 3 {
					shown++
					for _, x := range cs {
						_, _ = fmt.Fprintf(w, "conflict id=%x block@%d key=%x stamp=%x status=%02x folder=%x\n", id, x.block, x.key, x.stamp, x.status, x.folder)
					}
				}
				break
			}
		}
	}
	folders := map[string]int{}
	unread := 0
	for _, cs := range copies {
		top := cs[0]
		for _, c := range cs {
			if c.stamp > top.stamp {
				top = c
			}
		}
		key := fmt.Sprintf("%x", top.folder)
		if top.status&0x02 == 0 {
			unread++
			key += " unread"
		}
		folders[key]++
	}
	_, _ = fmt.Fprintf(w, "current copies: unread(bit 0x02 clear)=%d distinct folders=%d\n", unread, len(folders))
	printCounts(w, "current copies per folder id", folders)
	_, _ = fmt.Fprintf(w, "0xc7 ids=%d ids_with_conflicting_top_copies=%d distinct_top_stamps=%d\n", ids, conflicts, len(stamps))
	printCounts(w, "conflict kinds", diffKinds)
	_, _ = fmt.Fprintf(w, "ids whose newest stamp is 0: %d\n", zeroStamp)
	types := map[string]int{}
	for _, d := range damage {
		if d.Offset+0x28 > int64(len(data)) || d.Reason == "header crc mismatch" {
			types["type=? "+d.Reason]++ // header unreadable or not trustworthy
			continue
		}
		h := data[d.Offset : d.Offset+0x28]
		types[fmt.Sprintf("type=%d %s", binary.LittleEndian.Uint32(h[0x10:]), d.Reason)]++
	}
	_, _ = fmt.Fprintf(w, "valid=%d\n", st.Valid)
	printCounts(w, "failing blocks", types)
	return nil
}
