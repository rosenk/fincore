package fincrt

import (
	"bytes"
	"strconv"
	"unsafe"
)

// Copy copies min(len(dst), len(src)) elements and returns the count.
func Copy[T any](dst, src []T) uint { return uint(copy(dst, src)) }

// BytesEqual reports whether a and b hold the same bytes.
func BytesEqual(a, b []byte) bool { return bytes.Equal(a, b) }

// StrBytes returns a read-only, zero-copy view of s's bytes.
// Writing to the result is undefined behaviour.
func StrBytes(s string) []byte {
	if len(s) == 0 {
		return nil
	}
	return unsafe.Slice(unsafe.StringData(s), len(s))
}

// BytesToStr copies b into a new string.
func BytesToStr(b []byte) string { return string(b) }

// FormatUint writes val in decimal into dst and returns the byte count.
// ErrArenaFull (and 0) if dst is too small.
func FormatUint(val uint64, dst []byte) (uint, error) {
	var tmp [20]byte
	s := strconv.AppendUint(tmp[:0], val, 10)
	if len(s) > len(dst) {
		return 0, ErrArenaFull
	}
	return uint(copy(dst, s)), nil
}

// ParseUint parses a non-empty decimal ASCII number. ErrParse on empty input,
// non-digit bytes or overflow of uint64.
func ParseUint(src []byte) (uint64, error) {
	if len(src) == 0 {
		return 0, ErrParse
	}
	var v uint64
	for _, c := range src {
		if c < '0' || c > '9' {
			return 0, ErrParse
		}
		d := uint64(c - '0')
		if v > (^uint64(0)-d)/10 {
			return 0, ErrParse
		}
		v = v*10 + d
	}
	return v, nil
}
