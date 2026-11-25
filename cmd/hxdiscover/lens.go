package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"sort"
)

// stringLengthFields searches the fixed part of items for fields that hold
// the length of the k-th string of the string run (in characters, bytes or
// bytes+2), as u16 or u32. It prints, per string index, the offsets with
// the highest hit rate. Only offsets and counts are printed.
func stringLengthFields(data []byte, w io.Writer) error {
	li, err := readLiveIndex(data)
	if err != nil {
		return err
	}
	cur, err := currentC7(data, li, false)
	if err != nil {
		return err
	}
	type key struct{ k, off, unit int }
	hits := map[key]int{}
	n := 0
	for _, it := range cur {
		var anchor []byte
		anchor = binary.LittleEndian.AppendUint64(anchor, it.id)
		anchor = append(anchor, 0, 0, 0xc7, 0, 0, 0, 0, 0, 0, 0, 0, 0)
		anchor = binary.LittleEndian.AppendUint64(anchor, it.conv)
		a := bytes.Index(it.obj, anchor)
		if a < 0 {
			continue
		}
		s, ok := itemStrings(it.obj, it.id, it.conv)
		if !ok {
			continue
		}
		n++
		for k := 0; k < min(len(s), 8); k++ {
			chars := len(encodeU16(s[k])) / 2
			if chars < 3 {
				continue // too common as a value
			}
			for off := 0; off+2 <= a; off++ {
				v := int(binary.LittleEndian.Uint16(it.obj[off:]))
				switch v {
				case chars:
					hits[key{k, off, 1}]++
				case 2 * chars:
					hits[key{k, off, 2}]++
				case 2*chars + 2:
					hits[key{k, off, 3}]++
				}
			}
		}
	}
	_, _ = fmt.Fprintf(w, "items=%d\n", n)
	keys := make([]key, 0, len(hits))
	for k := range hits {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return hits[keys[i]] > hits[keys[j]] })
	shown := map[int]int{}
	units := map[int]string{1: "chars", 2: "bytes", 3: "bytes+2"}
	for _, k := range keys {
		if shown[k.k] < 3 && hits[k] >= 50 {
			shown[k.k]++
			_, _ = fmt.Fprintf(w, "string %d: +%d as %s hits=%d\n", k.k, k.off, units[k.unit], hits[k])
		}
	}
	return nil
}

// probeLengths prints, for one probe item, each string's index, length in
// bytes+2 and the fixed-part offsets whose u16 equals that value. Strings
// are not printed (the subject is synthetic, the rest is real).
func probeLengths(data []byte, target uint64, w io.Writer) error {
	li, err := readLiveIndex(data)
	if err != nil {
		return err
	}
	cur, err := currentC7(data, li, false)
	if err != nil {
		return err
	}
	it, ok := cur[target]
	if !ok || !probeSubject(it.obj) {
		return fmt.Errorf("no current probe item %x", target)
	}
	var anchor []byte
	anchor = binary.LittleEndian.AppendUint64(anchor, it.id)
	anchor = append(anchor, 0, 0, 0xc7, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	anchor = binary.LittleEndian.AppendUint64(anchor, it.conv)
	a := bytes.Index(it.obj, anchor)
	s, _ := itemStrings(it.obj, it.id, it.conv)
	for k, v := range s {
		n := len(encodeU16(v)) + 2
		var offs []int
		for off := 1400; off+2 <= a; off++ {
			if int(binary.LittleEndian.Uint16(it.obj[off:])) == n {
				offs = append(offs, off)
			}
		}
		_, _ = fmt.Fprintf(w, "string %d bytes+2=%d at %v\n", k, n, offs)
	}
	return nil
}

// lengthTableCheck tests the length table on all items: for each property
// offset it counts items where the u16 there is absent (0 or 2), matches
// exactly one string of the run (bytes+2), matches several, or none.
func lengthTableCheck(data []byte, w io.Writer) error {
	li, err := readLiveIndex(data)
	if err != nil {
		return err
	}
	cur, err := currentC7(data, li, false)
	if err != nil {
		return err
	}
	props := []struct {
		name string
		off  int
	}{
		{"sender address", 1648},
		{"sender name", 1640},
		{"+1636", 1636},
		{"+1644", 1644},
		{"item class", 1488},
		{"Message-ID", 1480},
		{"preview", 1556},
		{"subject", 1512},
		{"+1504", 1504},
		{"topic", 1684},
		{"+1668", 1668},
		{"+1500", 1500},
	}
	c := map[string]int{}
	for _, it := range cur {
		s, ok := itemStrings(it.obj, it.id, it.conv)
		if !ok {
			continue
		}
		for _, p := range props {
			v := int(binary.LittleEndian.Uint16(it.obj[p.off:]))
			m := 0
			for _, x := range s {
				if len(encodeU16(x))+2 == v {
					m++
				}
			}
			switch {
			case v <= 2:
				c[p.name+": absent"]++
			case m == 1:
				c[p.name+": one match"]++
			case m > 1:
				c[p.name+": several matches"]++
			default:
				c[p.name+": no match"]++
			}
		}
	}
	printCounts(w, "length table check (counts only)", c)
	return nil
}

// subjectGap prints, for items whose subject length field matches no
// string, the difference between the field and the length of the last
// string of the run (in UTF-16 units), and whether that string starts with
// a short prefix ending in ": ". Counts only.
func subjectGap(data []byte, w io.Writer) error {
	li, err := readLiveIndex(data)
	if err != nil {
		return err
	}
	cur, err := currentC7(data, li, false)
	if err != nil {
		return err
	}
	c := map[string]int{}
	for _, it := range cur {
		s, ok := itemStrings(it.obj, it.id, it.conv)
		if !ok {
			continue
		}
		v := int(binary.LittleEndian.Uint16(it.obj[1512:]))
		match := false
		for _, x := range s {
			if len(encodeU16(x))+2 == v {
				match = true
			}
		}
		if match || v <= 2 {
			continue
		}
		classAt := -1
		for k, x := range s {
			if len(x) > 4 && (x[:4] == "IPM." || len(x) > 7 && x[:7] == "REPORT.") {
				classAt = k
				break
			}
		}
		best := 1 << 30
		for k := classAt + 1; k < len(s) && classAt >= 0; k++ {
			d := v/2 - 1 - len(encodeU16(s[k]))/2
			if d < 0 {
				d = -d
			}
			best = min(best, d)
		}
		c[fmt.Sprintf("closest string after class differs by %d units", min(best, 20))]++
		if v > 1024 {
			c["field > 1024 bytes"]++
		}
	}
	printCounts(w, "subject length field without a matching string", c)
	return nil
}
