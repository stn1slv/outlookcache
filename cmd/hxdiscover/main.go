// Command hxdiscover is a development tool for reverse engineering
// HxStore.hxd. It reads a private copy of the store and prints counts,
// offsets and redacted shapes only. It is not shipped in releases.
//
// Usage:
//
//	hxdiscover stats [-store path]
//	hxdiscover snap  -out file [-store path]
//	hxdiscover find  -marker oc-probe [-store path | -file copy] [-before 1024] [-after 3072] [-max 5]
package main

import (
	"bytes"
	"context"
	"encoding/hex"
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdout)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, w io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: hxdiscover stats|snap|find [flags]")
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	store := fs.String("store", defaultStore(), "path of the live HxStore.hxd (copied before reading)")
	file := fs.String("file", "", "read this existing private copy instead of copying -store")
	out := fs.String("out", "", "snap: where to write the copy")
	marker := fs.String("marker", "", "find: synthetic marker text")
	before := fs.Int("before", 1024, "find: bytes shown before each hit")
	after := fs.Int("after", 3072, "find: bytes shown after each hit")
	maxHits := fs.Int("max", 5, "find: maximum hits shown")
	blockOff := fs.Int64("block", -1, "find: only this block offset; diff: only this block key")
	other := fs.String("b", "", "diff: second private copy (the first is -file)")
	valueList := fs.String("values", "", "values: comma-separated u32 values")
	probeSpec := fs.String("probes", "", "records: rel:len,rel:len byte ranges relative to the marker")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	if args[0] == "snap" {
		if *out == "" {
			return errors.New("snap needs -out")
		}
		return snap(ctx, *store, *out, w)
	}

	data, cleanup, err := load(ctx, *store, *file)
	if err != nil {
		return err
	}
	defer cleanup()

	switch args[0] {
	case "diff":
		if len(*marker) < 6 || *other == "" || *file == "" {
			return errors.New("diff needs -file, -b and -marker")
		}
		b, err := os.ReadFile(*other) //nolint:gosec // dev tool: path from the developer's own flag
		if err != nil {
			return err
		}
		return diffSnapshots(data, b, redactor{marker: strings.ToLower(*marker), now: time.Now()}, *blockOff, w)
	case "values":
		return values(data, strings.ToLower(*marker), *valueList, w)
	case "check":
		return check(data, w)
	case "objects":
		return objects(data, w)
	case "records":
		probes, err := parseProbes(*probeSpec)
		if err != nil {
			return err
		}
		return records(data, *marker, probes, w)
	case "stats":
		return stats(data, w)
	case "find":
		if len(*marker) < 6 {
			return errors.New("find needs -marker with at least 6 characters")
		}
		r := redactor{marker: strings.ToLower(*marker), now: time.Now()}
		return find(data, r, *before, *after, *maxHits, *blockOff, w)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// load returns the bytes of a private copy of the store.
func load(ctx context.Context, store, file string) ([]byte, func(), error) {
	if file != "" {
		data, err := os.ReadFile(file) //nolint:gosec // dev tool: paths come from the developer's own flags
		return data, func() {}, err
	}
	s, err := snapshot.New()
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = s.Close() }
	p, err := s.Copy(ctx, store)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	data, err := os.ReadFile(p) //nolint:gosec // dev tool: paths come from the developer's own flags
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return data, cleanup, nil
}

func snap(ctx context.Context, store, out string, w io.Writer) error {
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
	if err := os.Rename(p, out); err != nil { //nolint:gosec // dev tool: paths come from the developer's own flags
		return err
	}
	_, _ = fmt.Fprintf(w, "copied in %s to %s\n", time.Since(start).Round(time.Millisecond), out)
	return nil
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
// where the marker occurs (UTF-16LE or UTF-8, case as given).
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

func dumpBinary(w io.Writer, r redactor, raw []byte, rel int) {
	for i := 0; i < len(raw); i += 32 {
		chunk := raw[i:min(len(raw), i+32)]
		var notes []string
		for j := range chunk {
			if n, ok := r.tickNote(raw[i+j:]); ok {
				notes = append(notes, fmt.Sprintf("+%d:%s", j, n))
			}
		}
		line := fmt.Sprintf("%+6d bin %s", rel+i, hex.EncodeToString(chunk))
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
