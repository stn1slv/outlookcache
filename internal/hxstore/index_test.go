package hxstore_test

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stn1slv/outlookcache/internal/hxstore"
	"github.com/stn1slv/outlookcache/internal/hxtest"
)

func TestReadIndexDirectSlots(t *testing.T) {
	t.Parallel()
	a := hxtest.Block(8, 0x18800001, []byte("first block payload, long enough"))
	b := hxtest.Block(8, 0x18800002, []byte("second block payload, also long"))
	data := hxtest.FileWithIndex([][]byte{a, b}, []bool{true, false})
	ix, err := hxstore.ReadIndex(data)
	require.NoError(t, err)
	assert.Equal(t, uint32(1), ix.Entries)

	var offs []int64
	_, _, err = hxstore.Scan(data, "t", func(blk hxstore.Block) error {
		offs = append(offs, blk.Offset)
		return nil
	})
	require.NoError(t, err)
	require.Len(t, offs, 2)
	assert.True(t, ix.Direct(offs[0]))
	assert.False(t, ix.Direct(offs[1]))
	assert.False(t, ix.Direct(offs[0]+16), "not a block start")
}

func TestReadIndexRejectsBadAreas(t *testing.T) {
	t.Parallel()
	data := hxtest.FileWithIndex(nil, nil)
	for _, b := range []uint64{0, hxtest.IndexA, hxtest.IndexA + 1, 1 << 62, ^uint64(0)} {
		bad := bytes.Clone(data)
		binary.LittleEndian.PutUint64(bad[0x78:], b)
		require.NotPanics(t, func() {
			_, err := hxstore.ReadIndex(bad)
			assert.ErrorIs(t, err, hxstore.ErrNotHxStore)
		})
	}
	_, err := hxstore.ReadIndex(data[:0x40])
	assert.ErrorIs(t, err, hxstore.ErrNotHxStore)
}

func TestForEachFraming(t *testing.T) {
	t.Parallel()
	obj := func(id uint64, typ uint16, n int) []byte {
		b := make([]byte, n)
		copy(b, hxstore.FramingMagic())
		binary.LittleEndian.PutUint64(b[4:], id)
		binary.LittleEndian.PutUint16(b[12:], typ)
		binary.LittleEndian.PutUint32(b[14:], uint32(n))
		return b
	}
	broken := obj(3, 0xc7, 40)
	binary.LittleEndian.PutUint32(broken[14:], 4000) // longer than the payload
	payload := bytes.Join([][]byte{{1, 2, 3}, obj(1, 0xc7, 30), obj(2, 0xcc, 20), broken}, nil)
	var got []hxstore.Framing
	hxstore.ForEachFraming(payload, func(f hxstore.Framing) { got = append(got, f) })
	require.Len(t, got, 2)
	assert.Equal(t, uint64(1), got[0].ID)
	assert.Equal(t, uint16(0xc7), got[0].Type)
	assert.Len(t, got[0].Data, 30)
	assert.Equal(t, 3, got[0].Offset)
	assert.Equal(t, uint16(0xcc), got[1].Type)
}
