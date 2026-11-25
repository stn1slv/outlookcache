// Command hxdiscover is a development tool for reverse engineering
// HxStore.hxd. It reads a private copy of the store and prints counts,
// offsets, masked hex and redacted shapes only. It is not shipped in
// releases. Markers must start with "oc-probe" (synthetic test mail).
//
// Usage:
//
//	hxdiscover snap  -out file [-store path]
//	hxdiscover stats|objects|check|index|shifted|folderlist|folders|tree [-store path | -file copy]
//	hxdiscover find    -marker oc-probe... [-before 1024] [-after 3072] [-max 5] [-block offset]
//	hxdiscover diff    -file a -b b -marker oc-probe... [-key hexkey]
//	hxdiscover records -marker oc-probe... -probes rel:len,...
//	hxdiscover values  -values n,n [-marker oc-probe...]
//	hxdiscover ids|frames -marker oc-probe...
//	hxdiscover byid|around -value hex [-key hexkey]
//	hxdiscover obj|idslots -value hex -type hex
//	hxdiscover fieldcmp -type hex
//	hxdiscover index   [-value messageid]
//	hxdiscover refs    -values offset,offset
//	hxdiscover slots   -values folder:owner,folder:owner
//	hxdiscover framesin|refslots -block offset
//	hxdiscover entryrefs -value blockkey (0 = all blocks)
//	HXD_MATCH=text hxdiscover namefind|addrcount   (tree marks matches when HXD_MATCH is set)
//	hxdiscover sizes
//	hxdiscover items   [-value itemid] [-marker oc-probe]
//	hxdiscover bytecand -value probeid
//	hxdiscover ticks   -value probeid -ticks
//
// Real names and addresses are passed only through HXD_MATCH and are never
// printed.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"

	"github.com/stn1slv/outlookcache/internal/hxstore"
	"github.com/stn1slv/outlookcache/internal/snapshot"
)

