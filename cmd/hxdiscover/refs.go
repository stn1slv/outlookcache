package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/stn1slv/outlookcache/internal/hxstore"
)

// blockRefs searches for references to block offsets: for each offset it
// counts, in the raw file and in decoded payloads, the u32 and u64 encodings
// of the offset itself and of offset/4096, offset/512 and offset/16. It
// helps to find an index that points at the live copy of an object.
func blockRefs(data []byte, offs []uint64, w io.Writer) error {
	var payloads [][]byte
	_, _, err := hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		payloads = append(payloads, b.Payload)
		return nil
	})
	if err != nil {
		return err
	}
	count := bytes.Count
	for _, off := range offs {
		for _, unit := range []uint64{1, 16, 512, 4096} {
			if off%unit != 0 {
				continue
			}
			v := off / unit
			var b4 [4]byte
			var b8 [8]byte
			binary.LittleEndian.PutUint32(b4[:], uint32(v))
			binary.LittleEndian.PutUint64(b8[:], v)
			raw4, raw8, pl4, pl8 := count(data, b4[:]), count(data, b8[:]), 0, 0
			for _, p := range payloads {
				pl4 += count(p, b4[:])
				pl8 += count(p, b8[:])
			}
			_, _ = fmt.Fprintf(w, "offset=%d unit=%d value=%x raw_u32=%d raw_u64=%d payload_u32=%d payload_u64=%d\n",
				off, unit, v, raw4, raw8, pl4, pl8)
		}
	}
	return nil
}

// parseOffsets parses a comma-separated list of decimal file offsets.
func parseOffsets(list string) ([]uint64, error) {
	var offs []uint64
	for _, s := range strings.Split(list, ",") {
		v, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("bad offset %q: %w", s, err)
		}
		offs = append(offs, v)
	}
	return offs, nil
}
