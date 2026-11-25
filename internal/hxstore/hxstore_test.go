package hxstore_test

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stn1slv/outlookcache/internal/hxstore"
	"github.com/stn1slv/outlookcache/internal/hxtest"
)

func TestDecodeLZ4RoundTrip(t *testing.T) {
	t.Parallel()
	tests := map[string][]byte{
		"empty":       {},
		"short":       []byte("abc"),
		"repeating":   bytes.Repeat([]byte("ab"), 5000), // overlapping matches
		"long run":    bytes.Repeat([]byte{0}, 70000),
		"mixed":       append([]byte("header-"), bytes.Repeat([]byte("Inbox/Projects/X "), 300)...),
		"incompress.": []byte("the quick brown fox jumps over the lazy dog 0123456789"),
	}
	for name, src := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := hxstore.DecodeLZ4(hxtest.EncodeLZ4(src), len(src))
			require.NoError(t, err)
			assert.Equal(t, src, got)
		})
	}
}

func TestDecodeLZ4RejectsWrongLength(t *testing.T) {
	t.Parallel()
	src := bytes.Repeat([]byte("xyz"), 100)
	comp := hxtest.EncodeLZ4(src)
	_, err := hxstore.DecodeLZ4(comp, len(src)+1)
	require.ErrorIs(t, err, hxstore.ErrLZ4)
	_, err = hxstore.DecodeLZ4(comp, len(src)-1)
	require.ErrorIs(t, err, hxstore.ErrLZ4)
	_, err = hxstore.DecodeLZ4([]byte{0x00, 0x05, 0x00}, 10) // distance before the window
	require.ErrorIs(t, err, hxstore.ErrLZ4)
}

func FuzzDecodeLZ4(f *testing.F) {
	f.Add(hxtest.EncodeLZ4(bytes.Repeat([]byte("ab"), 50)), 100)
	f.Add([]byte{0xff, 0xff, 0xff}, 10)
	f.Fuzz(func(t *testing.T, src []byte, n int) {
		out, err := hxstore.DecodeLZ4(src, n)
		if err == nil && len(out) != n {
			t.Fatalf("decoded %d bytes without error, want %d", len(out), n)
		}
	})
}

func TestParseHeader(t *testing.T) {
	t.Parallel()
	h, err := hxstore.ParseHeader(hxtest.File())
	require.NoError(t, err)
	assert.True(t, h.Supported())
	assert.Equal(t, uint64(4096), h.PageSize)

	_, err = hxstore.ParseHeader([]byte("SQLite format 3\x00 and more bytes to reach the header size....."))
	require.ErrorIs(t, err, hxstore.ErrNotHxStore)
}