func defaultStore() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library/Group Containers/UBF8T346G9.Office/Outlook/Outlook 15 Profiles/Main Profile/HxStore.hxd")
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	err := run(ctx, os.Args[1:], os.Stdout)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, w io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: hxdiscover <command> [flags], see the package comment")
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	store := fs.String("store", defaultStore(), "path of the live HxStore.hxd (copied before reading)")
	file := fs.String("file", "", "read this existing private copy instead of copying -store")
	out := fs.String("out", "", "snap: where to write the copy")
	marker := fs.String("marker", "", "synthetic marker text, must start with \"oc-probe\"")
	before := fs.Int("before", 1024, "find: bytes shown before each hit")
	after := fs.Int("after", 3072, "find: bytes shown after each hit")
	maxHits := fs.Int("max", 5, "find, around: maximum hits shown")
	blockOff := fs.Int64("block", -1, "find, framesin, refslots: block file offset (decimal)")
	keyHex := fs.String("key", "", "diff, around: only blocks with this key (hex)")
	showTicks := fs.Bool("ticks", false, "print exact times of recent .NET ticks (may belong to real mail)")
	other := fs.String("b", "", "diff: second private copy (the first is -file)")
	typeHex := fs.String("type", "", "obj, idslots, fieldcmp: object type (hex)")
	valueHex := fs.String("value", "", "byid, around, obj, idslots, index, entryrefs: u64 ID or key (hex)")
	valueList := fs.String("values", "", "values: u32 list; refs: offsets; slots: folder:owner pairs")
	probeSpec := fs.String("probes", "", "records: rel:len,rel:len byte ranges relative to the marker")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *before < 0 || *after < 0 || *maxHits < 0 {
		return errors.New("-before, -after and -max must not be negative")
	}
	now := time.Now()
	probeRedactor := redactor{marker: markerPrefix, now: now, showTicks: *showTicks}

	if args[0] == "snap" {
		if *out == "" {
			return errors.New("snap needs -out")
		}
		return snap(ctx, *store, *out, w)
	}

	// Validate everything before the store is copied.
	var cmd func(data []byte) error
	needMarker := func(optional bool) (string, error) {
		if optional && *marker == "" {
			return "", nil
		}
		return checkMarker(*marker)
	}
	parseHex := func(s string, bits int) (uint64, error) {
		v, err := strconv.ParseUint(s, 16, bits)
		if err != nil {
			return 0, fmt.Errorf("bad hex value %q: %w", s, err)
		}
		return v, nil
	}
	var key keyFilter
	if *keyHex != "" {
		v, err := parseHex(*keyHex, 64)
		if err != nil {
			return err
		}
		key = keyFilter{set: true, key: v}
	}
	needBlock := func() error {
		if *blockOff < 0 {
			return errors.New(args[0] + " needs -block with a block file offset")
		}
		return nil
	}
	switch args[0] {
	case "diff":
		m, err := needMarker(false)
		if err != nil {
			return err
		}
		if *other == "" || *file == "" {
			return errors.New("diff needs -file and -b")
		}
		cmd = func(data []byte) error {
			b, err := os.ReadFile(*other) //nolint:gosec // dev tool: path from the developer's own flag
			if err != nil {
				return err
			}
			return diffSnapshots(data, b, redactor{marker: m, now: now, showTicks: *showTicks}, key, w)
		}
	case "values":
		m, err := needMarker(true)
		if err != nil {
			return err
		}
		cmd = func(data []byte) error { return values(data, m, *valueList, w) }
	case "ids", "frames", "records", "find":
		m, err := needMarker(false)
		if err != nil {
			return err
		}
		switch args[0] {
		case "ids":
			cmd = func(data []byte) error { return ids(data, m, w) }
		case "frames":
			cmd = func(data []byte) error { return frames(data, m, w) }
		case "records":
			probes, err := parseProbes(*probeSpec)
			if err != nil {
				return err
			}
			cmd = func(data []byte) error { return records(data, m, probes, w) }
		default:
			r := redactor{marker: m, now: now, showTicks: *showTicks}
			cmd = func(data []byte) error { return find(data, r, *before, *after, *maxHits, *blockOff, w) }
		}
	case "folders":
		cmd = func(data []byte) error { return folderObjects(data, probeRedactor, w) }
	case "folderlist":
		cmd = func(data []byte) error { return folderList(data, probeRedactor, w) }
	case "obj":
		v, err := parseHex(*valueHex, 64)
		if err != nil {
			return err
		}
		t, err := parseHex(*typeHex, 16)
		if err != nil {
			return err
		}
		cmd = func(data []byte) error { return dumpObject(data, probeRedactor, v, uint16(t), w) }
	case "fieldcmp":
		t, err := parseHex(*typeHex, 16)
		if err != nil {
			return err
		}
		cmd = func(data []byte) error { return fieldCompare(data, uint16(t), w) }
	case "byid", "around":
		v, err := parseHex(*valueHex, 64)
		if err != nil {
			return err
		}
		if args[0] == "byid" {
			cmd = func(data []byte) error { return framesByID(data, v, w) }
		} else {
			cmd = func(data []byte) error {
				return around(data, probeRedactor, v, key, *before, *after, *maxHits, w)
			}
		}
	case "shifted":
		cmd = func(data []byte) error { return shifted(data, w) }
	case "refs":
		offs, err := parseOffsets(*valueList)
		if err != nil {
			return err
		}
		cmd = func(data []byte) error { return blockRefs(data, offs, w) }
	case "index":
		var show uint64
		if *valueHex != "" {
			v, err := parseHex(*valueHex, 64)
			if err != nil {
				return err
			}
			show = v
		}
		cmd = func(data []byte) error { return indexCheck(data, show, w) }
	case "slots":
		pairs, err := parsePairs(*valueList)
		if err != nil {
			return err
		}
		cmd = func(data []byte) error { return slots(data, pairs, w) }
	case "idslots":
		v, err := parseHex(*valueHex, 64)
		if err != nil {
			return err
		}
		t, err := parseHex(*typeHex, 16)
		if err != nil {
			return err
		}
		cmd = func(data []byte) error { return idSlots(data, v, uint16(t), w) }
	case "framesin", "refslots":
		if err := needBlock(); err != nil {
			return err
		}
		if args[0] == "framesin" {
			cmd = func(data []byte) error { return framesIn(data, *blockOff, w) }
		} else {
			cmd = func(data []byte) error { return refSlots(data, *blockOff, w) }
		}
	case "entryrefs":
		v, err := parseHex(*valueHex, 64)
		if err != nil {
			return err
		}
		cmd = func(data []byte) error { return entryRefs(data, v, w) }
	case "namefind", "addrcount":
		text, err := matchText()
		if err != nil {
			return err
		}
		if args[0] == "namefind" {
			cmd = func(data []byte) error { return nameFind(data, text, w) }
		} else {
			cmd = func(data []byte) error { return addrCount(data, text, w) }
		}
	case "tree":
		match := os.Getenv(matchEnv)
		cmd = func(data []byte) error { return folderTree(data, probeRedactor, match, w) }
	case "sizes":
		cmd = func(data []byte) error { return typeSizes(data, []uint16{0xcc, 0xcd, 0xce, 0x154, 0x168, 0x2f6}, w) }
	case "bytecand":
		v, err := parseHex(*valueHex, 64)
		if err != nil {
			return err
		}
		cmd = func(data []byte) error { return byteCandidates(data, v, bytecandLimit, w) }
	case "ticks":
		v, err := parseHex(*valueHex, 64)
		if err != nil {
			return err
		}
		cmd = func(data []byte) error { return probeTicks(data, v, *showTicks, now, w) }
	case "items":
		var show uint64
		if *valueHex != "" {
			v, err := parseHex(*valueHex, 64)
			if err != nil {
				return err
			}
			show = v
		}
		m, err := needMarker(true)
		if err != nil {
			return err
		}
		cmd = func(data []byte) error { return itemCheck(data, show, m, w) }
	case "fields":
		cmd = func(data []byte) error { return fieldCheck(data, now, w) }
	case "lens":
		cmd = func(data []byte) error { return stringLengthFields(data, w) }
	case "probelens":
		v, err := parseHex(*valueHex, 64)
		if err != nil {
			return err
		}
		cmd = func(data []byte) error { return probeLengths(data, v, w) }
	case "lentable":
		cmd = func(data []byte) error { return lengthTableCheck(data, w) }
	case "subjgap":
		cmd = func(data []byte) error { return subjectGap(data, w) }
	case "text":
		cmd = func(data []byte) error { return textCheck(data, w) }
	case "recips":
		cmd = func(data []byte) error { return recipientCheck(data, w) }
	case "attach":
		cmd = func(data []byte) error { return attachmentCheck(data, w) }
	case "check":
		cmd = func(data []byte) error { return check(data, w) }
	case "objects":
		cmd = func(data []byte) error { return objects(data, w) }
	case "stats":
		cmd = func(data []byte) error { return stats(data, w) }
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}

	data, err := load(ctx, *store, *file)
	if err != nil {
		return err
	}
	return cmd(data)
}

