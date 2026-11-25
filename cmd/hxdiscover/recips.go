package main

import (
	"fmt"
	"io"

	"github.com/stn1slv/outlookcache/internal/hxstore"
	"github.com/stn1slv/outlookcache/internal/store"
)

// recipientCheck parses (store.ParseRecipients) the To/Cc/Bcc objects of
// every item (copy from an
// indexed block when there is one) and prints counts only; for probe items
// it prints the number of entries per list.
func recipientCheck(data []byte, w io.Writer) error {
	li, err := readLiveIndex(data)
	if err != nil {
		return err
	}
	cur, err := currentC7(data, li, false)
	if err != nil {
		return err
	}
	type pick struct {
		obj     []byte
		indexed bool
	}
	lists := map[uint16]map[uint64]pick{0xcc: {}, 0xcd: {}, 0xce: {}}
	if _, _, err := hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		hxstore.ForEachFraming(b.Payload, func(f hxstore.Framing) {
			m, ok := lists[f.Type]
			if !ok {
				return
			}
			idx := li.indexed(b.Offset, 0)
			if old, seen := m[f.ID]; !seen || idx || !old.indexed {
				m[f.ID] = pick{f.Data, idx}
			}
		})
		return nil
	}); err != nil {
		return err
	}
	names := map[uint16]string{0xcc: "To", 0xcd: "Cc", 0xce: "Bcc"}
	c := map[string]int{}
	for id, it := range cur {
		line := ""
		for _, typ := range []uint16{0xcc, 0xcd, 0xce} {
			p, ok := lists[typ][id]
			if !ok {
				c[names[typ]+": no object"]++
				continue
			}
			rs := store.ParseRecipients(p.obj)
			c[fmt.Sprintf("%s: %d entries", names[typ], min(len(rs), 5))]++
			for _, r := range rs {
				if r.Name != "" {
					c[names[typ]+": entries with a name"]++
				} else {
					c[names[typ]+": entries without a name"]++
				}
			}
			line += fmt.Sprintf(" %s=%d", names[typ], len(rs))
		}
		if probeSubject(it.obj) {
			_, _ = fmt.Fprintf(w, "probe item %x%s\n", id, line)
		}
	}
	printCounts(w, "recipient lists (counts only, 5 = 5 or more)", c)
	return nil
}
