package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/stn1slv/outlookcache/internal/hxstore"
)

// fieldCompare takes the last folder (class 0x4d) framing of type typ for every ID (framing ID
// equal to the block key, so the object is the main one of its block) and
// prints one line per ID with the u16 values at the offsets that differ
// between IDs. Text spans, and any single u16 that decodes as a letter or
// digit in any script (short names, CJK), are printed as "txt".
func fieldCompare(data []byte, typ uint16, w io.Writer) error {
	objs := map[uint64][]byte{}
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
			if binary.LittleEndian.Uint16(p[o+12:]) != typ || id != b.Key || n < 34 || o+n > len(p) || binary.LittleEndian.Uint16(p[o+28:]) != 0x4d {
				continue
			}
			objs[id] = p[o : o+n]
		}
	})
	if err != nil {
		return err
	}
	ids := make([]uint64, 0, len(objs))
	minLen := -1
	for id, o := range objs {
		ids = append(ids, id)
		if minLen < 0 || len(o) < minLen {
			minLen = len(o)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	masks := map[uint64][]bool{}
	for _, id := range ids {
		masks[id] = textMask(objs[id])
	}
	var offs []int
	for i := 18; i+2 <= minLen; i += 2 {
		first := binary.LittleEndian.Uint16(objs[ids[0]][i:])
		for _, id := range ids[1:] {
			if binary.LittleEndian.Uint16(objs[id][i:]) != first {
				offs = append(offs, i)
				break
			}
		}
	}
	_, _ = fmt.Fprintf(w, "type=0x%x ids=%d min_size=%d differing u16 offsets=%v\n", typ, len(ids), minLen, offs)
	for _, id := range ids {
		o, m := objs[id], masks[id]
		var sb strings.Builder
		for _, i := range offs {
			if m[i] || m[i+1] || textUnit(binary.LittleEndian.Uint16(o[i:])) {
				sb.WriteString(" txt")
				continue
			}
			_, _ = fmt.Fprintf(&sb, " %04x", binary.LittleEndian.Uint16(o[i:]))
		}
		_, _ = fmt.Fprintf(w, "%x size=%d%s\n", id, len(o), sb.String())
	}
	return nil
}

// textUnit reports a UTF-16 code unit that could be part of a name.
func textUnit(v uint16) bool {
	r := rune(v)
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// typeSizes prints, for each given type, how many IDs have a last copy of
// each size class (empty = the smallest size seen for that type).
func typeSizes(data []byte, types []uint16, w io.Writer) error {
	last := map[uint16]map[uint64]int{}
	for _, t := range types {
		last[t] = map[uint64]int{}
	}
	_, _, err := hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		forEachFraming(b.Payload, func(_ int, id uint64, typ uint16, obj []byte) {
			if m, ok := last[typ]; ok {
				m[id] = len(obj)
			}
		})
		return nil
	})
	if err != nil {
		return err
	}
	for _, t := range types {
		minSize := -1
		for _, n := range last[t] {
			if minSize < 0 || n < minSize {
				minSize = n
			}
		}
		empty := 0
		for _, n := range last[t] {
			if n == minSize {
				empty++
			}
		}
		_, _ = fmt.Fprintf(w, "type=0x%04x ids=%d smallest size=%d ids with smallest size=%d\n", t, len(last[t]), minSize, empty)
	}
	return nil
}

// byteCandidates compares one object (the last 0xc7 copy with this ID)
// with the last copy of every other 0xc7 object of the same form (with or
// without an account ID at +32) and prints byte offsets where the target
// differs from a value that at least 80% of the population share. Only
// offsets, byte values and shares are printed.
func byteCandidates(data []byte, target uint64, limit int, w io.Writer) error {
	last := map[uint64][]byte{}
	_, _, err := hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		forEachFraming(b.Payload, func(_ int, id uint64, typ uint16, obj []byte) {
			if typ == 0xc7 && len(obj) >= msgMinLen {
				last[id] = obj
			}
		})
		return nil
	})
	if err != nil {
		return err
	}
	t, ok := last[target]
	if !ok {
		return fmt.Errorf("no 0xc7 object with id %x", target)
	}
	form := isID(binary.LittleEndian.Uint64(t[msgAccountAt:]))
	var pop [][]byte
	for id, o := range last {
		if id != target && isID(binary.LittleEndian.Uint64(o[msgAccountAt:])) == form {
			pop = append(pop, o)
		}
	}
	mask := textMask(t)
	_, _ = fmt.Fprintf(w, "target form message=%t population=%d\n", form, len(pop))
	for i := 0; i < min(limit, len(t)); i++ {
		if mask[i] {
			continue
		}
		var hist [256]int
		n := 0
		for _, o := range pop {
			if i < len(o) {
				hist[o[i]]++
				n++
			}
		}
		top := 0
		for v := range hist {
			if hist[v] > hist[top] {
				top = v
			}
		}
		if n > 0 && hist[top]*5 >= n*4 && byte(top) != t[i] {
			_, _ = fmt.Fprintf(w, "+%d target=%02x majority=%02x share=%.3f population with target value=%d\n",
				i, t[i], top, float64(hist[top])/float64(n), hist[t[i]])
		}
	}
	return nil
}

// probeTicks prints the offsets and exact UTC times of all u64 values in
// the last copy of one object that decode as .NET ticks within the last 90
// days. It refuses objects that do not contain the synthetic marker, so
// only times of probe mails are printed.
func probeTicks(data []byte, target uint64, typ uint16, now time.Time, w io.Writer) error {
	var obj []byte
	_, _, err := hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		forEachFraming(b.Payload, func(_ int, id uint64, t uint16, o []byte) {
			if id == target && t == typ {
				obj = o
			}
		})
		return nil
	})
	if err != nil {
		return err
	}
	if obj == nil {
		return fmt.Errorf("no object %x of type %#x", target, typ)
	}
	if !bytes.Contains(asciiLower(obj), encodeU16(markerPrefix)) {
		return errors.New("object has no oc-probe marker; refusing to print times")
	}
	for i := 0; i+8 <= len(obj); i++ {
		v := binary.LittleEndian.Uint64(obj[i:])
		if v < ticksUnixEpoch {
			continue
		}
		ts := ticksToTime(v)
		if ts.After(now.AddDate(0, 0, -90)) && ts.Before(now.Add(time.Hour)) {
			_, _ = fmt.Fprintf(w, "+%d %s\n", i, ts.UTC().Format(time.RFC3339Nano))
		}
	}
	return nil
}
