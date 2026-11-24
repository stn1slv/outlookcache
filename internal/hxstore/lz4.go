package hxstore

import (
	"errors"
	"fmt"
)

// ErrLZ4 reports an LZ4 payload that does not decode to the declared length.
var ErrLZ4 = errors.New("lz4: invalid block")

// DecodeLZ4 decodes one LZ4 block (no frame header) into exactly n bytes.
// Any other outcome is an error: short or long output, a match distance that
// points before the start of the output, or input left over at the end.
func DecodeLZ4(src []byte, n int) ([]byte, error) {
	dst := make([]byte, 0, n)
	i := 0
	for i < len(src) {
		token := src[i]
		i++

		lit, next, err := lz4Length(src, i, int(token>>4))
		if err != nil {
			return nil, err
		}
		i = next
		if lit > len(src)-i || lit > n-len(dst) {
			return nil, fmt.Errorf("%w: literal run of %d bytes at %d", ErrLZ4, lit, i)
		}
		dst = append(dst, src[i:i+lit]...)
		i += lit
		if i == len(src) {
			break // the last sequence has literals only
		}

		if len(src)-i < 2 {
			return nil, fmt.Errorf("%w: truncated match offset at %d", ErrLZ4, i)
		}
		dist := int(src[i]) | int(src[i+1])<<8
		i += 2
		if dist == 0 || dist > len(dst) {
			return nil, fmt.Errorf("%w: match distance %d with %d bytes of output", ErrLZ4, dist, len(dst))
		}

		ml, next, err := lz4Length(src, i, int(token&0x0f))
		if err != nil {
			return nil, err
		}
		i = next
		ml += 4
		if ml > n-len(dst) {
			return nil, fmt.Errorf("%w: match of %d bytes overflows output", ErrLZ4, ml)
		}
		start := len(dst) - dist
		if dist >= ml {
			dst = append(dst, dst[start:start+ml]...)
			continue
		}
		for k := range ml { // overlapping copy repeats the window byte by byte
			dst = append(dst, dst[start+k])
		}
	}
	if len(dst) != n {
		return nil, fmt.Errorf("%w: decoded %d bytes, want %d", ErrLZ4, len(dst), n)
	}
	return dst, nil
}

// lz4Length reads the 255-continuation extension of a 4-bit length field.
func lz4Length(src []byte, i, base int) (length, next int, err error) {
	length = base
	if base != 15 {
		return length, i, nil
	}
	for {
		if i >= len(src) {
			return 0, 0, fmt.Errorf("%w: truncated length at %d", ErrLZ4, i)
		}
		b := src[i]
		i++
		length += int(b)
		if length > 1<<30 {
			return 0, 0, fmt.Errorf("%w: length too large", ErrLZ4)
		}
		if b != 255 {
			return length, i, nil
		}
	}
}
