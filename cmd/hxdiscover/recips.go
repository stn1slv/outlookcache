package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/stn1slv/outlookcache/internal/hxstore"
)

type recipient struct{ addr, name string }

// parseRecipients reads the entries of a recipient list object (0xcc To,
// 0xcd Cc, 0xce Bcc): each entry has a sort key, then the address and the
// display name as adjacent NUL-terminated UTF-16LE strings. An address is a
// UTF-16 string with "@" and no space; the name is the string that starts
// right after its terminator, when there is one. Repeated addresses (an
// entry can hold its address twice) are kept once.
func parseRecipients(obj []byte) []recipient {
	toks := tokenize(obj)
	var out []recipient
	seen := map[string]bool{}
	for i, t := range toks {
		if t.kind != tokU16 || !strings.Contains(t.text, "@") || strings.Contains(t.text, " ") {
			continue
		}
		r := recipient{addr: t.text}
		end := t.off + len(encodeU16(t.text)) + 2
		for _, n := range toks[i+1:] {
			if n.off > end {
				break
			}
			if n.kind == tokU16 && n.off == end {
				r.name = n.text
				break
			}
		}
		if !seen[strings.ToLower(r.addr)] {
			seen[strings.ToLower(r.addr)] = true
			out = append(out, r)
		} else if r.name != "" {
			for k := range out {
				if strings.EqualFold(out[k].addr, r.addr) && out[k].name == "" {
					out[k].name = r.name
				}
			}
		}
	}
	return out
}

// recipientCheck parses the To/Cc/Bcc objects of every item (copy from an
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
		forEachFraming(b.Payload, func(_ int, id uint64, typ uint16, obj []byte) {
			m, ok := lists[typ]
			if !ok {
				return
			}
			idx := li.indexed(b.Offset, 0)
			if old, seen := m[id]; !seen || idx || !old.indexed {
				m[id] = pick{obj, idx}
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
			rs := parseRecipients(p.obj)
			c[fmt.Sprintf("%s: %d entries", names[typ], min(len(rs), 5))]++
			for _, r := range rs {
				if r.name != "" {
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
