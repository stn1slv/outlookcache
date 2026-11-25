package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"sort"

	"github.com/stn1slv/outlookcache/internal/hxstore"
)

// Item (single message) form of 0xc7: no account ID at +32, and the same
// fields as the conversation form 10 bytes lower.
const itemShift = 10

// scanItems calls fn for every item-form 0xc7 copy that has a folder ID at
// +1156. The conversation ID is the u64 at +436.
func scanItems(data []byte, fn func(c c7copy, conv uint64)) error {
	_, _, err := hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		forEachFraming(b.Payload, func(_ int, id uint64, typ uint16, obj []byte) {
			if typ != 0xc7 || len(obj) < msgMinLen || isID(binary.LittleEndian.Uint64(obj[msgAccountAt:])) {
				return
			}
			folder := binary.LittleEndian.Uint64(obj[msgFolderAt-itemShift:])
			if !isID(folder) {
				return
			}
			var sb [8]byte
			copy(sb[:6], obj[msgStampAt-itemShift:])
			fn(c7copy{
				id: id, stamp: binary.LittleEndian.Uint64(sb[:]), status: obj[msgStatusAt-itemShift], flag: obj[msgFlagAt-itemShift],
				folder: folder, block: b.Offset, key: b.Key, obj: obj,
			}, binary.LittleEndian.Uint64(obj[436:]))
		})
		return nil
	})
	return err
}

// itemCheck counts current item copies (live-block rule) per folder and
// unread state, and how many items share a conversation.
func itemCheck(data []byte, show uint64, w io.Writer) error {
	li, err := readLiveIndex(data)
	if err != nil {
		return err
	}
	copies := map[uint64][]c7copy{}
	convOf := map[uint64]uint64{}
	if err := scanItems(data, func(c c7copy, conv uint64) {
		copies[c.id] = append(copies[c.id], c)
		convOf[c.id] = conv
	}); err != nil {
		return err
	}
	var ckpt uint64 // highest stamp among indexed item copies
	for _, cs := range copies {
		for _, x := range cs {
			if li.indexed(x.block, 0) {
				ckpt = max(ckpt, x.stamp)
			}
		}
	}
	rule, folders, convSize := map[string]int{}, map[string]int{}, map[uint64]int{}
	unread, unreadStrict, deleted := 0, 0, 0
	strict := map[string]int{}
	ids := make([]uint64, 0, len(copies))
	for id := range copies {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		c, indexed, ambiguous := li.current(copies[id])
		switch {
		case ambiguous:
			rule["ambiguous"]++
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
		// Strict rule: an item with no indexed copy is live only when its
		// newest copy is not older than the checkpoint; otherwise deleted.
		if !indexed && c.stamp < ckpt {
			deleted++
		} else {
			strict[key]++
			if c.status&0x02 == 0 {
				unreadStrict++
				maxStamp, readCopies := uint64(0), 0
				for _, x := range copies[id] {
					maxStamp = max(maxStamp, x.stamp)
					if x.status&0x02 != 0 {
						readCopies++
					}
				}
				_, _ = fmt.Fprintf(w, "unread item id=%x conv=%x copies=%d read_copies=%d chosen_stamp=%x max_stamp=%x indexed=%t status=%02x\n",
					id, convOf[id], len(copies[id]), readCopies, c.stamp, maxStamp, indexed, c.status)
			}
		}
		convSize[convOf[id]]++
		if id == show {
			for _, x := range copies[id] {
				_, _ = fmt.Fprintf(w, "copy id=%x conv=%x block@%d stamp=%x status=%02x folder=%x indexed=%t\n",
					id, convOf[id], x.block, x.stamp, x.status, x.folder, li.indexed(x.block, 0))
			}
		}
	}
	sizes := map[string]int{}
	for _, n := range convSize {
		sizes[fmt.Sprintf("conversations with %d item(s)", min(n, 5))]++
	}
	printCounts(w, "current item copy chosen by", rule)
	_, _ = fmt.Fprintf(w, "items=%d unread=%d conversations=%d\n", len(ids), unread, len(convSize))
	printCounts(w, "items per folder", folders)
	_, _ = fmt.Fprintf(w, "strict rule: checkpoint=%x treated as deleted=%d live=%d unread=%d\n", ckpt, deleted, len(ids)-deleted, unreadStrict)
	printCounts(w, "strict rule: items per folder", strict)
	printCounts(w, "conversation sizes (5 = 5 or more)", sizes)
	return nil
}
