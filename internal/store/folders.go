package store

import (
	"bytes"
	"encoding/binary"
	"strconv"
)

var (
	recHead = []byte{0xe3, 0x01, 0, 0, 0, 0, 0, 0, 0, 0}
	recTail = []byte{0x05, 0x00, 0xd8, 0x04}
	nineZ   = make([]byte, 9)
)

type folderRec struct {
	id, parent, owner uint64
	name              string
	block             int64
	direct            bool
}

// readFolderRecords finds child folder records (form 1) in one payload: `e3
// 01` + 8 zero bytes, parent ID, a Windows sort key, the name as UTF-16LE
// ending with 0x0000, then `01`, 4 bytes, `05 00 d8 04`, 4 bytes,
// `00 00 4d`, 9 zero bytes, folder ID, u32 2, owner (account root). The name
// start is found by walking back from its terminator while the UTF-16 unit
// is >= 0x20; the unit before the name must be a non-zero byte and 0x00.
func readFolderRecords(p []byte, ids idSpace) []folderRec {
	var out []folderRec
	for off := 0; ; {
		k := bytes.Index(p[off:], recTail)
		if k < 0 {
			return out
		}
		t := off + k - 5
		off += k + 1
		if t < 4 || t+45 > len(p) || p[t] != 0x01 || p[t+13] != 0 || p[t+14] != 0 || p[t+15] != 0x4d ||
			!bytes.Equal(p[t+16:t+25], nineZ) || p[t-1] != 0 || p[t-2] != 0 || binary.LittleEndian.Uint32(p[t+33:]) != 2 {
			continue
		}
		id := binary.LittleEndian.Uint64(p[t+25:])
		if !ids.has(id) {
			continue
		}
		e := t - 2
		n := e
		for n-2 >= 0 && binary.LittleEndian.Uint16(p[n-2:]) >= 0x20 && e-n < 512 {
			n -= 2
		}
		if n == e || n < 2 || p[n-1] != 0 || p[n-2] == 0 {
			continue
		}
		lo := max(0, n-1100)
		h := bytes.LastIndex(p[lo:n], recHead)
		if h < 0 {
			continue
		}
		s := lo + h + len(recHead)
		if s+8 > n || !ids.has(binary.LittleEndian.Uint64(p[s:])) {
			continue
		}
		out = append(out, folderRec{
			id: id, parent: binary.LittleEndian.Uint64(p[s:]), owner: binary.LittleEndian.Uint64(p[t+37:]),
			name: decodeU16(p[n:e]),
		})
	}
}

// Folder roles by slot offset in the unframed head of an account root
// block (verified: inbox, drafts, sent, deleted, junk, archive; likely:
// outbox).
var roleSlots = map[int]string{
	285: "archive", 429: "deleted", 477: "drafts", 861: "inbox", 909: "junk", 1205: "outbox", 1549: "sent",
}

// readRootSlots reads the role slots of a block whose payload starts with
// an account root record: at each role offset a reference
// [type u16 = 0x4d][8 zero bytes][folder ID][u32 2][owner][folder ID][owner].
// ok is false unless the Inbox slot holds a folder owned by key.
func readRootSlots(p []byte, key uint64) (map[string]uint64, bool) {
	ref := func(at int) (uint64, bool) {
		if at+46 > len(p) || binary.LittleEndian.Uint16(p[at:]) != 0x4d || !bytes.Equal(p[at+2:at+10], nineZ[:8]) ||
			binary.LittleEndian.Uint32(p[at+18:]) != 2 {
			return 0, false
		}
		id := binary.LittleEndian.Uint64(p[at+10:])
		owner := binary.LittleEndian.Uint64(p[at+22:])
		return id, owner == key && binary.LittleEndian.Uint64(p[at+30:]) == id
	}
	if _, ok := ref(861); !ok {
		return nil, false
	}
	roles := map[string]uint64{}
	for at, role := range roleSlots {
		if id, ok := ref(at); ok {
			roles[role] = id
		}
	}
	return roles, true
}

var filesS0 = encodeU16("/Files/S0/")

// filesDirs returns the account folder numbers found in UTF-16 paths
// ".../Files/S0/<n>/..." in a payload.
func filesDirs(p []byte) []string {
	var out []string
	for off := 0; ; {
		k := bytes.Index(p[off:], filesS0)
		if k < 0 {
			return out
		}
		i := off + k + len(filesS0)
		off = i
		var digits []byte
		for i+1 < len(p) && p[i+1] == 0 && p[i] >= '0' && p[i] <= '9' && len(digits) < 9 {
			digits = append(digits, p[i])
			i += 2
		}
		if len(digits) > 0 {
			if _, err := strconv.Atoi(string(digits)); err == nil {
				out = append(out, string(digits))
			}
		}
	}
}

func encodeU16(s string) []byte {
	var b []byte
	for _, r := range s {
		b = binary.LittleEndian.AppendUint16(b, uint16(r))
	}
	return b
}