// load returns the bytes of a private copy of the store. The temp copy is
// removed as soon as it is in memory, so an interrupted scan leaves nothing.
func load(ctx context.Context, store, file string) ([]byte, error) {
	if file != "" {
		return os.ReadFile(file) //nolint:gosec // dev tool: paths come from the developer's own flags
	}
	s, err := snapshot.New()
	if err != nil {
		return nil, err
	}
	defer func() { _ = s.Close() }()
	p, err := s.Copy(ctx, store)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(p) //nolint:gosec // dev tool: paths come from the developer's own flags
}

// snap writes a private copy of the store to out. It refuses an existing
// target and any target inside the Office group container, so a flag
// mistake can never replace Outlook's files.
func snap(ctx context.Context, store, out string, w io.Writer) error {
	abs, err := filepath.Abs(out)
	if err != nil {
		return err
	}
	if strings.Contains(abs, "/Group Containers/UBF8T346G9.Office/") {
		return fmt.Errorf("refusing to write inside the Office group container: %s", abs)
	}
	s, err := snapshot.New()
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()
	start := time.Now()
	p, err := s.Copy(ctx, store)
	if err != nil {
		return err
	}
	if err := copyNew(p, abs); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "copied in %s to %s\n", time.Since(start).Round(time.Millisecond), abs)
	return nil
}

// copyNew copies src to a new file dst (0600); it fails if dst exists.
func copyNew(src, dst string) error {
	in, err := os.Open(src) //nolint:gosec // dev tool: our own snapshot copy
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // dev tool: path from the developer's own flag
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst) //nolint:gosec // dev tool: path from the developer's own flag
		return err
	}
	return out.Close()
}

var itemClassU16 = regexp.MustCompile(`I\x00P\x00[MF]\x00\.\x00(?:[A-Za-z0-9._-]\x00){1,60}`)

