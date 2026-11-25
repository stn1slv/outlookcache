package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/stn1slv/outlookcache/internal/hxstore"
)

// frames lists the framings (id, type, size, marker offset) that contain
// the marker, to learn which object type holds it.
func frames(data []byte, marker string, w io.Writer) error {
	needle := encodeU16(marker)
	counts := map[string]int{}
	_, _, err := hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		p := b.Payload
		for off := 0; ; {
			k := bytes.Index(p[off:], objMagic)
			if k < 0 || off+k+18 > len(p) {
				return nil
			}
			o := off + k
			off = o + 4
			n := int(binary.LittleEndian.Uint32(p[o+14:]))
			if n <= 0 || o+n > len(p) {
				continue
			}
			m := bytes.Index(asciiLower(p[o:o+n]), needle)
			if m < 0 {
				continue
			}
			counts[fmt.Sprintf("id=%x type=0x%04x size=%d marker+%d", binary.LittleEndian.Uint64(p[o+4:]),
				binary.LittleEndian.Uint16(p[o+12:]), n, m)]++
		}
	})
	printCounts(w, "framings containing marker", counts)
	return err
}

// folderObjects lists distinct 0x1e4 objects: owner, the ID-range refs and
// the redacted first UTF-16 string.
func folderObjects(data []byte, r redactor, w io.Writer) error {
	counts := map[string]int{}
	_, _, err := hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		p := b.Payload
		for off := 0; ; {
			k := bytes.Index(p[off:], objMagic)
			if k < 0 || off+k+18 > len(p) {
				return nil
			}
			o := off + k
			off = o + 4
			n := int(binary.LittleEndian.Uint32(p[o+14:]))
			if binary.LittleEndian.Uint16(p[o+12:]) != 0x1e4 || n <= 0 || o+n > len(p) {
				continue
			}
			obj := p[o : o+n]
			var refs []string
			for i := 18; i+8 <= len(obj) && len(refs) < 8; i++ {
				v := binary.LittleEndian.Uint64(obj[i:])
				if isID(v) {
					refs = append(refs, fmt.Sprintf("+%d:%x", i, v))
					i += 7
				}
			}
			name := "-"
			for _, t := range tokenize(obj) {
				if t.kind == tokU16 {
					name = r.show(t.text)
					break
				}
			}
			counts[fmt.Sprintf("owner=%x size=%d name=%s refs=%v", binary.LittleEndian.Uint64(obj[4:]), n, name, refs)]++
		}
	})
	printCounts(w, "0x1e4 objects", counts)
	return err
}

type folderEntry struct {
	id, owner, account uint64
	kind               uint32
	name               string
}

// folderEntries finds embedded folder entries:
// [account u64][kind u32] 00 00 00 00 4d 00 (9 zero bytes) [id] 02 00 00 00 [owner] [id] [owner] 00 00 00 00 [len|0x80000000] [utf16 name].
func folderEntries(data []byte) (map[uint64]folderEntry, error) {
	out := map[uint64]folderEntry{}
	pat := []byte{0x4d, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	_, _, err := hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		p := b.Payload
		for off := 0; ; {
			k := bytes.Index(p[off:], pat)
			if k < 0 {
				return nil
			}
			at := off + k
			off = at + 1
			e := at + len(pat)
			if at < 16 || e+44 > len(p) {
				continue
			}
			id := binary.LittleEndian.Uint64(p[e:])
			owner := binary.LittleEndian.Uint64(p[e+12:])
			if binary.LittleEndian.Uint32(p[e+8:]) != 2 || binary.LittleEndian.Uint64(p[e+20:]) != id ||
				binary.LittleEndian.Uint64(p[e+28:]) != owner {
				continue
			}
			l := binary.LittleEndian.Uint32(p[e+40:])
			if l&0x80000000 == 0 {
				continue
			}
			l &^= 0x80000000
			if l < 2 || l > 512 || e+46+int(l) > len(p) {
				continue
			}
			name := decodeU16(p[e+46 : e+46+int(l)-2])
			fe := folderEntry{id: id, owner: owner, name: name}
			for j := at - 8; j >= max(0, at-32); j-- { // nearest ID-range u64 before the entry
				v := binary.LittleEndian.Uint64(p[j:])
				if isID(v) {
					fe.account = v
					fe.kind = binary.LittleEndian.Uint32(p[j+8:])
					break
				}
			}
			out[id] = fe
		}
	})
	return out, err
}

func folderList(data []byte, r redactor, w io.Writer) error {
	fs, err := folderEntries(data)
	if err != nil {
		return err
	}
	for _, f := range fs {
		_, _ = fmt.Fprintf(w, "id=%x owner=%x account=%x kind=%d name=%s\n", f.id, f.owner, f.account, f.kind, r.show(f.name))
	}
	_, _ = fmt.Fprintf(w, "folders=%d\n", len(fs))
	return nil
}

// framesByID lists framings whose ID equals id: type, size, block key,
// block offset and whether the block is in index area A or B.
func framesByID(data []byte, id uint64, w io.Writer) error {
	li, err := readLiveIndex(data)
	if err != nil {
		return err
	}
	counts := map[string]int{}
	_, _, err = hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		forEachFraming(b.Payload, func(_ int, fid uint64, typ uint16, obj []byte) {
			if fid == id {
				counts[fmt.Sprintf("type=0x%04x size=%d blockkey=%x block@%d indexedA=%t indexedB=%t", typ, len(obj), b.Key, b.Offset,
					li.indexed(b.Offset, 0), li.indexed(b.Offset, 1))]++
			}
		})
		return nil
	})
	printCounts(w, "framings with this id", counts)
	return err
}

// dumpObject prints the first framing with this id and type, redacted.
func dumpObject(data []byte, r redactor, id uint64, typ uint16, w io.Writer) error {
	done := false
	_, _, err := hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		p := b.Payload
		for off := 0; ; {
			k := bytes.Index(p[off:], objMagic)
			if k < 0 || off+k+18 > len(p) {
				return nil
			}
			o := off + k
			off = o + 4
			n := int(binary.LittleEndian.Uint32(p[o+14:]))
			if binary.LittleEndian.Uint64(p[o+4:]) != id || binary.LittleEndian.Uint16(p[o+12:]) != typ || o+n > len(p) {
				continue
			}
			_, _ = fmt.Fprintf(w, "=== block@%d size=%d\n", b.Offset, n)
			dump(w, r, p[o:o+n], 0, 0)
			done = true
			return hxstore.ErrStop
		}
	})
	if !done {
		_, _ = fmt.Fprintln(w, "not found")
	}
	return err
}
