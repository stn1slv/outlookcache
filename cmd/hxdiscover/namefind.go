package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/stn1slv/outlookcache/internal/hxstore"
)

// matchEnv names the environment variable that carries a real name or
// address for namefind, tree and addrcount. An environment variable keeps
// the value out of shell history, the process list and the command line
// that a transcript shows.
const matchEnv = "HXD_MATCH"

func matchText() (string, error) {
	v := os.Getenv(matchEnv)
	if strings.TrimSpace(v) == "" {
		return "", fmt.Errorf("set %s to the text to match; it is never printed", matchEnv)
	}
	return v, nil
}

type needle struct {
	enc  string
	low  []byte // lower-case ASCII form, searched in a lower-cased copy
	text string
}

func needles(text string) []needle {
	low := string(asciiLower([]byte(text)))
	return []needle{{encUTF16, encodeU16(low), text}, {encUTF8, []byte(low), text}}
}

// nameFind counts where a known text occurs (UTF-16LE or UTF-8, ASCII case
// ignored). It prints only the containing framing (ID, type, offset), the
// kind of prefix in front of the text (a validated length prefix or the end
// of a sort key) and the object IDs in the 48 bytes before it, never the
// text or raw bytes next to it.
func nameFind(data []byte, text string, w io.Writer) error {
	ns := needles(text)
	counts := map[string]int{}
	_, _, err := hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		low := asciiLower(b.Payload) // same length and offsets as the payload
		for _, n := range ns {
			findName(b.Payload, low, b.Key, n, counts)
		}
		return nil
	})
	if err != nil {
		return err
	}
	printCounts(w, "occurrences (text not printed)", counts)
	return nil
}

// findName records each match of n. Matching runs on low; every value is
// read from the original payload p, and each candidate is confirmed there
// with a case-insensitive comparison of the decoded text.
func findName(p, low []byte, key uint64, n needle, counts map[string]int) {
	step := 1
	if n.enc == encUTF16 {
		step = 2
	}
	for off := 0; off < len(low); {
		k := bytes.Index(low[off:], n.low)
		if k < 0 {
			return
		}
		at := off + k
		off = at + 1
		end := at + len(n.low)
		got := string(p[at:end])
		if n.enc == encUTF16 {
			got = decodeU16(p[at:end])
		}
		if !strings.EqualFold(got, n.text) {
			continue
		}
		frame := "unframed"
		if f := bytes.LastIndex(p[:at], objMagic); f >= 0 && f+18 <= len(p) {
			if size := int(binary.LittleEndian.Uint32(p[f+14:])); size >= 18 && end-f <= size {
				frame = fmt.Sprintf("id=%x type=0x%04x +%d", binary.LittleEndian.Uint64(p[f+4:]), binary.LittleEndian.Uint16(p[f+12:]), at-f)
			}
		}
		prefix := "-"
		switch {
		case at >= 6 && n.enc == encUTF16 && binary.LittleEndian.Uint32(p[at-6:]) == 0x80000000|uint32(len(n.low)+2):
			prefix = "length-prefixed"
		case at >= 2 && p[at-2] == 0x01 && p[at-1] == 0x00:
			prefix = "after-sort-key"
		}
		var refs []string
		for i := max(0, at-48); i+8 <= at; i++ {
			if v := binary.LittleEndian.Uint64(p[i:]); isID(v) {
				refs = append(refs, fmt.Sprintf("-%d:%x", at-i, v))
				i += 7
			}
		}
		counts[fmt.Sprintf("%s blockkey=%x frame[%s] prefix=%s refs=%v", n.enc, key, frame, prefix, refs)]++
		off = at + step
	}
}

// addrCount counts the current message copies per folder (live-block rule,
// see liveIndex.current) and how many of them contain the given text in
// UTF-16LE (ASCII case ignored), to test where an account's own address is
// stored. Only counts are printed.
func addrCount(data []byte, text string, w io.Writer) error {
	if text == "" {
		return errors.New("addrcount needs a non-empty text")
	}
	li, err := readLiveIndex(data)
	if err != nil {
		return err
	}
	n := needles(text)[0]
	copies := map[uint64][]c7copy{}
	if _, err := scanMessages(data, func(c c7copy, folderOK bool) {
		if folderOK {
			copies[c.id] = append(copies[c.id], c)
		}
	}); err != nil {
		return err
	}
	counts := map[string]int{}
	for _, cs := range copies {
		c, _, ambiguous := li.current(cs)
		counts[fmt.Sprintf("folder %x total", c.folder)]++
		if ambiguous {
			counts[fmt.Sprintf("folder %x ambiguous current copy", c.folder)]++
		}
		hit := map[string]int{}
		findName(c.obj, asciiLower(c.obj), 0, n, hit)
		if len(hit) > 0 {
			counts[fmt.Sprintf("folder %x contains text", c.folder)]++
		}
	}
	printCounts(w, "messages per folder (text not printed)", counts)
	return nil
}

// Encodings searched by namefind.
const (
	encUTF16 = "utf16"
	encUTF8  = "utf8"
)
