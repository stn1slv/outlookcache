package main

import (
	"fmt"
	"io"
)

// indexCheck tests the candidate live-block index: for every message-form
// 0xc7 copy it checks whether offset/256 of its block appears in area A or
// area B, then counts per ID how many copies are indexed and whether the
// indexed one has the highest stamp. With show != 0 it lists every copy of
// that message ID.
func indexCheck(data []byte, show uint64, w io.Writer) error {
	li, err := readLiveIndex(data)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "area A=%#x area B=%#x size=%#x\n", li.start, li.start+li.size, li.size)

	blocks, aligned, refd := map[int64]bool{}, 0, [2]int{}
	copies := map[uint64][]c7copy{}
	other := 0
	st, err := scanMessages(data, func(c c7copy, folderOK bool) {
		if !folderOK {
			other++
			return
		}
		copies[c.id] = append(copies[c.id], c)
		if !blocks[c.block] {
			blocks[c.block] = true
			if c.block%256 == 0 {
				aligned++
			}
			for i := range refd {
				if li.indexed(c.block, i) {
					refd[i]++
				}
			}
		}
	})
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "valid blocks=%d blocks with message copies=%d at 256-byte boundary=%d indexed in A=%d in B=%d other layout copies (skipped)=%d\n",
		st.Valid, len(blocks), aligned, refd[0], refd[1], other)
	if show != 0 {
		for _, c := range copies[show] {
			_, _ = fmt.Fprintf(w, "copy id=%x block@%d stamp=%x status=%02x folder=%x indexedA=%t indexedB=%t\n",
				show, c.block, c.stamp, c.status, c.folder, li.indexed(c.block, 0), li.indexed(c.block, 1))
		}
	}
	for area, name := range []string{"A", "B"} {
		afterCheckpoint(copies, li, area, name, w)
		hist := map[string]int{}
		unread, folders := 0, map[string]int{}
		for _, cs := range copies {
			var live []c7copy
			maxStamp := uint64(0)
			for _, c := range cs {
				maxStamp = max(maxStamp, c.stamp)
				if li.indexed(c.block, area) {
					live = append(live, c)
				}
			}
			switch {
			case len(live) == 0:
				hist["no indexed copy"]++
				continue
			case len(live) > 1:
				hist[fmt.Sprintf("%d indexed copies", len(live))]++
				continue
			case live[0].stamp == maxStamp:
				hist["1 indexed copy, has highest stamp"]++
			default:
				hist["1 indexed copy, older stamp"]++
			}
			key := fmt.Sprintf("%x", live[0].folder)
			if live[0].status&0x02 == 0 {
				unread++
				key += " unread"
			}
			folders[key]++
		}
		printCounts(w, "area "+name+": message IDs by indexed copies", hist)
		_, _ = fmt.Fprintf(w, "area %s: unread among single indexed copies=%d\n", name, unread)
		printCounts(w, "area "+name+": indexed copies per folder", folders)
	}
	return nil
}

// afterCheckpoint reports message IDs whose newest state may be missing
// from the index. The highest stamp among indexed copies is taken as the
// checkpoint. An ID is reported when its newest copy is not indexed and has
// a stamp >= the checkpoint, or when an unindexed copy has the same stamp
// as the indexed one but a different status or folder (a mark-read does not
// change the stamp, so such a change is ambiguous).
func afterCheckpoint(copies map[uint64][]c7copy, li liveIndex, area int, name string, w io.Writer) {
	var ckpt uint64
	for _, cs := range copies {
		for _, c := range cs {
			if li.indexed(c.block, area) {
				ckpt = max(ckpt, c.stamp)
			}
		}
	}
	if ckpt == 0 {
		_, _ = fmt.Fprintf(w, "area %s: no indexed message copy with a stamp, checkpoint unknown\n", name)
		return
	}
	hist := map[string]int{}
	for _, cs := range copies {
		top := cs[0]
		var live *c7copy
		for i, c := range cs {
			if c.stamp > top.stamp {
				top = c
			}
			if li.indexed(c.block, area) {
				live = &cs[i]
			}
		}
		switch {
		case live != nil && live.stamp == top.stamp:
			for _, c := range cs {
				if c.stamp == live.stamp && !li.indexed(c.block, area) && (c.status != live.status || c.folder != live.folder) {
					hist["unindexed copy with the same stamp but other status or folder (ambiguous)"]++
					break
				}
			}
		case top.stamp >= ckpt && !li.indexed(top.block, area):
			state := "no indexed copy"
			if live != nil {
				state = fmt.Sprintf("indexed copy differs=%t", live.folder != top.folder || live.status != top.status)
			}
			hist[fmt.Sprintf("newest copy not indexed, stamp >= checkpoint, %s, newest folder %x", state, top.folder)]++
		}
	}
	_, _ = fmt.Fprintf(w, "area %s: checkpoint stamp=%x\n", name, ckpt)
	printCounts(w, "area "+name+": IDs whose newest state may be missing from the index", hist)
}
