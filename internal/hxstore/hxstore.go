// Package hxstore reads the container layer of HxStore.hxd, the local mail
// store of new Outlook for Mac: the file header and the CRC-protected,
// LZ4-compressed blocks. It knows nothing about the records inside a block.
//
// The layout follows the public specification at
// https://github.com/securized/hxstore-reverse-engineering (MIT) and was
// checked against Outlook 16.115.1, see docs/STORAGE.md.
package hxstore

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
)

// FileMagic is the first eight bytes of every HxStore.hxd file.
const FileMagic = "Nostromo"

// SupportedVersion is the format version byte this package was verified with.
const SupportedVersion = 'i'

const (
	headerSize      = 0x40
	blockHeaderSize = 0x28
	// A block cannot be larger than the 32 MiB cap in the file header.
	maxBlockLen = 32 << 20
)

// blockMagic is the block marker 0x5d0245643b706a05, stored little-endian.
var blockMagic = []byte{0x05, 0x6a, 0x70, 0x3b, 0x64, 0x45, 0x02, 0x5d}

var (
	// ErrNotHxStore reports a file that does not start with FileMagic.
	ErrNotHxStore = errors.New("not an HxStore file")
	// ErrStop can be returned by a Scan callback to end the scan early.
	ErrStop = errors.New("stop scan")
)

// Header is the part of the file header this package uses.
type Header struct {
	Version  byte
	PageSize uint64
}

// Supported reports whether the version byte is the one this package was
// verified with. Other versions may still parse, because every block is
// checked by two CRCs, but callers should warn.
func (h Header) Supported() bool { return h.Version == SupportedVersion }

// ParseHeader reads the file header.
func ParseHeader(data []byte) (Header, error) {
	if len(data) < headerSize || string(data[:8]) != FileMagic {
		return Header{}, ErrNotHxStore
	}
	return Header{
		Version:  data[8],
		PageSize: binary.LittleEndian.Uint64(data[0x38:]),
	}, nil
}

// Block is one decoded block.
type Block struct {
	Offset  int64  // file offset of the block header
	Type    uint32 // 8 = message data in the observed stores; 16 is rare
	Key     uint64 // header +0x20: ID in the first object framing of the payload
	Payload []byte // decompressed payload
}

// Damage describes a block that failed validation.
type Damage struct {
	File   string `json:"file"`
	Offset int64  `json:"offset"`
	Length int    `json:"length"`
	Reason string `json:"reason"`
}

// Stats counts what a scan found.
type Stats struct {
	Candidates int // block magic hits
	Valid      int // blocks that passed all checks
	ByType     map[uint32]int
	Inflated   int64 // total decompressed bytes of valid blocks
}

// Scan finds every block in data, validates it, and calls fn with the
// decoded payload. A block is valid only when the header CRC, the payload
// CRC and the exact decompressed length all match. Invalid blocks are
// returned as Damage. On the observed store most of them are type 16, a
// second block format whose payload this package cannot read yet
// (UNVERIFIED, see docs/STORAGE.md).
func Scan(data []byte, file string, fn func(Block) error) (Stats, []Damage, error) {
	st := Stats{ByType: map[uint32]int{}}
	var damage []Damage
	pos := 8
	for {
		k := bytes.Index(data[pos:], blockMagic)
		if k < 0 {
			return st, damage, nil
		}
		hit := pos + k
		pos = hit + 1
		start := hit - 8
		if start < headerSize {
			continue
		}
		st.Candidates++
		blk, n, reason := decodeBlock(data, start)
		if reason != "" {
			damage = append(damage, Damage{File: file, Offset: int64(start), Length: n, Reason: reason})
			continue
		}
		st.Valid++
		st.ByType[blk.Type]++
		st.Inflated += int64(len(blk.Payload))
		pos = start + n // a valid block cannot contain another block header
		if err := fn(blk); err != nil {
			if errors.Is(err, ErrStop) {
				return st, damage, nil
			}
			return st, damage, err
		}
	}
}

// decodeBlock validates and decodes the block at start. It returns the block,
// its size on disk, and a non-empty reason when the block is invalid.
func decodeBlock(data []byte, start int) (Block, int, string) {
	if len(data)-start < blockHeaderSize {
		return Block{}, len(data) - start, "truncated header"
	}
	h := data[start : start+blockHeaderSize]
	if crc32.ChecksumIEEE(h[4:0x20]) != binary.LittleEndian.Uint32(h[0:]) {
		return Block{}, blockHeaderSize, "header crc mismatch"
	}
	typ := binary.LittleEndian.Uint32(h[0x10:])
	plen := int(binary.LittleEndian.Uint32(h[0x14:]))
	ilen := int(binary.LittleEndian.Uint32(h[0x18:]))
	if plen > maxBlockLen || ilen > maxBlockLen {
		return Block{}, blockHeaderSize, "length over 32 MiB cap"
	}
	size := blockHeaderSize + plen
	if len(data)-start < size {
		return Block{}, len(data) - start, "truncated payload"
	}
	if crc32.ChecksumIEEE(data[start+8:start+size]) != binary.LittleEndian.Uint32(h[4:]) {
		return Block{}, size, "payload crc mismatch"
	}
	out, err := DecodeLZ4(data[start+blockHeaderSize:start+size], ilen)
	if err != nil {
		return Block{}, size, fmt.Sprintf("decompress: %v", err)
	}
	key := binary.LittleEndian.Uint64(h[0x20:])
	return Block{Offset: int64(start), Type: typ, Key: key, Payload: out}, size, ""
}
