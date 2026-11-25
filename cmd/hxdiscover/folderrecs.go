package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/stn1slv/outlookcache/internal/hxstore"
)

type folderRec struct {
	id, parent, owner uint64
	name              string
}

// folderRecords finds child folder records in folder lists (UNVERIFIED
// layout, from probes). It anchors on the record tail after the name:
// `01`, 4 bytes, `05 00 d8 04`, 4 bytes, `00 00 4d`, 9 zero bytes, folder
// ID, u32 2, owner (account root). The name is UTF-16LE ending with 0x0000
// just before the tail; it starts after a Windows sort key whose last unit
// is below U+0020. The parent is the ID in the nearest `e3 01` + 8 zero
// bytes + ID header within 600 bytes before the name.
func folderRecords(data []byte) (map[uint64]folderRec, error) {
	out := map[uint64]folderRec{}
	tail := []byte{0x05, 0x00, 0xd8, 0x04}
	head := []byte{0xe3, 0x01, 0, 0, 0, 0, 0, 0, 0, 0}
	_, _, err := hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		p := b.Payload
		for off := 0; ; {
			k := bytes.Index(p[off:], tail)
			if k < 0 {
				return nil
			}
			t := off + k - 5 // start of the tail
			off = off + k + 1
			if t < 4 || t+45 > len(p) || p[t] != 0x01 || p[t+13] != 0 || p[t+14] != 0 || p[t+15] != 0x4d ||
				p[t-1] != 0 || p[t-2] != 0 || binary.LittleEndian.Uint32(p[t+33:]) != 2 {
				continue
			}
			id := binary.LittleEndian.Uint64(p[t+25:])
			if !isID(id) {
				continue
			}
			e := t - 2 // name terminator
			n := e
			for n-2 >= 0 && binary.LittleEndian.Uint16(p[n-2:]) >= 0x20 && e-n < 512 {
				n -= 2
			}
			if n == e {
				continue
			}
			parent := uint64(0)
			if h := bytes.LastIndex(p[max(0, n-600):n], head); h >= 0 {
				h += max(0, n-600) + len(head)
				if h+8 <= len(p) && isID(binary.LittleEndian.Uint64(p[h:])) {
					parent = binary.LittleEndian.Uint64(p[h:])
				}
			}
			out[id] = folderRec{id: id, parent: parent, owner: binary.LittleEndian.Uint64(p[t+37:]), name: decodeU16(p[n:e])}
		}
	})
	return out, err
}

// folderTree prints the folder records as a tree with name lengths only
// (names are shown only when they are synthetic probe names).
func folderTree(data []byte, r redactor, match string, w io.Writer) error {
	recs, err := folderRecords(data)
	if err != nil {
		return err
	}
	children := map[uint64][]uint64{}
	for id, f := range recs {
		children[f.parent] = append(children[f.parent], id)
	}
	var roots []uint64
	for p := range children {
		if _, ok := recs[p]; !ok {
			roots = append(roots, p)
		}
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i] < roots[j] })
	var walk func(id uint64, depth int)
	walk = func(id uint64, depth int) {
		kids := children[id]
		sort.Slice(kids, func(i, j int) bool { return kids[i] < kids[j] })
		for _, k := range kids {
			f := recs[k]
			_, _ = fmt.Fprintf(w, "%*s%x owner=%x name=%s%s\n", 2*depth, "", k, f.owner, r.show(f.name), map[bool]string{true: " <== equals the -marker name", false: ""}[match != "" && strings.EqualFold(f.name, match)])
			if depth < 10 {
				walk(k, depth+1)
			}
		}
	}
	for _, root := range roots {
		_, _ = fmt.Fprintf(w, "parent %x (no record)\n", root)
		walk(root, 1)
	}
	_, _ = fmt.Fprintf(w, "records=%d\n", len(recs))
	return nil
}
