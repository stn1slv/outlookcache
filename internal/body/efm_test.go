package body

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const page = "<html><body><p>Synthetic body</p></body></html>"

// profile writes EFMData files under a temp profile folder.
func profile(t *testing.T, files map[string][]byte) string {
	t.Helper()
	dir := t.TempDir()
	efm := filepath.Join(dir, "Files", "S0", "2", "EFMData")
	require.NoError(t, os.MkdirAll(efm, 0o700))
	for name, b := range files {
		require.NoError(t, os.WriteFile(filepath.Join(efm, name), b, 0o600))
	}
	return dir
}

func gz(t *testing.T, s string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, err := zw.Write([]byte(s))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func TestReadEFM(t *testing.T) {
	t.Parallel()
	dir := profile(t, map[string][]byte{"10.dat": gz(t, page), "11.dat": []byte(page)})

	b, name, err := ReadEFM(dir, "2", []string{"9.dat", "10.dat"}, 0)
	require.NoError(t, err)
	assert.Equal(t, "10.dat", name, "a missing first file falls back to the next")
	assert.Equal(t, page, string(b))

	b, _, err = ReadEFM(dir, "2", []string{"11.dat"}, 0)
	require.NoError(t, err)
	assert.Equal(t, page, string(b), "plain files are read as they are")

	_, _, err = ReadEFM(dir, "2", []string{"9.dat"}, 0)
	require.ErrorIs(t, err, ErrNoFile)
}

func TestReadEFMRejectsBadReferences(t *testing.T) {
	t.Parallel()
	dir := profile(t, map[string][]byte{"10.dat": []byte(page)})
	for _, tc := range []struct{ dir, name string }{
		{"../2", "10.dat"}, {"2/..", "10.dat"}, {"", "10.dat"}, {"2", "../10.dat"}, {"2", "10.txt"}, {"2", "/etc/hosts"},
	} {
		_, _, err := ReadEFM(dir, tc.dir, []string{tc.name}, 0)
		require.ErrorIs(t, err, ErrBadReference, "%q %q", tc.dir, tc.name)
	}
}

func TestReadEFMDoesNotFollowLinksOut(t *testing.T) {
	t.Parallel()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	require.NoError(t, os.WriteFile(outside, []byte("outside"), 0o600))
	dir := profile(t, nil)
	require.NoError(t, os.Symlink(outside, filepath.Join(dir, "Files", "S0", "2", "EFMData", "12.dat")))
	_, _, err := ReadEFM(dir, "2", []string{"12.dat"}, 0)
	require.ErrorIs(t, err, ErrNoFile)
}

func TestReadEFMLimits(t *testing.T) {
	t.Parallel()
	big := strings.Repeat("a", 100)
	dir := profile(t, map[string][]byte{"1.dat": []byte(big), "2.dat": gz(t, big), "3.dat": {0x1f, 0x8b, 1, 2}})
	for _, name := range []string{"1.dat", "2.dat"} {
		_, _, err := ReadEFM(dir, "2", []string{name}, 50)
		require.ErrorIs(t, err, ErrTooLarge, name)
	}
	_, _, err := ReadEFM(dir, "2", []string{"3.dat"}, 0)
	require.ErrorIs(t, err, ErrNoFile, "broken gzip")
}

func TestReadEFMChecksContentAndKinds(t *testing.T) {
	t.Parallel()
	tiny := gz(t, "<p>x</p>") // the gzip file is larger than its content
	dir := profile(t, map[string][]byte{"1.dat": []byte("not html"), "2.dat": tiny, "3.dat": {'<', 0xff, 0xfe}})
	b, name, err := ReadEFM(dir, "2", []string{"1.dat", "3.dat", "2.dat"}, 8)
	require.NoError(t, err)
	assert.Equal(t, "2.dat", name, "text that is not HTML or not UTF-8 falls back to the next file")
	assert.Equal(t, "<p>x</p>", string(b), "the limit applies after decompression")

	_, _, err = ReadEFM(dir, "2", nil, 0)
	require.ErrorIs(t, err, ErrBadReference)

	fifo := filepath.Join(dir, "Files", "S0", "2", "EFMData", "4.dat")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))
	_, _, err = ReadEFM(dir, "2", []string{"4.dat"}, 0)
	require.ErrorIs(t, err, ErrNoFile, "a FIFO is not opened")
}
