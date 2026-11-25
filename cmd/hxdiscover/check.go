package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"sort"

	"github.com/stn1slv/outlookcache/internal/hxstore"
)

// check prints status and flag histograms of message-form 0xc7 copies,
// picks the current copy of each message with the live-block rule (see
// liveIndex.current), counts unread messages per folder, lists ambiguous
// choices and the failing block types.
func check(data []byte, w io.Writer) error {
	li, err := readLiveIndex(data)
	if err != nil {
		return err
	}
	hist := map[string]int{}
	copies := map[uint64][]c7copy{}
	if _, err := scanMessages(data, func(c c7copy, folderOK bool) {
		if !folderOK {
			hist["other layout, no folder ID at +1166 (skipped)"]++
			return
		}
		hist[fmt.Sprintf("status=0x%02x", c.status)]++
		hist[fmt.Sprintf("flag=0x%02x", c.flag)]++
		copies[c.id] = append(copies[c.id], c)
	}); err != nil {
		return err
	}
	printCounts(w, "message-form 0xc7 copies", hist)

	ids := make([]uint64, 0, len(copies))
	for id := range copies {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	rule, folders := map[string]int{}, map[string]int{}
	unread, shown := 0, 0
	for _, id := range ids {
		cs := copies[id]
		c, indexed, ambiguous := li.current(cs)
		switch {
		case ambiguous:
			rule["ambiguous (stamp tie with different state, or several indexed copies)"]++
			if shown < 3 {
				shown++
				for _, x := range cs {
					_, _ = fmt.Fprintf(w, "ambiguous id=%x block@%d stamp=%x status=%02x folder=%x indexed=%t\n",
						id, x.block, x.stamp, x.status, x.folder, li.indexed(x.block, 0))
				}
			}
		case indexed:
			rule["indexed copy"]++
		default:
			rule["no indexed copy, highest stamp"]++
		}
		key := fmt.Sprintf("%x", c.folder)
		if c.status&0x02 == 0 {
			unread++
			key += " unread"
		}
		folders[key]++
	}
	printCounts(w, "current copy chosen by", rule)
	_, _ = fmt.Fprintf(w, "current copies: messages=%d unread(bit 0x02 clear)=%d distinct folders=%d\n", len(ids), unread, len(folders))
	printCounts(w, "current copies per folder id", folders)

	st, damage, err := hxstore.Scan(data, "HxStore.hxd", func(hxstore.Block) error { return nil })
	if err != nil {
		return err
	}
	types := map[string]int{}
	for _, d := range damage {
		if d.Offset+0x28 > int64(len(data)) || d.Reason == "header crc mismatch" {
			types["type=? "+d.Reason]++ // header unreadable or not trustworthy
			continue
		}
		h := data[d.Offset : d.Offset+0x28]
		types[fmt.Sprintf("type=%d %s indexed=%t", binary.LittleEndian.Uint32(h[0x10:]), d.Reason, li.indexed(d.Offset, 0))]++
	}
	_, _ = fmt.Fprintf(w, "valid=%d\n", st.Valid)
	printCounts(w, "failing blocks", types)
	return nil
}
