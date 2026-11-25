package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/stn1slv/outlookcache/internal/hxstore"
)

// Header fields seen in every store so far (UNVERIFIED meaning): u64 at
// 0x58 and 0x78 point to two areas of equal size that both hold
// offset/256 values of blocks.
const (
	hdrAreaA = 0x58
	hdrAreaB = 0x78
)

// refSet holds every u32 value found at any byte position in data[lo:hi].
func refSet(data []byte, lo, hi int) map[uint32]bool {
	s := map[uint32]bool{}
	for i := lo; i+4 <= hi; i++ {
		s[binary.LittleEndian.Uint32(data[i:])] = true
	}
	return s
}

// indexCheck tests whether the areas at header 0x58 and 0x78 point at the
// live copy of each message: for every message-form 0xc7 copy it checks
// whether offset/256 of its block appears in area A or area B, then counts
// per ID how many copies are referenced and whether the referenced one has
// the highest stamp.
func indexCheck(data []byte, show uint64, w io.Writer) error {
	if len(data) < 0x80 {
		return hxstore.ErrNotHxStore
	}
	a := int(binary.LittleEndian.Uint64(data[hdrAreaA:]))
	b := int(binary.LittleEndian.Uint64(data[hdrAreaB:]))
	size := b - a
	if a <= 0 || size <= 0 || b+size > len(data) {
		return fmt.Errorf("unexpected area offsets %#x %#x", a, b)
	}
	areas := []map[uint32]bool{refSet(data, a, a+size), refSet(data, b, b+size)}
	_, _ = fmt.Fprintf(w, "area A=%#x area B=%#x size=%#x\n", a, b, size)

	blocksRef := [2]int{}
	copies := map[uint64][]c7copy{}
	refd := map[int64][2]bool{}
	st, _, err := hxstore.Scan(data, "HxStore.hxd", func(blk hxstore.Block) error {
		v := uint32(blk.Offset / 256)
		r := [2]bool{blk.Offset%256 == 0 && areas[0][v], blk.Offset%256 == 0 && areas[1][v]}
		refd[blk.Offset] = r
		for i := range r {
			if r[i] {
				blocksRef[i]++
			}
		}
		p := blk.Payload
		for off := 0; ; {
			k := bytes.Index(p[off:], objMagic)
			if k < 0 || off+k+18 > len(p) {
				return nil
			}
			o := off + k
			off = o + 4
			n := int(binary.LittleEndian.Uint32(p[o+14:]))
			if binary.LittleEndian.Uint16(p[o+12:]) != 0xc7 || o+n > len(p) || n < 1800 ||
				!isID(binary.LittleEndian.Uint64(p[o+32:])) || !isID(binary.LittleEndian.Uint64(p[o+1166:])) {
				continue
			}
			var sb [8]byte
			copy(sb[:6], p[o+370:])
			id := binary.LittleEndian.Uint64(p[o+4:])
			copies[id] = append(copies[id], c7copy{
				stamp: binary.LittleEndian.Uint64(sb[:]), status: p[o+1772], flag: p[o+1742],
				folder: binary.LittleEndian.Uint64(p[o+1166:]), block: blk.Offset, key: blk.Key,
			})
		}
	})
	if err != nil {
		return err
	}
	aligned := 0
	for off := range refd {
		if off%256 == 0 {
			aligned++
		}
	}
	_, _ = fmt.Fprintf(w, "valid blocks=%d at 256-byte boundary=%d referenced in A=%d in B=%d\n", st.Valid, aligned, blocksRef[0], blocksRef[1])
	for _, c := range copies[show] {
		r := refd[c.block]
		_, _ = fmt.Fprintf(w, "copy id=%x block@%d stamp=%x status=%02x folder=%x liveA=%t liveB=%t\n", show, c.block, c.stamp, c.status, c.folder, r[0], r[1])
	}
	afterCheckpoint(copies, refd, 0, w)
	for i, name := range []string{"A", "B"} {
		hist := map[string]int{}
		unread, folders := 0, map[string]int{}
		for _, cs := range copies {
			var live []c7copy
			maxStamp := uint64(0)
			for _, c := range cs {
				maxStamp = max(maxStamp, c.stamp)
				if refd[c.block][i] {
					live = append(live, c)
				}
			}
			switch {
			case len(live) == 0:
				hist["no live copy"]++
				top := cs[0]
				for _, c := range cs {
					if c.stamp > top.stamp {
						top = c
					}
				}
				if top.status&0x02 == 0 {
					hist[fmt.Sprintf("no live copy, newest unread, folder %x", top.folder)]++
				}
				hist[fmt.Sprintf("no live copy, newest folder %x, stamp top byte %x", top.folder, top.stamp>>40)]++
				continue
			case len(live) > 1:
				hist[fmt.Sprintf("%d live copies", len(live))]++
				continue
			}
			if live[0].stamp == maxStamp {
				hist["1 live copy, has highest stamp"]++
			} else {
				hist["1 live copy, older stamp"]++
			}
			key := fmt.Sprintf("%x", live[0].folder)
			if live[0].status&0x02 == 0 {
				unread++
				key += " unread"
			}
			folders[key]++
		}
		printCounts(w, "area "+name+": message IDs by live copies", hist)
		_, _ = fmt.Fprintf(w, "area %s: unread among single live copies=%d\n", name, unread)
		printCounts(w, "area "+name+": live copies per folder", folders)
	}
	return nil
}

// afterCheckpoint reports copies newer than every indexed copy: the
// highest stamp among indexed message copies is taken as the checkpoint
// time, and IDs whose newest copy is above it are listed by folder.
func afterCheckpoint(copies map[uint64][]c7copy, refd map[int64][2]bool, area int, w io.Writer) {
	var ckpt uint64
	for _, cs := range copies {
		for _, c := range cs {
			if refd[c.block][area] {
				ckpt = max(ckpt, c.stamp)
			}
		}
	}
	hist := map[string]int{}
	for _, cs := range copies {
		top := cs[0]
		var live *c7copy
		for i, c := range cs {
			if c.stamp > top.stamp {
				top = c
			}
			if refd[c.block][area] {
				live = &cs[i]
			}
		}
		if top.stamp < ckpt || (live != nil && live.stamp == top.stamp) {
			continue
		}
		state := "no indexed copy"
		if live != nil {
			state = fmt.Sprintf("indexed copy differs=%t", live.folder != top.folder || live.status != top.status)
		}
		hist[fmt.Sprintf("newest not indexed, stamp >= checkpoint, %s, newest folder %x", state, top.folder)]++
	}
	_, _ = fmt.Fprintf(w, "checkpoint stamp=%x\n", ckpt)
	printCounts(w, "IDs with a copy newer than the checkpoint", hist)
}
