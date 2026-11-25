package main

import (
	"bytes"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/stn1slv/outlookcache/internal/hxstore"
)

// probe is a byte range relative to a marker hit.
type probe struct{ rel, n int }

func parseProbes(s string) ([]probe, error) {
	var out []probe
	for _, part := range strings.Split(s, ",") {
		a, b, ok := strings.Cut(part, ":")
		if !ok {
			return nil, fmt.Errorf("bad probe %q, want rel:len", part)
		}
		rel, err := strconv.Atoi(a)
		if err != nil {
			return nil, err
		}
		n, err := strconv.Atoi(b)
		if err != nil || n <= 0 || n > 32 {
			return nil, fmt.Errorf("bad probe length %q", b)
		}
		out = append(out, probe{rel, n})
	}
	return out, nil
}

// records counts the distinct values of binary probes at fixed offsets from
// each NUL-preceded UTF-16LE marker hit (ASCII case-insensitive). Probes
// can point anywhere, so their bytes go through safeHex, which masks text.
func records(data []byte, marker string, probes []probe, w io.Writer) error {
	needle := encodeU16(marker)
	counts := map[string]int{}
	_, _, err := hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		p := b.Payload
		low := asciiLower(p)
		for off := 0; ; {
			k := bytes.Index(low[off:], needle)
			if k < 0 {
				return nil
			}
			hit := off + k
			off = hit + 2
			if hit < 2 || p[hit-1] != 0 || p[hit-2] != 0 {
				continue
			}
			key := []string{fmt.Sprintf("@%d key=%d len=%d hdr=%s", b.Offset, b.Key, len(p), safeHex(p[:min(16, len(p))]))}
			for _, pr := range probes {
				lo, hi := hit+pr.rel, hit+pr.rel+pr.n
				if lo < 0 || hi > len(p) {
					key = append(key, "-")
					continue
				}
				key = append(key, safeHex(p[lo:hi]))
			}
			counts[strings.Join(key, " ")]++
		}
	})
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return counts[keys[i]] > counts[keys[j]] })
	for _, k := range keys {
		_, _ = fmt.Fprintf(w, "%5d  %s\n", counts[k], k)
	}
	return err
}
