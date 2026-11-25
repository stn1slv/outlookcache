// Package body reads and converts message bodies. It reads EFMData files
// read-only and only inside the profile's Files/S0 folder.
package body

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"unicode/utf8"
)

var (
	// ErrBadReference reports an EFMData folder or file name with an
	// unexpected shape.
	ErrBadReference = errors.New("invalid EFMData reference")
	// ErrNoFile reports that none of the named EFMData files can be read.
	ErrNoFile = errors.New("no readable EFMData file")
	// ErrTooLarge reports a body over the size limit.
	ErrTooLarge = errors.New("body over the size limit")
)

// MaxBytes is the default limit for one body, after decompression.
const MaxBytes = 32 << 20

// maxLimit caps any limit, so that limit+1 cannot overflow.
const maxLimit = 1 << 40

var (
	dirRe  = regexp.MustCompile(`^[0-9]{1,9}$`)
	nameRe = regexp.MustCompile(`^[0-9]{1,12}\.dat$`)
	gzipID = []byte{0x1f, 0x8b}
)

// ReadEFM reads the first readable file of names from
// <profile>/Files/S0/<dir>/EFMData and returns its content, gunzipped when
// it is gzip, with the name of the file used. A file must hold UTF-8 text
// that starts with a tag; otherwise the next name is tried. Files are
// opened read-only through an os.Root, so a link cannot lead outside
// Files/S0, and only regular files are opened. limit caps the size after
// decompression (0 or less means MaxBytes).
func ReadEFM(profile, dir string, names []string, limit int64) ([]byte, string, error) {
	if limit <= 0 {
		limit = MaxBytes
	}
	limit = min(limit, maxLimit)
	if !dirRe.MatchString(dir) {
		return nil, "", fmt.Errorf("%w: folder %q", ErrBadReference, dir)
	}
	if len(names) == 0 {
		return nil, "", fmt.Errorf("%w: no file names", ErrBadReference)
	}
	for _, name := range names {
		if !nameRe.MatchString(name) {
			return nil, "", fmt.Errorf("%w: file %q", ErrBadReference, name)
		}
	}
	root, err := os.OpenRoot(filepath.Join(profile, "Files", "S0"))
	if err != nil {
		return nil, "", fmt.Errorf("%w: %w", ErrNoFile, err)
	}
	defer func() { _ = root.Close() }()
	var errs []error
	for _, name := range names {
		b, err := readOne(root, path.Join(dir, "EFMData", name), limit)
		if err == nil {
			return b, name, nil
		}
		if errors.Is(err, ErrTooLarge) {
			return nil, name, err
		}
		errs = append(errs, err)
	}
	return nil, "", fmt.Errorf("%w: %w", ErrNoFile, errors.Join(errs...))
}

// errNotHTML reports file content that is not UTF-8 text starting with a
// tag.
var errNotHTML = errors.New("content is not HTML text")

func readOne(root *os.Root, name string, limit int64) ([]byte, error) {
	// Lstat first: opening a FIFO or a device could block or have side
	// effects, and a link is not followed.
	st, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: %w", name, fs.ErrInvalid)
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	br := bufio.NewReader(f)
	var src io.Reader = br
	if head, _ := br.Peek(len(gzipID)); bytes.Equal(head, gzipID) {
		zr, err := gzip.NewReader(br)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		defer func() { _ = zr.Close() }()
		src = zr
	}
	out, err := readLimited(src, limit)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if t := bytes.TrimLeft(out, " \t\r\n\ufeff"); len(t) == 0 || t[0] != '<' || !utf8.Valid(out) {
		return nil, fmt.Errorf("%s: %w", name, errNotHTML)
	}
	return out, nil
}

// readLimited reads all of r, or fails with ErrTooLarge after limit bytes.
func readLimited(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, ErrTooLarge
	}
	return b, nil
}
