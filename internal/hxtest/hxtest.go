// Package hxtest writes synthetic HxStore.hxd files for tests. It never
// contains real mailbox data.
package hxtest

import (
	"encoding/binary"
	"hash/crc32"
)

// BlockMagic is the on-disk block marker.
var BlockMagic = []byte{0x05, 0x6a, 0x70, 0x3b, 0x64, 0x45, 0x02, 0x5d}

// EncodeLZ4 compresses src as one LZ4 block with a simple greedy matcher.
// It is not fast; it only has to produce valid input for the decoder,
// including overlapping matches.
func EncodeLZ4(src []byte) []byte {
	var out []byte
	table := map[uint32]int{}
	anchor, i := 0, 0
	limit := len(src) - 12 // LZ4 rule: the last match starts 12 bytes before the end
	for i < limit {
		key := binary.LittleEndian.Uint32(src[i:])
		cand, ok := table[key]
		table[key] = i
		if !ok || i-cand > 0xffff || binary.LittleEndian.Uint32(src[cand:]) != key {
			i++
			continue
		}
		ml := 4
		for i+ml < len(src)-5 && src[cand+ml] == src[i+ml] {
			ml++
		}
		out = appendSequence(out, src[anchor:i], i-cand, ml)
		i += ml
		anchor = i
	}
	return appendSequence(out, src[anchor:], 0, 0)
}

func appendSequence(out, lit []byte, dist, ml int) []byte {
	token := byte(0)
	if len(lit) >= 15 {
		token = 15 << 4
	} else {
		token = byte(len(lit)) << 4
	}
	if ml > 0 {
		if ml-4 >= 15 {
			token |= 15
		} else {
			token |= byte(ml - 4)
		}
	}
	out = append(out, token)
	if len(lit) >= 15 {
		out = appendLen(out, len(lit)-15)
	}
	out = append(out, lit...)
	if ml == 0 {
		return out
	}
	out = append(out, byte(dist), byte(dist>>8))
	if ml-4 >= 15 {
		out = appendLen(out, ml-4-15)
	}
	return out
}

func appendLen(out []byte, n int) []byte {
	for n >= 255 {
		out = append(out, 255)
		n -= 255
	}
	return append(out, byte(n))
}

// Block returns one valid block: 40-byte header with the key at +0x20,
// both CRCs, LZ4 payload.
func Block(typ uint32, key uint64, payload []byte) []byte {
	comp := EncodeLZ4(payload)
	b := make([]byte, 0x28+len(comp))
	copy(b[8:], BlockMagic)
	binary.LittleEndian.PutUint32(b[0x10:], typ)
	binary.LittleEndian.PutUint32(b[0x14:], uint32(len(comp)))
	binary.LittleEndian.PutUint32(b[0x18:], uint32(len(payload)))
	binary.LittleEndian.PutUint32(b[0x1c:], 4)
	binary.LittleEndian.PutUint64(b[0x20:], key)
	copy(b[0x28:], comp)
	binary.LittleEndian.PutUint32(b[4:], crc32.ChecksumIEEE(b[8:]))
	binary.LittleEndian.PutUint32(b[0:], crc32.ChecksumIEEE(b[4:0x20]))
	return b
}

// File returns a store with a version 'i' header followed by the blocks,
// each padded to a 16-byte boundary with zero bytes.
func File(blocks ...[]byte) []byte {
	f := make([]byte, 0x1000, 0x2000)
	copy(f, "Nostromo")
	f[8] = 'i'
	binary.LittleEndian.PutUint64(f[0x38:], 0x1000)
	for _, b := range blocks {
		f = append(f, b...)
		for len(f)%16 != 0 {
			f = append(f, 0)
		}
	}
	return f
}

// Index layout written by FileWithIndex (same as the real store).
const (
	IndexA        = 0x1000
	IndexAreaSize = 65536 * 5
)

// FileWithIndex returns a store whose header points at two block index
// areas and whose blocks start on 256-byte boundaries after them. Block i
// gets a direct index slot when direct[i] is true; the header entry count
// is the number of such slots.
func FileWithIndex(blocks [][]byte, direct []bool) []byte {
	b := IndexA + IndexAreaSize
	start := b + IndexAreaSize
	f := make([]byte, start, start+len(blocks)*4096)
	copy(f, "Nostromo")
	f[8] = 'i'
	binary.LittleEndian.PutUint64(f[0x38:], 0x1000)
	binary.LittleEndian.PutUint64(f[0x58:], IndexA)
	binary.LittleEndian.PutUint64(f[0x78:], uint64(b))
	slot := 0
	used := 0
	for i, blk := range blocks {
		for len(f)%256 != 0 {
			f = append(f, 0)
		}
		off := len(f)
		f = append(f, blk...)
		if i < len(direct) && direct[i] {
			for _, area := range []int{IndexA, b} {
				p := area + 5*slot
				binary.LittleEndian.PutUint32(f[p:], uint32(off/256))
				f[p+4] = byte((0x28 + len(blk) + 511) / 512)
			}
			slot++
			used++
		}
	}
	binary.LittleEndian.PutUint32(f[0x68:], uint32(used))
	return f
}
