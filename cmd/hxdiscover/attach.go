package main

import (
	"fmt"
	"io"
	"regexp"

	"github.com/stn1slv/outlookcache/internal/hxstore"
	"github.com/stn1slv/outlookcache/internal/store"
)

// probeFileName matches the synthetic attachment names of the probes only.
var probeFileName = regexp.MustCompile(`^oc-probe-[a-z0-9-]{1,40}(\[[0-9]{1,12}\])?\.txt$`)

// attachmentCheck groups the 0x168 objects by owning item and prints
// counts: items with the has-attachments flag and how many of them have a
// named attachment object (store.ParseAttachment), plus the size field
// range. It prints names and sizes only for strict probe names.
func attachmentCheck(data []byte, w io.Writer) error {
	li, err := readLiveIndex(data)
	if err != nil {
		return err
	}
	cur, err := currentC7(data, li, false)
	if err != nil {
		return err
	}
	objs := map[uint64][][]byte{}
	if _, _, err := hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		hxstore.ForEachFraming(b.Payload, func(f hxstore.Framing) {
			if f.Type == 0x168 {
				objs[f.ID] = append(objs[f.ID], f.Data)
			}
		})
		return nil
	}); err != nil {
		return err
	}
	c := map[string]int{}
	for id, it := range cur {
		flag := it.obj[itemAttachAt]&0x40 != 0
		named := 0
		var probe []string
		for _, o := range objs[id] {
			a, ok := store.ParseAttachment(o)
			if !ok {
				continue
			}
			named++
			size := a.Size
			switch {
			case size <= 0:
				c["size field: zero or missing"]++
			case size < 200<<20:
				c["size field: 1 byte to 200 MB"]++
			default:
				c["size field: larger"]++
			}
			if probeFileName.MatchString(a.Name) {
				probe = append(probe, fmt.Sprintf("%q size=%d", a.Name, size))
			}
		}
		c[fmt.Sprintf("flag=%t, copies with a file name: %t", flag, named > 0)]++
		if len(probe) > 0 {
			_, _ = fmt.Fprintf(w, "probe item %x flag=%t %v\n", id, flag, probe)
		}
	}
	printCounts(w, "attachments (counts only)", c)
	return nil
}
