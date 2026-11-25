// Package body reads and converts message bodies. It reads EFMData files
// read-only and only inside the profile's Files/S0 folder.
package body

import (
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

var (
	dirRe  = regexp.MustCompile(`^[0-9]{1,9}$`)
	nameRe = regexp.MustCompile(`^[0-9]{1,12}\.dat$`)
	gzipID = []byte{0x1f, 0x8b}
)

// ReadEFM reads the first readable file of names from
// <profile>/Files/S0/<dir>/EFMData and returns its content, gunzipped when
// it is gzip, with the name of the file used. Files are opened read-only
// through an os.Root, so a link cannot lead outside Files/S0. limit caps
// the size after decompression (0 means MaxBytes).
func ReadEFM(profile, dir string, names []string, limit int64) ([]byte, string, error) {
	if limit <= 0 {
		limit = MaxBytes
	}
	if !dirRe.MatchString(dir) {
		return nil, "", fmt.Errorf("%w: folder %q", ErrBadReference, dir)
	}
	root, err := os.OpenRoot(filepath.Join(profile, "Files", "S0"))
	if err != nil {
		return nil, "", fmt.Errorf("%w: %w", ErrNoFile, err)
	}
	defer func() { _ = root.Close() }()
	var errs []error
	for _, name := range names {
		if !nameRe.MatchString(name) {
			errs = append(errs, fmt.Errorf("%w: file %q", ErrBadReference, name))
			continue
		}
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

func readOne(root *os.Root, name string, limit int64) ([]byte, error) {
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: %w", name, fs.ErrInvalid)
	}
	raw, err := readLimited(f, limit)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if !bytes.HasPrefix(raw, gzipID) {
		return raw, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	out, err := readLimited(zr, limit)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
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
