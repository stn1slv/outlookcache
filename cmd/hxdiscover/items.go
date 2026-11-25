package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"sort"

	"github.com/stn1slv/outlookcache/internal/hxstore"
)

// Item (single message) form of 0xc7: no account ID at +32, and the same
// fields as the conversation form 10 bytes lower.
const (
	itemShift  = 10
	itemMinLen = msgMinLen - itemShift
	itemConvAt = 436 // u64 conversation ID
)

// unreadSuffix marks unread rows in per-folder counts.
const unreadSuffix = " unread"

// scanItems calls fn for every item-form 0xc7 copy. folderOK is false when
// the u64 at +1156 is not an object ID (another layout); such copies are
// passed so the caller can count them.
func scanItems(data []byte, fn func(c c7copy, folderOK bool)) error {
	_, _, err := hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		forEachFraming(b.Payload, func(_ int, id uint64, typ uint16, obj []byte) {
			if typ != 0xc7 || len(obj) < itemMinLen || isID(binary.LittleEndian.Uint64(obj[msgAccountAt:])) {
				return
			}
			var sb [8]byte
			copy(sb[:6], obj[msgStampAt-itemShift:])
			c := c7copy{
				id: id, stamp: binary.LittleEndian.Uint64(sb[:]), status: obj[msgStatusAt-itemShift], flag: obj[msgFlagAt-itemShift],
				folder: binary.LittleEndian.Uint64(obj[msgFolderAt-itemShift:]), block: b.Offset, key: b.Key, obj: obj,
			}
			if v := binary.LittleEndian.Uint64(obj[itemConvAt:]); isID(v) {
				c.conv = v
			}
			fn(c, isID(c.folder))
		})
		return nil
	})
	return err
}

// checkpointStamp estimates the stamp of the last index checkpoint: the
// highest stamp among indexed copies after dropping the top few, so that a
// single false-positive index match cannot move it. 0 means unknown.
func checkpointStamp(copies map[uint64][]c7copy, li liveIndex) uint64 {
	const ignoreTop = 5
	var stamps []uint64
	for _, cs := range copies {
		for _, x := range cs {
			if x.stamp != 0 && li.indexed(x.block, 0) {
				stamps = append(stamps, x.stamp)
			}
		}
	}
	if len(stamps) <= ignoreTop {
		return 0
	}
	sort.Slice(stamps, func(i, j int) bool { return stamps[i] > stamps[j] })
	return stamps[ignoreTop]
}

// itemCheck counts item copies with the current-copy rule (liveIndex.current)
// per folder and read state. All totals include deleted items, because no
// reliable deletion signal is known. It also reports what a candidate
// deletion rule would drop ("no indexed copy and older than the
// checkpoint"); a probe check showed that this rule drops live Sent items,
// so it is printed for diagnosis only. With marker set, it lists every
// item that holds the synthetic marker.
func itemCheck(data []byte, show uint64, marker string, w io.Writer) error {
	li, err := readLiveIndex(data)
	if err != nil {
		return err
	}
	copies := map[uint64][]c7copy{}
	skipped := 0
	if err := scanItems(data, func(c c7copy, folderOK bool) {
		if !folderOK {
			skipped++
			return
		}
		copies[c.id] = append(copies[c.id], c)
	}); err != nil {
		return err
	}
	ids := make([]uint64, 0, len(copies))
	for id := range copies {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	ckpt := checkpointStamp(copies, li)

	rule, folders, candidate := map[string]int{}, map[string]int{}, map[string]int{}
	conv, candConv := map[uint64]int{}, map[uint64]int{}
	unread, ambiguousUnread, noConv, dropped := 0, 0, 0, 0
	for _, id := range ids {
		cs := copies[id]
		c, indexed, ambiguous := li.current(cs)
		key := fmt.Sprintf("%x", c.folder)
		switch {
		case ambiguous:
			rule["ambiguous (not counted below)"]++
			if c.status&0x02 == 0 {
				ambiguousUnread++
			}
			continue
		case indexed:
			rule["indexed copy"]++
		default:
			rule["no indexed copy, highest stamp"]++
		}
		if c.status&0x02 == 0 {
			unread++
			key += unreadSuffix
		}
		folders[key]++
		if c.conv == 0 {
			noConv++
		}
		conv[c.conv]++
		if indexed {
			var newest c7copy
			for _, x := range cs {
				if x.stamp > newest.stamp {
					newest = x
				}
			}
			if newest.stamp > c.stamp {
				lag := "older than checkpoint"
				if newest.stamp >= ckpt {
					lag = "at or after checkpoint"
				}
				rule["indexed copy is older than an unindexed copy, newer copy "+lag]++
			}
		}
		drop := ckpt != 0 && !indexed && c.stamp < ckpt
		if drop {
			dropped++
		} else {
			candidate[key]++
			candConv[c.conv]++
		}
		if marker != "" && hasMarker(cs, marker) {
			_, _ = fmt.Fprintf(w, "probe item id=%x conv=%x folder=%x status=%02x indexed=%t candidate_drop=%t copies=%d\n",
				id, c.conv, c.folder, c.status, indexed, drop, len(cs))
		}
	}
	if show != 0 {
		for _, x := range copies[show] {
			_, _ = fmt.Fprintf(w, "copy id=%x conv=%x block@%d stamp=%x status=%02x folder=%x indexedA=%t indexedB=%t\n",
				show, x.conv, x.block, x.stamp, x.status, x.folder, li.indexed(x.block, 0), li.indexed(x.block, 1))
		}
	}
	printCounts(w, "current item copy chosen by", rule)
	_, _ = fmt.Fprintf(w, "all item IDs (deleted ones included): items=%d unread=%d (+%d ambiguous unread) conversations=%d no conversation ID=%d other layout copies (skipped)=%d\n",
		len(ids)-rule["ambiguous (not counted below)"], unread, ambiguousUnread, len(conv), noConv, skipped)
	printCounts(w, "items per folder (deleted ones included)", folders)
	printCounts(w, "conversation sizes (deleted ones included)", convSizes(conv))
	if ckpt == 0 {
		_, _ = fmt.Fprintln(w, "candidate deletion rule: checkpoint unknown (too few indexed copies), not applied")
		return nil
	}
	_, _ = fmt.Fprintf(w, "candidate deletion rule (REJECTED: drops live Sent items in the probe check): checkpoint=%x would drop=%d\n", ckpt, dropped)
	printCounts(w, "candidate rule: items per folder", candidate)
	printCounts(w, "candidate rule: conversation sizes", convSizes(candConv))
	return nil
}

func convSizes(conv map[uint64]int) map[string]int {
	sizes := map[string]int{}
	for _, n := range conv {
		sizes[fmt.Sprintf("conversations with %d item(s)", min(n, 5))]++
	}
	return sizes
}

// hasMarker reports whether any copy contains the synthetic marker.
func hasMarker(cs []c7copy, marker string) bool {
	n := encodeU16(marker)
	for _, c := range cs {
		if bytes.Contains(asciiLower(c.obj), n) {
			return true
		}
	}
	return false
}
