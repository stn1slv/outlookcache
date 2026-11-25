package body

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stn1slv/outlookcache/internal/store"
)

func TestHTML(t *testing.T) {
	t.Parallel()
	dir := profile(t, map[string][]byte{"7.dat": gz(t, page)})

	b, src, err := HTML(store.BodyRef{Kind: store.BodyInline, HTML: []byte(page)}, dir, 0)
	require.NoError(t, err)
	assert.Equal(t, page, string(b))
	assert.Equal(t, "inline", src)

	_, _, err = HTML(store.BodyRef{Kind: store.BodyInline, HTML: []byte(page)}, dir, 10)
	require.ErrorIs(t, err, ErrTooLarge)

	b, src, err = HTML(store.BodyRef{Kind: store.BodyExternal, Dir: "2", File: []string{"7.dat"}}, dir, 0)
	require.NoError(t, err)
	assert.Equal(t, page, string(b))
	assert.Equal(t, "efmdata:7.dat", src)

	for _, kind := range []string{store.BodyNone, store.BodyUnparsed} {
		_, _, err = HTML(store.BodyRef{Kind: kind}, dir, 0)
		require.ErrorIs(t, err, ErrNoBody, kind)
	}
}
