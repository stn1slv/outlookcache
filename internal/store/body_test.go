package store

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testClassAt = 1893

// bodyHead builds a body record: u32 size fields before the class, the
// UTF-16 class, a zero unit, then the given fields without terminators,
// then an object framing that ends the head.
func bodyHead(sizes map[int]uint32, fields ...[]byte) []byte {
	pre := make([]byte, testClassAt)
	for at, v := range sizes {
		binary.LittleEndian.PutUint32(pre[testClassAt+at:], v)
	}
	parts := append([][]byte{pre, encodeU16("IPM.Note"), {0, 0}}, fields...)
	parts = append(parts, []byte{0, 0}, framing(tItem, typeMessage, 40, nil))
	return bytes.Join(parts, nil)
}

const doc = "<html><body><p>Hello</p></body></html>\r\n"

func TestParseBodyInline(t *testing.T) {
	t.Parallel()
	frag := "<div>new text</div>"
	n := uint32(len(doc)) | 0x80000000
	tests := []struct {
		name string
		head []byte
	}{
		{"body first", bodyHead(map[int]uint32{bodyMainAt: n, bodySecondAt: n}, []byte(doc), []byte(doc))},
		{"fragment sized at -214", bodyHead(map[int]uint32{bodyMainAt: n, bodyPrefixAt: uint32(len(frag))}, []byte(frag), []byte(doc))},
		{"fragment sized at -202", bodyHead(map[int]uint32{bodyMainAt: n, bodySecondAt: uint32(len(frag)) | 0x80000000}, []byte(frag), []byte(doc))},
		{"fragment without a size", bodyHead(map[int]uint32{bodyMainAt: n}, []byte(frag), []byte(doc), []byte("tail"))},
		{"upper-case end tag and non-ASCII text", bodyHead(map[int]uint32{bodyMainAt: uint32(len("<HTML>Grüße</HTML>")) | 0x80000000}, []byte(frag+"ä"), []byte("<HTML>Grüße</HTML>"))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ref := ParseBody(tt.head)
			require.Equal(t, BodyInline, ref.Kind)
			assert.True(t, isHTMLDoc(ref.HTML))
			assert.Contains(t, []string{doc, "<HTML>Grüße</HTML>"}, string(ref.HTML))
		})
	}
}

func TestParseBodyOtherKinds(t *testing.T) {
	t.Parallel()
	path := func(dir, name string) []byte {
		return append(encodeU16("/Users/x/Library/Files/S0/"+dir+"/EFMData/"+name), 0, 0)
	}
	ext := bodyHead(map[int]uint32{bodyMainAt: 0x8200003e},
		[]byte{0x05, 0}, path("2", "17.dat"), []byte{0x05, 0}, path("2", "18.dat"), []byte{0x05, 0}, path("2", "17.dat"),
		[]byte{0x05, 0}, path("9", "99.dat"), []byte{0x05, 0}, path("2", "../x.dat"))
	assert.Equal(t, BodyRef{Kind: BodyExternal, Dir: "2", File: []string{"17.dat", "18.dat"}}, ParseBody(ext),
		"another folder and a bad name are ignored")

	assert.Equal(t, BodyNone, ParseBody(bodyHead(nil)).Kind)
	assert.Equal(t, BodyUnparsed, ParseBody(bodyHead(map[int]uint32{bodyMainAt: 0x82000010})).Kind, "file flag without a path")
	assert.Equal(t, BodyUnparsed, ParseBody(bodyHead(map[int]uint32{bodyMainAt: 0x5d001234})).Kind, "unknown flags")
	assert.Equal(t, BodyUnparsed, ParseBody(bodyHead(map[int]uint32{bodyMainAt: 0x80000005}, []byte(doc))).Kind, "size that is not a document")
	assert.Equal(t, BodyUnparsed, ParseBody(make([]byte, 100)).Kind, "no class")
	short := bytes.Join([][]byte{make([]byte, 10), encodeU16("IPM.Note")}, nil)
	assert.Equal(t, BodyUnparsed, ParseBody(short).Kind, "class too close to the start")
}

func TestStoreBody(t *testing.T) {
	t.Parallel()
	_, err := (&Store{}).Body("zz")
	require.ErrorIs(t, err, ErrUnknownMessage)
	_, err = (&Store{}).Body("18800001")
	require.ErrorIs(t, err, ErrUnknownMessage)
}
