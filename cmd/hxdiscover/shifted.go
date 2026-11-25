package main

import (
	"encoding/binary"
	"fmt"
	"io"
)

// shifted looks at message-form 0xc7 copies whose u64 at +1166 is not an
// object ID. For each one it searches +1166±128 for a known folder ID (one
// seen at +1166 in a normal copy) and counts the deltas with the object
// size, to learn whether the field moves.
func shifted(data []byte, w io.Writer) error {
	var bad []c7copy
	folders := map[uint64]bool{}
	good := map[string]int{}
	total := 0
	if _, err := scanMessages(data, func(c c7copy, folderOK bool) {
		total++
		if folderOK {
			folders[c.folder] = true
			good[fmt.Sprintf("size=%d", len(c.obj))]++
			return
		}
		bad = append(bad, c)
	}); err != nil {
		return err
	}
	hist := map[string]int{}
	ids := map[uint64]bool{}
	for _, c := range bad {
		ids[c.id] = true
		delta := notFound
		for d := 1; d <= 128 && delta == notFound; d++ {
			for _, s := range []int{d, -d} {
				i := msgFolderAt + s
				if i >= 0 && i+8 <= len(c.obj) && folders[binary.LittleEndian.Uint64(c.obj[i:])] {
					delta = fmt.Sprintf("%+d", s)
					break
				}
			}
		}
		hist[fmt.Sprintf("size=%d delta=%s", len(c.obj), delta)]++
	}
	_, _ = fmt.Fprintf(w, "message-form copies=%d known folders=%d copies with non-ID at +1166: ids=%d\n", total, len(folders), len(ids))
	printCounts(w, "normal copies by size", good)
	printCounts(w, "non-ID copies by size and delta to a known folder ID", hist)
	return nil
}
