package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/stn1slv/outlookcache/internal/hxstore"
)

type folderRec struct {
	id, parent, owner uint64
	name              string
	block             int64
}

var (
	recHead = []byte{0xe3, 0x01, 0, 0, 0, 0, 0, 0, 0, 0}
	recTail = []byte{0x05, 0x00, 0xd8, 0x04}
	nineZ   = make([]byte, 9)
)

// parseFolderRecords finds child folder records (form 1, UNVERIFIED layout
// from probes) in one payload. A record is: `e3 01` + 8 zero bytes, parent
// ID (u64), a Windows sort key that ends with one 0x00 byte, the name as
// UTF-16LE ending with 0x0000, then the tail `01`, 4 bytes, `05 00 d8 04`,
// 4 bytes, `00 00 4d`, 9 zero bytes, folder ID, u32 2, owner (account
// root). The parser anchors on the tail. The sort key can contain zero
// bytes, so the name start is found by walking back from the terminator
// while the UTF-16 unit is >= 0x20; the unit before the name (last sort-key
// byte and 0x00) is below 0x20 in every record seen. A sort key that ended
// with a byte >= 0x20 would add one character to the name (UNVERIFIED
// limit). The parent is the ID after the nearest `e3 01` head before it.
func parseFolderRecords(p []byte, block int64) []folderRec {
	var out []folderRec
	for off := 0; ; {
		k := bytes.Index(p[off:], recTail)
		if k < 0 {
			return out
		}
		t := off + k - 5 // start of the tail
		off += k + 1
		if t < 4 || t+45 > len(p) || p[t] != 0x01 || p[t+13] != 0 || p[t+14] != 0 || p[t+15] != 0x4d ||
			!bytes.Equal(p[t+16:t+25], nineZ) || p[t-1] != 0 || p[t-2] != 0 || binary.LittleEndian.Uint32(p[t+33:]) != 2 {
			continue
		}
		id := binary.LittleEndian.Uint64(p[t+25:])
		if !isID(id) {
			continue
		}
		e := t - 2 // name terminator
		n := e     // name start: walk back while the UTF-16 unit is printable
		for n-2 >= 0 && binary.LittleEndian.Uint16(p[n-2:]) >= 0x20 && e-n < 512 {
			n -= 2
		}
		if n == e || n < 2 || p[n-1] != 0 || p[n-2] == 0 {
			continue // the sort key must end with a non-zero byte and 0x00
		}
		lo := max(0, n-1100)
		h := bytes.LastIndex(p[lo:n], recHead)
		if h < 0 {
			continue
		}
		s := lo + h + len(recHead)
		if s+8 > n || !isID(binary.LittleEndian.Uint64(p[s:])) {
			continue
		}
		out = append(out, folderRec{
			id: id, parent: binary.LittleEndian.Uint64(p[s:]), owner: binary.LittleEndian.Uint64(p[t+37:]),
			name: decodeU16(p[n:e]), block: block,
		})
	}
}

// folderRecords collects every version of every form 1 record and picks
// one per folder: a version from a block in the live-block index (area A)
// wins over others. conflicts counts folders whose indexed versions
// disagree in name or parent.
func folderRecords(data []byte) (recs map[uint64]folderRec, conflicts int, err error) {
	li, err := readLiveIndex(data)
	if err != nil {
		return nil, 0, err
	}
	all := map[uint64][]folderRec{}
	if _, _, err := hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		for _, r := range parseFolderRecords(b.Payload, b.Offset) {
			all[r.id] = append(all[r.id], r)
		}
		return nil
	}); err != nil {
		return nil, 0, err
	}
	recs = map[uint64]folderRec{}
	for id, vs := range all {
		var live []folderRec
		for _, v := range vs {
			if li.indexed(v.block, 0) {
				live = append(live, v)
			}
		}
		pick := vs[len(vs)-1]
		if len(live) > 0 {
			pick = live[0]
			for _, v := range live[1:] {
				if v.name != pick.name || v.parent != pick.parent {
					conflicts++
					break
				}
			}
		}
		recs[id] = pick
	}
	return recs, conflicts, nil
}

// folderTree prints the folder records as a tree. A name is printed only
// when the redactor would print it in full (a known schema word or a whole
// synthetic probe name); otherwise only its shape. With match set, rows
// whose name equals it (case ignored) are marked; the text is not printed.
func folderTree(data []byte, r redactor, match string, w io.Writer) error {
	recs, conflicts, err := folderRecords(data)
	if err != nil {
		return err
	}
	children := map[uint64][]uint64{}
	for id, f := range recs {
		children[f.parent] = append(children[f.parent], id)
	}
	var roots []uint64
	for p := range children {
		if _, ok := recs[p]; !ok {
			roots = append(roots, p)
		}
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i] < roots[j] })
	shown := map[uint64]bool{}
	var walk func(id uint64, depth int)
	walk = func(id uint64, depth int) {
		kids := children[id]
		sort.Slice(kids, func(i, j int) bool { return kids[i] < kids[j] })
		for _, k := range kids {
			if shown[k] {
				continue // a cycle
			}
			shown[k] = true
			f := recs[k]
			_, _ = fmt.Fprintf(w, "%*s%x owner=%x name=%s%s\n", 2*depth, "", k, f.owner, treeName(r, f.name), matchMark(f.name, match))
			walk(k, depth+1)
		}
	}
	for _, root := range roots {
		_, _ = fmt.Fprintf(w, "parent %x (no record)\n", root)
		walk(root, 1)
	}
	_, _ = fmt.Fprintf(w, "records=%d shown=%d not shown (parent cycle)=%d folders with conflicting indexed versions=%d\n",
		len(recs), len(shown), len(recs)-len(shown), conflicts)
	return nil
}

// probeFolderName is the strict form of a synthetic test folder name.
var probeFolderName = regexp.MustCompile(`^oc-probe-[a-z0-9-]{1,40}$`)

// probeText returns a subject quoted when it is a strict probe text
// (oc-probe- followed by lower-case words), else only its shape.
func probeText(s string) string {
	if probeFolderName.MatchString(strings.ReplaceAll(strings.ToLower(s), " ", "-")) {
		return fmt.Sprintf("%q", s)
	}
	return shape(s)
}

// treeName prints a folder name only when it is a strict probe name or a
// schema word that the redactor prints unchanged; otherwise its shape.
func treeName(r redactor, name string) string {
	if probeFolderName.MatchString(name) {
		return strconv.Quote(name)
	}
	if s := r.show(name); s == strconv.Quote(name) && !strings.Contains(strings.ToLower(name), markerPrefix) {
		return s
	}
	return shape(name)
}

func matchMark(name, match string) string {
	if match != "" && strings.EqualFold(name, match) {
		return " <== equals " + matchEnv
	}
	return ""
}
