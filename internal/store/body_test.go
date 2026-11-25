package store

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stn1slv/outlookcache/internal/hxtest"
)

const testClassAt = 1893

// bodyRecord builds a body record: u32 size fields before the class, the
// UTF-16 class, a zero unit, then the given fields without terminators.
func bodyRecord(sizes map[int]uint32, fields ...[]byte) []byte {
	pre := make([]byte, testClassAt)
	for at, v := range sizes {
		binary.LittleEndian.PutUint32(pre[testClassAt+at:], v)
	}
	parts := append([][]byte{pre, encodeU16("IPM.Note"), {0, 0}}, fields...)
	return bytes.Join(append(parts, []byte{0, 0}), nil)
}

// bodyHead is a body record followed by an object framing that ends it.
func bodyHead(sizes map[int]uint32, fields ...[]byte) []byte {
	return append(bodyRecord(sizes, fields...), framing(tItem, typeMessage, 40, nil)...)
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
		{"fragment ending in a line break", bodyHead(map[int]uint32{bodyMainAt: n}, []byte(frag+"\r\n"), []byte(doc))},
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
	assert.Equal(t, BodyUnparsed, ParseBody(bodyHead(map[int]uint32{bodyMainAt: 0x00001234})).Kind, "flag 0 with a size")
	assert.Equal(t, BodyUnparsed, ParseBody(bodyHead(map[int]uint32{bodyMainAt: 0x82000010})).Kind, "file flag without a path")
	assert.Equal(t, BodyUnparsed, ParseBody(bodyHead(map[int]uint32{bodyMainAt: 0x5d001234})).Kind, "unknown flags")
	assert.Equal(t, BodyUnparsed, ParseBody(bodyHead(map[int]uint32{bodyMainAt: 0x80000005}, []byte(doc))).Kind, "size that is not a document")
	assert.Equal(t, BodyUnparsed, ParseBody(make([]byte, 100)).Kind, "no class")
	short := bytes.Join([][]byte{make([]byte, 10), encodeU16("IPM.Note")}, nil)
	assert.Equal(t, BodyUnparsed, ParseBody(short).Kind, "class too close to the start")

	// Text before the class offset is never taken as the class, and paths
	// before the class are ignored.
	rec := bodyHead(map[int]uint32{bodyMainAt: 0x82000010}, []byte{0x05, 0}, path("2", "17.dat"))
	copy(rec[100:], encodeU16("IPM.Decoy"))
	copy(rec[400:], path("9", "99.dat"))
	assert.Equal(t, BodyRef{Kind: BodyExternal, Dir: "2", File: []string{"17.dat"}}, ParseBody(rec))
}

func TestStoreBody(t *testing.T) {
	t.Parallel()
	_, err := (&Store{}).Body("zz")
	require.ErrorIs(t, err, ErrUnknownMessage)
	_, err = (&Store{}).Body("18800001")
	require.ErrorIs(t, err, ErrUnknownMessage)
}

// loadWithBody loads a store whose item block starts with a body record.
// With foreign, the item copy sits in a block keyed by another ID; own
// adds a separate block keyed by the item ID that holds the body record.
func loadWithBody(t *testing.T, foreign, own bool) *Store {
	t.Helper()
	sent := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	rec := bodyRecord(map[int]uint32{bodyMainAt: uint32(len(doc)) | 0x80000000}, []byte(doc))
	itemKey := uint64(tItem)
	if foreign {
		itemKey = 0x18900009
	}
	blocks := [][]byte{
		hxtest.Block(8, tRoot, rootPayload()),
		hxtest.Block(8, tInbox, framing(tInbox, 0x1e3, 40, nil)),
		hxtest.Block(8, itemKey, append(bytes.Clone(rec), itemPayload(sent, true)...)),
	}
	if own {
		blocks = append(blocks, hxtest.Block(8, tItem, append(bytes.Clone(rec), framing(tItem, 0x154, 40, nil)...)))
	}
	data := hxtest.FileWithIndex(blocks, []bool{true, true, true, true})
	s, err := Load(data, "t.hxd")
	require.NoError(t, err)
	require.Len(t, s.Messages, 1)
	return s
}

func TestLoadAndBody(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		foreign, own bool
		want         string
	}{
		{"own block", false, false, BodyInline},
		{"copy in a foreign block, own block present", true, true, BodyInline},
		{"copy in a foreign block only", true, false, BodyUnparsed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := loadWithBody(t, tc.foreign, tc.own)
			m := s.Messages[0]
			assert.Equal(t, tc.want, m.BodyKind)
			ref, err := s.Body(m.ID)
			require.NoError(t, err)
			assert.Equal(t, tc.want, ref.Kind, "Body agrees with BodyKind")
			if tc.want == BodyInline {
				assert.Equal(t, doc, string(ref.HTML))
			}
		})
	}
}
