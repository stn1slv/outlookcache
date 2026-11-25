package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/stn1slv/outlookcache/internal/hxstore"
)

// slots takes pairs "folder:owner,folder:owner" of folders with the same
// role in different accounts (for example both Inboxes). For each pair it
// collects the positions (framing type, offset in framing) where the folder
// ID appears inside framings whose ID is in the owner set. Positions shared
// by all pairs are candidates for a fixed role slot.
func slots(data []byte, spec string, w io.Writer) error {
	type pair struct{ folder, owner uint64 }
	var pairs []pair
	for _, s := range strings.Split(spec, ",") {
		f, o, ok := strings.Cut(strings.TrimSpace(s), ":")
		if !ok {
			return fmt.Errorf("bad pair %q, want folder:owner in hex", s)
		}
		fv, err1 := strconv.ParseUint(f, 16, 64)
		ov, err2 := strconv.ParseUint(o, 16, 64)
		if err1 != nil || err2 != nil {
			return fmt.Errorf("bad pair %q", s)
		}
		pairs = append(pairs, pair{fv, ov})
	}
	found := make([]map[string]bool, len(pairs))
	for i := range found {
		found[i] = map[string]bool{}
	}
	_, _, err := hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		p := b.Payload
		for off := 0; ; {
			k := bytes.Index(p[off:], objMagic)
			if k < 0 || off+k+18 > len(p) {
				return nil
			}
			o := off + k
			off = o + 4
			id := binary.LittleEndian.Uint64(p[o+4:])
			n := int(binary.LittleEndian.Uint32(p[o+14:]))
			if n < 18 || o+n > len(p) {
				continue
			}
			typ := binary.LittleEndian.Uint16(p[o+12:])
			obj := p[o : o+n]
			for i, pr := range pairs {
				if id != pr.owner {
					continue
				}
				var pat [8]byte
				binary.LittleEndian.PutUint64(pat[:], pr.folder)
				for j := 0; ; {
					h := bytes.Index(obj[j:], pat[:])
					if h < 0 {
						break
					}
					found[i][fmt.Sprintf("type=0x%04x size=%d +%d", typ, n, j+h)] = true
					j += h + 1
				}
			}
		}
	})
	if err != nil {
		return err
	}
	for i, pr := range pairs {
		_, _ = fmt.Fprintf(w, "pair %x:%x positions=%d\n", pr.folder, pr.owner, len(found[i]))
	}
	// Compare by type and offset only, since sizes differ per account.
	strip := func(s string) string {
		t, rest, _ := strings.Cut(s, " size=")
		_, off, _ := strings.Cut(rest, " ")
		return t + " " + off
	}
	common := map[string]int{}
	for i := range pairs {
		seen := map[string]bool{}
		for s := range found[i] {
			seen[strip(s)] = true
		}
		for s := range seen {
			common[s]++
		}
	}
	var out []string
	for s, c := range common {
		if c == len(pairs) {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	_, _ = fmt.Fprintf(w, "positions shared by all pairs: %d\n", len(out))
	for _, s := range out {
		_, _ = fmt.Fprintln(w, " ", s)
	}
	return nil
}

// idSlots prints every object-ID-range u64 in the last framing with this id
// and type, with its offset.
func idSlots(data []byte, id uint64, typ uint16, w io.Writer) error {
	var last []byte
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
			if binary.LittleEndian.Uint64(p[o+4:]) == id && binary.LittleEndian.Uint16(p[o+12:]) == typ && n >= 18 && o+n <= len(p) {
				last = p[o : o+n]
			}
		}
	})
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "size=%d\n", len(last))
	for i := 0; i+8 <= len(last); i++ {
		if v := binary.LittleEndian.Uint64(last[i:]); isID(v) {
			_, _ = fmt.Fprintf(w, "+%d %x\n", i, v)
			i += 7
		}
	}
	return nil
}

