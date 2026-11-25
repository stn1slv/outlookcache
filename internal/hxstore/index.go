package hxstore

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// Header fields of the block index (UNVERIFIED meaning, see docs/STORAGE.md).
const (
	hdrIndexA      = 0x58 // u64 offset of index area A
	hdrIndexB      = 0x78 // u64 offset of index area B
	hdrIndexCounts = 0x68 // u32 entries, u32 overflow buckets
	// IndexSlots is the number of 5-byte slots in one index area.
	IndexSlots    = 65536
	indexSlotSize = 5
	indexAreaSize = IndexSlots * indexSlotSize
)

// Index is the block index of the store: a hash table of 5-byte slots. A
// used slot holds a block offset / 256 (u32) and a size class (one byte).
// About a fifth of the entries sit in overflow buckets whose storage is not
// decoded, so a block that is not in a direct slot may still be live.
type Index struct {
	Entries         uint32 // header count of index entries
	OverflowBuckets uint32 // header count of overflow buckets
	direct          map[uint32]bool
}

// ReadIndex reads index area A. It fails when the header does not describe
// two areas of the expected size inside the file.
func ReadIndex(data []byte) (Index, error) {
	if len(data) < headerSize+0x40 {
		return Index{}, ErrNotHxStore
	}
	a := binary.LittleEndian.Uint64(data[hdrIndexA:])
	b := binary.LittleEndian.Uint64(data[hdrIndexB:])
	n := uint64(len(data))
	if a == 0 || b <= a || b-a != indexAreaSize || b >= n || indexAreaSize > n-b {
		return Index{}, fmt.Errorf("%w: unexpected index areas %#x %#x", ErrNotHxStore, a, b)
	}
	ix := Index{
		Entries:         binary.LittleEndian.Uint32(data[hdrIndexCounts:]),
		OverflowBuckets: binary.LittleEndian.Uint32(data[hdrIndexCounts+4:]),
		direct:          map[uint32]bool{},
	}
	area := data[a : a+indexAreaSize]
	for i := 0; i+indexSlotSize <= len(area); i += indexSlotSize {
		if v := binary.LittleEndian.Uint32(area[i:]); v != 0 {
			ix.direct[v] = true
		}
	}
	return ix, nil
}

// Direct reports whether a direct index slot points at the block that
// starts at this file offset.
func (ix Index) Direct(blockOffset int64) bool {
	if blockOffset < 0 || blockOffset%256 != 0 || blockOffset/256 > int64(^uint32(0)) {
		return false
	}
	return ix.direct[uint32(blockOffset/256)]
}

// Framing is one object inside a block payload: `06 00 70 05`, u64 ID,
// u16 type, u32 length, then the object fields.
type Framing struct {
	Offset int // offset in the payload
	ID     uint64
	Type   uint16
	Data   []byte // the whole object, framing header included
}

var framingMagic = []byte{0x06, 0x00, 0x70, 0x05}

// FramingMagic returns a copy of the object framing marker.
func FramingMagic() []byte { return append([]byte(nil), framingMagic...) }

// ForEachFraming calls fn for every framing in payload whose declared
// length fits inside the payload.
func ForEachFraming(payload []byte, fn func(Framing)) {
	for off := 0; ; {
		k := bytes.Index(payload[off:], framingMagic)
		if k < 0 || off+k+18 > len(payload) {
			return
		}
		o := off + k
		off = o + 4
		n := uint64(binary.LittleEndian.Uint32(payload[o+14:]))
		if n < 18 || n > uint64(len(payload)-o) {
			continue
		}
		fn(Framing{
			Offset: o, ID: binary.LittleEndian.Uint64(payload[o+4:]), Type: binary.LittleEndian.Uint16(payload[o+12:]),
			Data: payload[o : o+int(n)],
		})
	}
}
