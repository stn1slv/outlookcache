package main

import (
	"bytes"
	"fmt"
	"io"

	"github.com/stn1slv/outlookcache/internal/hxstore"
)

// window is the part of a payload around the first marker hit.
type window struct {
	block int64
	hit   int
	data  []byte // payload[hit-span : hit+span], clipped
	lo    int    // offset of data[0] relative to the hit (<= 0)
}

const diffSpan = 4096

func windows(data []byte, marker string, key int64) ([]window, error) {
	needle := encodeU16(marker)
	var out []window
	_, _, err := hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		if key >= 0 && b.Key != uint64(key) {
			return nil
		}
		k := bytes.Index(asciiLower(b.Payload), needle)
		if k < 0 {
			return nil
		}
		lo, hi := max(0, k-diffSpan), min(len(b.Payload), k+diffSpan)
		out = append(out, window{block: b.Offset, hit: k, data: b.Payload[lo:hi], lo: lo - k})
		return nil
	})
	return out, err
}

// diffSnapshots reports, for every marker window in b that has no identical
// window in a, the byte ranges that differ from the most similar window in a.
func diffSnapshots(a, b []byte, r redactor, key int64, w io.Writer) error {
	wa, err := windows(a, r.marker, key)
	if err != nil {
		return err
	}
	wb, err := windows(b, r.marker, key)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "windows: a=%d b=%d\n", len(wa), len(wb))
	seen := map[string]bool{}
	for _, x := range wa {
		seen[string(x.data)] = true
	}
	for _, y := range wb {
		if seen[string(y.data)] {
			continue
		}
		best, bestN := -1, 1<<30
		for i, x := range wa {
			if n := countDiff(x, y); n < bestN {
				best, bestN = i, n
			}
		}
		if best < 0 {
			_, _ = fmt.Fprintf(w, "\n### new window block@%d (no counterpart)\n", y.block)
			continue
		}
		x := wa[best]
		_, _ = fmt.Fprintf(w, "\n### block@%d vs a:block@%d, %d differing bytes, b.len=%d a.len=%d\n", y.block, x.block, bestN, len(y.data), len(x.data))
		printRanges(w, r, x, y)
	}
	return nil
}

func at(x window, rel int) (byte, bool) {
	i := rel - x.lo
	if i < 0 || i >= len(x.data) {
		return 0, false
	}
	return x.data[i], true
}

func countDiff(x, y window) int {
	n := 0
	for rel := y.lo; rel < y.lo+len(y.data); rel++ {
		cx, okx := at(x, rel)
		cy, _ := at(y, rel)
		if !okx || cx != cy {
			n++
		}
	}
	return n
}

func printRanges(w io.Writer, r redactor, x, y window) {
	shown := 0
	for rel := y.lo; rel < y.lo+len(y.data) && shown < 40; {
		cx, okx := at(x, rel)
		cy, _ := at(y, rel)
		if okx && cx == cy {
			rel++
			continue
		}
		start := rel
		for rel < y.lo+len(y.data) {
			cx, okx = at(x, rel)
			cy, _ = at(y, rel)
			if okx && cx == cy {
				break
			}
			rel++
		}
		ctxLo := start - 8
		var xa, ya []byte
		for i := ctxLo; i < min(rel, start+24); i++ {
			if c, ok := at(x, i); ok {
				xa = append(xa, c)
			}
			if c, ok := at(y, i); ok {
				ya = append(ya, c)
			}
		}
		_, _ = fmt.Fprintf(w, "  %+6d len=%d\n    a: %s%s\n    b: %s%s\n", start, rel-start, safeHex(xa), note(r, xa), safeHex(ya), note(r, ya))
		shown++
	}
}

// safeHex prints bytes as hex with every text span masked.
func safeHex(b []byte) string { return maskHex(b) }

func note(r redactor, b []byte) string {
	for j := range b {
		if n, ok := r.tickNote(b[j:]); ok {
			return fmt.Sprintf("  (+%d %s)", j, n)
		}
	}
	return ""
}