// framesIn lists the framings of the block at this file offset.
func framesIn(data []byte, blockOff int64, w io.Writer) error {
	_, _, err := hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		if b.Offset != blockOff {
			return nil
		}
		p := b.Payload
		_, _ = fmt.Fprintf(w, "block@%d key=%x type=%d payload=%d\n", b.Offset, b.Key, b.Type, len(p))
		for off := 0; ; {
			k := bytes.Index(p[off:], objMagic)
			if k < 0 || off+k+18 > len(p) {
				return hxstore.ErrStop
			}
			o := off + k
			off = o + 4
			_, _ = fmt.Fprintf(w, "  @%d id=%x type=0x%04x size=%d\n", o, binary.LittleEndian.Uint64(p[o+4:]),
				binary.LittleEndian.Uint16(p[o+12:]), binary.LittleEndian.Uint32(p[o+14:]))
		}
	})
	return err
}

// payloadHead dumps the first n payload bytes of the block at this offset,
// redacted.
func payloadHead(data []byte, r redactor, blockOff int64, n int, w io.Writer) error {
	_, _, err := hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		if b.Offset != blockOff {
			return nil
		}
		dump(w, r, b.Payload[:min(n, len(b.Payload))], 0, 0)
		return hxstore.ErrStop
	})
	return err
}

// refSlots lists object references in the unframed head of a block (before
// the first framing): [type u16][8 zero bytes][id][u32 2][owner][id][owner].
func refSlots(data []byte, blockOff int64, w io.Writer) error {
	_, _, err := hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		if b.Offset != blockOff {
			return nil
		}
		p := b.Payload
		end := bytes.Index(p, objMagic)
		if end < 0 {
			end = len(p)
		}
		for i := 0; i+46 <= end; i++ {
			id := binary.LittleEndian.Uint64(p[i+10:])
			if !isID(id) || binary.LittleEndian.Uint32(p[i+18:]) != 2 || binary.LittleEndian.Uint64(p[i+30:]) != id ||
				binary.LittleEndian.Uint64(p[i+22:]) != binary.LittleEndian.Uint64(p[i+38:]) {
				continue
			}
			_, _ = fmt.Fprintf(w, "+%d type=0x%04x id=%x owner=%x\n", i, binary.LittleEndian.Uint16(p[i:]), id,
				binary.LittleEndian.Uint64(p[i+22:]))
		}
		return hxstore.ErrStop
	})
	return err
}

// entryRefs lists named folder entries in blocks keyed by key: folder ID,
// name length in characters (the name itself is never printed) and the
// object-ID-range u64 values in the 200 bytes after the name, relative to
// the end of the name.
func entryRefs(data []byte, key uint64, w io.Writer) error {
	seen := map[string]bool{}
	pat := []byte{0x4d, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	_, _, err := hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		if key != 0 && b.Key != key {
			return nil
		}
		p := b.Payload
		for off := 0; ; {
			k := bytes.Index(p[off:], pat)
			if k < 0 {
				return nil
			}
			e := off + k + len(pat)
			off = e
			if e+46 > len(p) {
				continue
			}
			id := binary.LittleEndian.Uint64(p[e:])
			l := binary.LittleEndian.Uint32(p[e+40:])
			if !isID(id) || binary.LittleEndian.Uint32(p[e+8:]) != 2 || binary.LittleEndian.Uint64(p[e+20:]) != id || l&0x80000000 == 0 {
				continue
			}
			l &^= 0x80000000
			end := e + 46 + int(l)
			if l < 2 || end > len(p) {
				continue
			}
			var refs []string
			for i := end; i+8 <= min(len(p), end+200); i++ {
				if v := binary.LittleEndian.Uint64(p[i:]); isID(v) {
					refs = append(refs, fmt.Sprintf("+%d:%x", i-end, v))
					i += 7
				}
			}
			line := fmt.Sprintf("blockkey=%x id=%x owner=%x name_chars=%d refs=%v", b.Key, id, binary.LittleEndian.Uint64(p[e+12:]), (l-2)/2, refs)
			if !seen[line] {
				seen[line] = true
				_, _ = fmt.Fprintln(w, line)
			}
		}
	})
	return err
}
