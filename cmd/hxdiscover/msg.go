package main

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/stn1slv/outlookcache/internal/hxstore"
)

// Object IDs seen in the reference store lie in this range (UNVERIFIED for
// other profiles; new objects get higher IDs, so the upper bound will move).
const (
	idLow  = 0x18000000
	idHigh = 0x19000000
)

func isID(v uint64) bool { return v >= idLow && v < idHigh }

// notFound marks a value that a search did not find.
const notFound = "none"

// Offsets inside a message-form 0xc7 object (see docs/STORAGE.md).
const (
	msgMinLen    = 1800
	msgAccountAt = 32
	msgStampAt   = 370
	msgFolderAt  = 1166
	msgFlagAt    = 1742
	msgStatusAt  = 1772
)

type c7copy struct {
	id           uint64
	stamp        uint64
	status, flag byte
	folder       uint64
	conv         uint64 // items only: conversation ID at +436, 0 when not an ID
	block        int64
	key          uint64
	obj          []byte
}

// forEachFraming calls fn for every object framing in p whose declared
// length fits inside p.
func forEachFraming(p []byte, fn func(o int, id uint64, typ uint16, obj []byte)) {
	for off := 0; ; {
		k := bytes.Index(p[off:], objMagic)
		if k < 0 || off+k+18 > len(p) {
			return
		}
		o := off + k
		off = o + 4
		n := uint64(binary.LittleEndian.Uint32(p[o+14:]))
		if n < 18 || n > uint64(len(p)-o) {
			continue
		}
		fn(o, binary.LittleEndian.Uint64(p[o+4:]), binary.LittleEndian.Uint16(p[o+12:]), p[o:o+int(n)])
	}
}

// scanConversations calls fn for every conversation-form 0xc7 copy (account ID at
// +32, length >= 1800). folderOK is false for the other layout that has no
// folder ID at +1166.
func scanConversations(data []byte, fn func(c c7copy, folderOK bool)) (hxstore.Stats, error) {
	st, _, err := hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		forEachFraming(b.Payload, func(_ int, id uint64, typ uint16, obj []byte) {
			if typ != 0xc7 || len(obj) < msgMinLen || !isID(binary.LittleEndian.Uint64(obj[msgAccountAt:])) {
				return
			}
			var sb [8]byte
			copy(sb[:6], obj[msgStampAt:])
			c := c7copy{
				id: id, stamp: binary.LittleEndian.Uint64(sb[:]), status: obj[msgStatusAt], flag: obj[msgFlagAt],
				folder: binary.LittleEndian.Uint64(obj[msgFolderAt:]), block: b.Offset, key: b.Key, obj: obj,
			}
			fn(c, isID(c.folder))
		})
		return nil
	})
	return st, err
}

// Header fields seen in every store so far (UNVERIFIED meaning): u64 at
// 0x58 and 0x78 point to two areas of equal size that both hold
// slot tables of block offsets.
const (
	hdrAreaA = 0x58
	hdrAreaB = 0x78
)

// liveIndex holds the block index: each area is a table of 65,536 slots of
// 5 bytes (0x50000 bytes). A used slot holds offset/256 of a block as a
// u32 in its first 4 bytes; the 5th byte is unknown. How a key maps to a
// slot is not known (UNVERIFIED layout, see docs/STORAGE.md).
type liveIndex struct {
	start, size int
	areas       [2]map[uint32]bool
}

const (
	indexSlots    = 65536
	indexSlotSize = 5
)

func readLiveIndex(data []byte) (liveIndex, error) {
	if len(data) < 0x80 {
		return liveIndex{}, hxstore.ErrNotHxStore
	}
	a := binary.LittleEndian.Uint64(data[hdrAreaA:])
	b := binary.LittleEndian.Uint64(data[hdrAreaB:])
	n := uint64(len(data))
	const size = indexSlots * indexSlotSize
	if a == 0 || b <= a || b-a != size || b >= n || size > n-b {
		return liveIndex{}, fmt.Errorf("unexpected area offsets %#x %#x for %d bytes", a, b, n)
	}
	li := liveIndex{start: int(a), size: size}
	li.areas[0] = slotSet(data[a : a+size])
	li.areas[1] = slotSet(data[b : b+size])
	return li, nil
}

// slotSet returns the block offsets/256 held by the used slots of one area.
func slotSet(area []byte) map[uint32]bool {
	s := map[uint32]bool{}
	for i := 0; i+indexSlotSize <= len(area); i += indexSlotSize {
		if v := binary.LittleEndian.Uint32(area[i:]); v != 0 {
			s[v] = true
		}
	}
	return s
}

func (li liveIndex) indexed(blockOff int64, area int) bool {
	return blockOff >= 0 && blockOff%256 == 0 && blockOff/256 <= int64(^uint32(0)) && li.areas[area][uint32(blockOff/256)]
}

// current picks the current copy of one message: the indexed copy (area
// A); when no copy is indexed, the copy with the highest stamp. ambiguous
// is true when the choice rests on a stamp tie between copies that differ
// in status or folder, or when more than one copy is indexed.
func (li liveIndex) current(cs []c7copy) (c c7copy, indexed, ambiguous bool) {
	var hits []c7copy
	for _, x := range cs {
		if li.indexed(x.block, 0) {
			hits = append(hits, x)
		}
	}
	if len(hits) == 1 {
		return hits[0], true, false
	}
	pool := cs
	if len(hits) > 1 {
		pool = hits
	}
	top := pool[0]
	for _, x := range pool {
		if x.stamp > top.stamp {
			top = x
		}
	}
	for _, x := range pool {
		if x.stamp == top.stamp && (x.status != top.status || x.folder != top.folder) {
			ambiguous = true
		}
	}
	return top, len(hits) > 1, ambiguous || len(hits) > 1
}

// keyFilter selects blocks by key; the zero value selects all blocks.
type keyFilter struct {
	set bool
	key uint64
}

func (f keyFilter) match(k uint64) bool { return !f.set || f.key == k }
