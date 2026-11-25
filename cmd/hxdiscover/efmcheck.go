package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/stn1slv/outlookcache/internal/body"
	"github.com/stn1slv/outlookcache/internal/store"
)

// efmCheck reads, read-only (body.ReadEFM), the EFMData files that the
// shipped parser finds for each message with an external body and prints
// counts only: whether the folder is the account's folder, whether each
// file can be read and holds a whole HTML document, and whether the files
// of one message hold the same content.
func efmCheck(data []byte, profile string, w io.Writer) error {
	blocks, s, err := ownBlocks(data)
	if err != nil {
		return err
	}
	dirOf := map[string]string{}
	for _, a := range s.Accounts {
		dirOf[a.ID] = a.Dir
	}
	acc := map[uint64]string{}
	for _, m := range s.Messages {
		if id, err := strconv.ParseUint(m.ID, 16, 64); err == nil {
			acc[id] = m.AccountID
		}
	}
	c := map[string]int{}
	for id, pl := range blocks {
		ref := store.ParseBody(pl)
		if ref.Kind != store.BodyExternal {
			continue
		}
		c[fmt.Sprintf("external bodies: files %d, account folder=%t", min(len(ref.File), 3), ref.Dir == dirOf[acc[id]])]++
		var got [][]byte
		for i, name := range ref.File {
			b, _, err := body.ReadEFM(profile, ref.Dir, []string{name}, 0)
			switch {
			case errors.Is(err, body.ErrNoFile):
				c[fmt.Sprintf("file %d: missing or not HTML text", min(i, 2))]++
			case err != nil:
				c[fmt.Sprintf("file %d: error", min(i, 2))]++
			default:
				c[fmt.Sprintf("file %d: read, whole document=%t, size %s", min(i, 2), isHTMLDoc(b), sizeBucket(len(b)))]++
				got = append(got, b)
			}
		}
		if len(got) >= 2 {
			c[fmt.Sprintf("two readable files: same content=%t", bytes.Equal(got[0], got[1]))]++
		}
	}
	printCounts(w, "EFMData bodies (counts only)", c)
	return nil
}
