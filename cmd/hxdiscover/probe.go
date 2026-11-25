package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/stn1slv/outlookcache/internal/hxstore"
)

// typeSizes prints, for each given type, the number of IDs, the smallest
// object size and how many IDs have an object of that size. Per ID it uses
// a copy from an indexed block when there is one, else the last copy in
// file order. "Smallest size" is read as "empty list" only as a heuristic:
// a type whose objects all have one size may be constant, not empty.
func typeSizes(data []byte, types []uint16, w io.Writer) error {
	li, err := readLiveIndex(data)
	if err != nil {
		return err
	}
	type pick struct {
		size    int
		indexed bool
	}
	last := map[uint16]map[uint64]pick{}
	for _, t := range types {
		last[t] = map[uint64]pick{}
	}
	if _, _, err := hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		forEachFraming(b.Payload, func(_ int, id uint64, typ uint16, obj []byte) {
			m, ok := last[typ]
			if !ok {
				return
			}
			idx := li.indexed(b.Offset, 0)
			if old, seen := m[id]; !seen || idx || !old.indexed {
				m[id] = pick{len(obj), idx}
			}
		})
		return nil
	}); err != nil {
		return err
	}
	for _, t := range types {
		if len(last[t]) == 0 {
			_, _ = fmt.Fprintf(w, "type=0x%04x no objects\n", t)
			continue
		}
		minSize, atMin, sizes := -1, 0, map[int]bool{}
		for _, p := range last[t] {
			sizes[p.size] = true
			if minSize < 0 || p.size < minSize {
				minSize = p.size
			}
		}
		for _, p := range last[t] {
			if p.size == minSize {
				atMin++
			}
		}
		_, _ = fmt.Fprintf(w, "type=0x%04x ids=%d distinct sizes=%d smallest size=%d ids with smallest size=%d\n",
			t, len(last[t]), len(sizes), minSize, atMin)
	}
	return nil
}

// currentC7 returns the current copy (liveIndex.current) of every 0xc7
// object of one form: conversations (account ID at +32) or items.
func currentC7(data []byte, li liveIndex, conversations bool) (map[uint64]c7copy, error) {
	copies := map[uint64][]c7copy{}
	add := func(c c7copy, folderOK bool) {
		if folderOK {
			copies[c.id] = append(copies[c.id], c)
		}
	}
	var err error
	if conversations {
		_, err = scanConversations(data, add)
	} else {
		err = scanItems(data, add)
	}
	if err != nil {
		return nil, err
	}
	out := make(map[uint64]c7copy, len(copies))
	for id, cs := range copies {
		c, _, _ := li.current(cs)
		out[id] = c
	}
	return out, nil
}

// probeSubject reports whether obj holds a UTF-16 string that starts with
// the synthetic marker, optionally after a reply or forward prefix.
func probeSubject(obj []byte) bool {
	for _, t := range tokenize(obj) {
		if t.kind != tokU16 {
			continue
		}
		s := strings.ToLower(t.text)
		for _, p := range []string{"", "re: ", "fw: ", "fwd: "} {
			if strings.HasPrefix(s, p+markerPrefix) {
				return true
			}
		}
	}
	return false
}

// byteCandidates compares the current copy of one probe object (0xc7, its
// subject starts with the marker) with the current copies of all other
// objects of the same form, and prints offsets where the target differs
// from a value that at least 80% of at least minSample objects share. At
// every offset, objects with text there (textMask or a letter/digit UTF-16
// unit) are left out, so only binary fields are compared.
func byteCandidates(data []byte, target uint64, limit int, w io.Writer) error {
	const minSample = 100
	li, err := readLiveIndex(data)
	if err != nil {
		return err
	}
	for _, conversations := range []bool{true, false} {
		cur, err := currentC7(data, li, conversations)
		if err != nil {
			return err
		}
		t, ok := cur[target]
		if !ok {
			continue
		}
		if !probeSubject(t.obj) {
			return errors.New("target is not a probe message (no subject starting with the oc-probe marker)")
		}
		type masked struct {
			b []byte
			m []bool
		}
		pop := make([]masked, 0, len(cur))
		for id, c := range cur {
			if id != target {
				pop = append(pop, masked{c.obj, textMask(c.obj)})
			}
		}
		tm := textMask(t.obj)
		_, _ = fmt.Fprintf(w, "target conversation=%t population=%d\n", conversations, len(pop))
		for i := 0; i < min(limit, len(t.obj)); i++ {
			if tm[i] || i+1 < len(t.obj) && textUnit(binary.LittleEndian.Uint16(t.obj[i&^1:])) {
				continue
			}
			var hist [256]int
			n := 0
			for _, o := range pop {
				if i < len(o.b) && !o.m[i] && (i|1 >= len(o.b) || !textUnit(binary.LittleEndian.Uint16(o.b[i&^1:]))) {
					hist[o.b[i]]++
					n++
				}
			}
			top := 0
			for v := range hist {
				if hist[v] > hist[top] {
					top = v
				}
			}
			if n >= minSample && hist[top]*5 >= n*4 && byte(top) != t.obj[i] {
				_, _ = fmt.Fprintf(w, "+%d target=%02x majority=%02x share=%.3f n=%d with target value=%d\n",
					i, t.obj[i], top, float64(hist[top])/float64(n), n, hist[t.obj[i]])
			}
		}
		return nil
	}
	return fmt.Errorf("no current 0xc7 object with id %x", target)
}

// probeTicks prints the offsets and exact UTC times of u64 values that
// decode as .NET ticks within the last 7 days, in the current copy of one
// 0xc7 probe object (its subject starts with the marker). It needs the
// -ticks opt-in, so exact times are printed only on request.
func probeTicks(data []byte, target uint64, showTicks bool, now time.Time, w io.Writer) error {
	if !showTicks {
		return errors.New("ticks prints exact times; add -ticks to confirm")
	}
	li, err := readLiveIndex(data)
	if err != nil {
		return err
	}
	from, to := now.AddDate(0, 0, -7), now.Add(time.Hour)
	for _, conversations := range []bool{true, false} {
		cur, err := currentC7(data, li, conversations)
		if err != nil {
			return err
		}
		c, ok := cur[target]
		if !ok {
			continue
		}
		if !probeSubject(c.obj) {
			return errors.New("target is not a probe message (no subject starting with the oc-probe marker); refusing to print times")
		}
		_, _ = fmt.Fprintf(w, "conversation=%t\n", conversations)
		for i := 0; i+8 <= len(c.obj); i++ {
			v := binary.LittleEndian.Uint64(c.obj[i:])
			if v < ticksUnixEpoch {
				continue
			}
			if ts := ticksToTime(v); ts.After(from) && ts.Before(to) {
				_, _ = fmt.Fprintf(w, "+%d %s\n", i, ts.UTC().Format(time.RFC3339Nano))
			}
		}
		return nil
	}
	return fmt.Errorf("no current 0xc7 object with id %x", target)
}

// bytecandLimit bounds the compared prefix of a 0xc7 object: the fixed-layout
// fields end before the first strings at about +1900.
const bytecandLimit = 1900