func stats(data []byte, w io.Writer) error {
	h, err := hxstore.ParseHeader(data)
	if err != nil {
		return err
	}
	start := time.Now()
	classes := map[string]int{}
	st, damage, err := hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		for _, m := range itemClassU16.FindAll(b.Payload, -1) {
			c := decodeU16(m)
			if !safeItemClass(c) {
				c = "<other class, redacted>"
			}
			classes[c]++
		}
		return nil
	})
	if err != nil {
		return err
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	_, _ = fmt.Fprintf(w, "file_size=%d version=%q supported=%t page_size=%d\n", len(data), h.Version, h.Supported(), h.PageSize)
	_, _ = fmt.Fprintf(w, "candidates=%d valid=%d by_type=%v inflated_mb=%.1f scan=%s heap_mb=%d\n",
		st.Candidates, st.Valid, st.ByType, float64(st.Inflated)/1e6, time.Since(start).Round(time.Millisecond), ms.HeapAlloc>>20)
	reasons := map[string]int{}
	for _, d := range damage {
		reasons[d.Reason]++
	}
	_, _ = fmt.Fprintf(w, "damage=%d reasons=%v\n", len(damage), reasons)
	printCounts(w, "item classes (UTF-16LE in payloads)", classes)
	return nil
}

func printCounts(w io.Writer, title string, m map[string]int) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return m[keys[i]] > m[keys[j]] })
	_, _ = fmt.Fprintln(w, title+":")
	for _, k := range keys {
		_, _ = fmt.Fprintf(w, "  %7d  %s\n", m[k], k)
	}
}

func decodeU16(b []byte) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	return string(utf16.Decode(u))
}

func encodeU16(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		b[2*i] = byte(c)
		b[2*i+1] = byte(c >> 8)
	}
	return b
}

// find prints a redacted, tokenized window around each payload position
// where the marker occurs (UTF-16LE or UTF-8, ASCII case-insensitive).
func find(data []byte, r redactor, before, after, maxHits int, blockOff int64, w io.Writer) error {
	needles := [][]byte{encodeU16(r.marker), []byte(r.marker)}
	shown, total := 0, 0
	_, _, err := hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		if blockOff >= 0 && b.Offset != blockOff {
			return nil
		}
		low := asciiLower(b.Payload)
		for ni, nd := range needles {
			for off := 0; ; {
				k := bytes.Index(low[off:], nd)
				if k < 0 {
					break
				}
				hit := off + k
				off = hit + len(nd)
				total++
				if shown >= maxHits {
					continue
				}
				shown++
				enc := [...]string{tokU16, tokU8}[ni]
				_, _ = fmt.Fprintf(w, "\n=== hit %d: block@%d type=%d payload_len=%d marker@%d (%s)\n", shown, b.Offset, b.Type, len(b.Payload), hit, enc)
				lo, hi := max(0, hit-before), min(len(b.Payload), hit+after)
				dump(w, r, b.Payload[lo:hi], lo, hit)
			}
		}
		return nil
	})
	_, _ = fmt.Fprintf(w, "\nmarker hits: %d (shown %d)\n", total, shown)
	return err
}

func dump(w io.Writer, r redactor, span []byte, base, hit int) {
	for _, t := range tokenize(span) {
		off := base + t.off
		rel := off - hit
		switch t.kind {
		case tokU16, tokU8:
			_, _ = fmt.Fprintf(w, "%+6d %s %s\n", rel, t.kind, r.show(t.text))
		default:
			dumpBinary(w, r, t.raw, rel)
		}
	}
}

// dumpBinary prints a binary gap as hex, 32 bytes per line, with every span
// that decodes as text masked.
func dumpBinary(w io.Writer, r redactor, raw []byte, rel int) {
	mask := textMask(raw)
	for i := 0; i < len(raw); i += 32 {
		end := min(len(raw), i+32)
		var notes []string
		for j := i; j < end; j++ {
			if n, ok := r.tickNote(raw[j:]); ok {
				notes = append(notes, fmt.Sprintf("+%d:%s", j-i, n))
			}
		}
		line := fmt.Sprintf("%+6d bin %s", rel+i, renderHex(raw[i:end], mask[i:end]))
		if len(notes) > 0 {
			line += "  " + strings.Join(notes, " ")
		}
		_, _ = fmt.Fprintln(w, line)
	}
}

// asciiLower lowers ASCII letters only, so offsets stay equal to the input.
func asciiLower(b []byte) []byte {
	out := make([]byte, len(b))
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return out
}
