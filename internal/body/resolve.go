package body

import (
	"errors"
	"fmt"

	"github.com/stn1slv/outlookcache/internal/store"
)

// ErrNoBody reports a message without a readable stored body (kind none or
// unparsed); its preview may still be available.
var ErrNoBody = errors.New("no stored body")

// HTML returns the HTML of a body reference: inline bytes as they are, or
// the content of its EFMData file under profile (see ReadEFM). source names
// where the body came from: "inline" or "efmdata:<file>".
func HTML(ref store.BodyRef, profile string, limit int64) (doc []byte, source string, err error) {
	switch ref.Kind {
	case store.BodyInline:
		if limit > 0 && int64(len(ref.HTML)) > limit {
			return nil, store.BodyInline, ErrTooLarge
		}
		return ref.HTML, store.BodyInline, nil
	case store.BodyExternal:
		b, name, err := ReadEFM(profile, ref.Dir, ref.File, limit)
		if err != nil {
			return nil, store.BodyExternal, err
		}
		return b, store.BodyExternal + ":" + name, nil
	default:
		return nil, ref.Kind, fmt.Errorf("%w: %s", ErrNoBody, ref.Kind)
	}
}
