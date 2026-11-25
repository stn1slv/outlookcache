package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/stn1slv/outlookcache/internal/hxstore"
)

var fileNameRe = regexp.MustCompile(`^[^/\\:*?"<>|@]{1,200}\.[A-Za-z0-9]{1,8}$`)

const attachSizeAt = 670 // u32 in a 0x168 object (one probe; see docs)

// attachmentName returns the first UTF-16 string in a 0x168 object that
// looks like a file name and starts right after a sort-key terminator
// (a 0x00 byte after a non-zero byte).
func attachmentName(obj []byte) (string, bool) {
	for _, t := range tokenize(obj) {
		if t.kind != tokU16 || !fileNameRe.MatchString(t.text) || t.off < 2 {
			continue
		}
		if obj[t.off-1] == 0 && obj[t.off-2] != 0 {
			return t.text, true
		}
	}
	return "", false
}

// attachmentCheck groups the 0x168 objects by owning item and prints
// counts: items with the has-attachments flag and how many of them have a
// named attachment object, plus the size field range. For probe items it
// prints the names (synthetic) and sizes.
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
		forEachFraming(b.Payload, func(_ int, id uint64, typ uint16, obj []byte) {
			if typ == 0x168 {
				objs[id] = append(objs[id], obj)
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
			name, ok := attachmentName(o)
			if !ok {
				continue
			}
			named++
			size := -1
			if len(o) >= attachSizeAt+4 {
				size = int(binary.LittleEndian.Uint32(o[attachSizeAt:]))
			}
			switch {
			case size <= 0:
				c["size field: zero or missing"]++
			case size < 200<<20:
				c["size field: 1 byte to 200 MB"]++
			default:
				c["size field: larger"]++
			}
			if strings.HasPrefix(strings.ToLower(name), markerPrefix) {
				probe = append(probe, fmt.Sprintf("%s size=%d", name, size))
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
