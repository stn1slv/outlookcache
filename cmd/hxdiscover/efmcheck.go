package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	bodypkg "github.com/stn1slv/outlookcache/internal/body"
	"github.com/stn1slv/outlookcache/internal/hxstore"
	"github.com/stn1slv/outlookcache/internal/store"
)

// efmPaths returns the UTF-16 paths in p that contain /EFMData/, in order.
func efmPaths(p []byte) []string {
	nd := encodeU16("/EFMData/")
	var out []string
	for off := 0; ; {
		k := bytes.Index(p[off:], nd)
		if k < 0 {
			return out
		}
		at := off + k
		off = at + len(nd)
		s := at
		for s-2 >= 0 && binary.LittleEndian.Uint16(p[s-2:]) >= 0x20 && at-s < 2048 {
			s -= 2
		}
		e := at
		for e+2 <= len(p) && (p[e] != 0 || p[e+1] != 0) {
			e += 2
		}
		out = append(out, decodeU16(p[s:e]))
	}
}

// efmCheck reads, read-only (body.ReadEFM), the EFMData files named in the
// block of each message whose head flag is 0x82 and prints counts only:
// whether the file can be read under the account's Files/S0 folder, whether the
// content is a full HTML document, and how its size compares with the
// head size field.
func efmCheck(data []byte, profile string, w io.Writer) error {
	s, err := store.Load(data, "HxStore.hxd")
	if err != nil {
		return err
	}
	ix, err := hxstore.ReadIndex(data)
	if err != nil {
		return err
	}
	dirOf := map[string]string{}
	for _, a := range s.Accounts {
		dirOf[a.ID] = a.Dir
	}
	acc := map[uint64]string{}
	for _, m := range s.Messages {
		var id uint64
		_, _ = fmt.Sscanf(m.ID, "%x", &id)
		acc[id] = m.AccountID
	}
	type pick struct {
		payload []byte
		direct  bool
	}
	blocks := map[uint64]pick{}
	if _, _, err := hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		if _, ok := acc[b.Key]; !ok {
			return nil
		}
		d := ix.Direct(b.Offset)
		if old, ok := blocks[b.Key]; !ok || d || !old.direct {
			blocks[b.Key] = pick{b.Payload, d}
		}
		return nil
	}); err != nil {
		return err
	}
	c := map[string]int{}
	for id, p := range blocks {
		pl := p.payload
		head := len(pl)
		hxstore.ForEachFraming(pl, func(f hxstore.Framing) { head = min(head, f.Offset) })
		hd := pl[:head]
		cls := -1
		for _, nd := range [][]byte{encodeU16("IPM."), encodeU16("REPORT.")} {
			if k := bytes.Index(hd, nd); k >= 0 && (cls < 0 || k < cls) {
				cls = k
			}
		}
		if cls < 214 {
			continue
		}
		v1 := binary.LittleEndian.Uint32(hd[cls-210:])
		if v1>>24 != 0x82 {
			continue
		}
		paths := efmPaths(pl)
		c[fmt.Sprintf("0x82 messages: paths in block %d, in head %d", min(len(paths), 4), min(len(efmPaths(hd)), 4))]++
		dir := dirOf[acc[id]]
		for i, path := range paths {
			if i > 1 {
				break
			}
			sameDir := strings.Contains(path, "/Files/S0/"+dir+"/EFMData/")
			body, _, err := bodypkg.ReadEFM(profile, dir, []string{filepath.Base(path)}, 0)
			if err != nil {
				c[fmt.Sprintf("path %d: same account dir=%t, file missing or unreadable", i, sameDir)]++
				continue
			}
			doc := isHTMLDoc(body)
			c[fmt.Sprintf("path %d: same account dir=%t, html doc=%t, size %s, head size == len: %t", i, sameDir, doc,
				sizeBucket(len(body)), int(v1&0xffffff) == len(body))]++
		}
		if len(paths) >= 2 {
			c[fmt.Sprintf("first two paths name the same file: %t", filepath.Base(paths[0]) == filepath.Base(paths[1]))]++
		}
		v2 := binary.LittleEndian.Uint32(hd[cls-202:])
		hp := efmPaths(hd)
		if len(hp) > 0 {
			c[fmt.Sprintf("size1 low=%d equals path0 utf16 len: %t, utf16 len+? diff=%d; v2 flags %#x v2 low == path1 len: %t", 0, int(v1&0xffffff) == len([]rune(hp[0])),
				int(v1&0xffffff)-len([]rune(hp[0])), v2>>24, len(hp) > 1 && int(v2&0xffffff) == len([]rune(hp[1])))]++
		}
		if len(paths) >= 2 && filepath.Base(paths[0]) != filepath.Base(paths[1]) {
			read := func(path string) []byte {
				b, _, _ := bodypkg.ReadEFM(profile, dir, []string{filepath.Base(path)}, 0)
				return b
			}
			a, b := read(paths[0]), read(paths[1])
			la, lb := asciiLower(a), asciiLower(b)
			c[fmt.Sprintf("two files: equal=%t first longer=%t first starts %q second starts %q", bytes.Equal(a, b), len(a) > len(b),
				string(la[:min(5, len(la))]), string(lb[:min(5, len(lb))]))]++
		}
	}
	printCounts(w, "EFMData bodies (counts only)", c)
	return nil
}