func TestScanValidatesBlocks(t *testing.T) {
	t.Parallel()
	good := hxtest.Block(8, 0x18806590, bytes.Repeat([]byte("message-a "), 50))
	badHeader := hxtest.Block(8, 0, []byte("damaged header block"))
	badHeader[0] ^= 0xff
	badPayload := hxtest.Block(8, 0, bytes.Repeat([]byte("damaged payload "), 20))
	badPayload[len(badPayload)-1] ^= 0xff
	other := hxtest.Block(16, 0, []byte("type sixteen")) // synthetic; real type-16 blocks never validated
	data := hxtest.File(good, badHeader, badPayload, other)

	var payloads []string
	var keys []uint64
	st, damage, err := hxstore.Scan(data, "HxStore.hxd", func(b hxstore.Block) error {
		payloads = append(payloads, string(b.Payload[:12]))
		keys = append(keys, b.Key)
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 4, st.Candidates)
	assert.Equal(t, 2, st.Valid)
	assert.Equal(t, map[uint32]int{8: 1, 16: 1}, st.ByType)
	assert.Equal(t, []string{"message-a me", "type sixteen"}, payloads)
	require.Len(t, damage, 2)
	assert.Equal(t, "header crc mismatch", damage[0].Reason)
	assert.Equal(t, "payload crc mismatch", damage[1].Reason)
	assert.Equal(t, "HxStore.hxd", damage[0].File)
	assert.Equal(t, []uint64{0x18806590, 0}, keys)
}

func TestScanShortInput(t *testing.T) {
	t.Parallel()
	for _, n := range []int{0, 7, 0x40, 0x47} {
		st, damage, err := hxstore.Scan(make([]byte, n), "f", func(hxstore.Block) error { return nil })
		require.NoError(t, err)
		assert.Zero(t, st.Candidates)
		assert.Empty(t, damage)
	}
}

func TestScanDamageReasons(t *testing.T) {
	t.Parallel()
	// Truncated header: the magic sits in the last bytes of the file.
	data := append(hxtest.File(), make([]byte, 8)...)
	data = append(data, hxtest.BlockMagic...)
	_, damage, err := hxstore.Scan(data, "f", func(hxstore.Block) error { return nil })
	require.NoError(t, err)
	require.Len(t, damage, 1)
	assert.Equal(t, "truncated header", damage[0].Reason)

	// Over the 32 MiB cap, with a valid header CRC.
	big := hxtest.Block(8, 0, []byte("payload over the cap"))
	binary.LittleEndian.PutUint32(big[0x18:], 33<<20)
	binary.LittleEndian.PutUint32(big[0:], crc32.ChecksumIEEE(big[4:0x20]))
	_, damage, err = hxstore.Scan(hxtest.File(big), "f", func(hxstore.Block) error { return nil })
	require.NoError(t, err)
	require.Len(t, damage, 1)
	assert.Equal(t, "length over 32 MiB cap", damage[0].Reason)

	// Wrong inflated length with valid CRCs: the decoder must refuse it.
	bad := hxtest.Block(8, 0, bytes.Repeat([]byte("abc"), 40))
	binary.LittleEndian.PutUint32(bad[0x18:], 119)
	binary.LittleEndian.PutUint32(bad[4:], crc32.ChecksumIEEE(bad[8:]))
	binary.LittleEndian.PutUint32(bad[0:], crc32.ChecksumIEEE(bad[4:0x20]))
	_, damage, err = hxstore.Scan(hxtest.File(bad), "f", func(hxstore.Block) error { return nil })
	require.NoError(t, err)
	require.Len(t, damage, 1)
	assert.Contains(t, damage[0].Reason, "decompress")
}

func TestDecodeLZ4RejectsBadSizes(t *testing.T) {
	t.Parallel()
	for _, n := range []int{-1, 33 << 20, 255*3 + 17} {
		_, err := hxstore.DecodeLZ4([]byte{0x30, 'a', 'b', 'c'}, n)
		require.ErrorIs(t, err, hxstore.ErrLZ4, "n=%d", n)
	}
}

func FuzzScan(f *testing.F) {
	f.Add(hxtest.File(hxtest.Block(8, 1, bytes.Repeat([]byte("fuzz "), 30))))
	f.Add([]byte("Nostromo"))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _, err := hxstore.Scan(data, "f", func(b hxstore.Block) error {
			if b.Payload == nil {
				t.Fatal("valid block without payload")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}

func TestScanTruncatedBlock(t *testing.T) {
	t.Parallel()
	blk := hxtest.Block(8, 0, bytes.Repeat([]byte("cut "), 100))
	data := hxtest.File(blk)[:0x1000+len(blk)-2]
	st, damage, err := hxstore.Scan(data, "f", func(hxstore.Block) error { return nil })
	require.NoError(t, err)
	assert.Equal(t, 0, st.Valid)
	require.Len(t, damage, 1)
	assert.Equal(t, "truncated payload", damage[0].Reason)
}

func TestScanStop(t *testing.T) {
	t.Parallel()
	data := hxtest.File(hxtest.Block(8, 0, []byte("first block")), hxtest.Block(8, 0, []byte("second block")))
	calls := 0
	_, _, err := hxstore.Scan(data, "f", func(hxstore.Block) error {
		calls++
		return hxstore.ErrStop
	})
	require.NoError(t, err)
	assert.Equal(t, 1, calls)
}
