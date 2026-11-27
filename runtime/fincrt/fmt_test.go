package fincrt

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fmtCase renders one item through Fmt into a buffer of the given size.
type fmtCase struct {
	name string
	run  func(f *Fmt)
	want string // untruncated output
}

func fmtCases() []fmtCase {
	return []fmtCase{
		{"lit", func(f *Fmt) { f.Lit("abc%") }, "abc%"},
		{"str", func(f *Fmt) { f.Str("hi", 0) }, "hi"},
		{"str-right", func(f *Fmt) { f.Str("hi", 5) }, "   hi"},
		{"str-left", func(f *Fmt) { f.Str("hi", -5) }, "hi   "},
		{"str-narrow", func(f *Fmt) { f.Str("hello", 3) }, "hello"},
		{"bytes", func(f *Fmt) { f.Bytes([]byte("xy"), -4) }, "xy  "},
		{"byte", func(f *Fmt) { f.Byte('Z', 3) }, "  Z"},
		{"bool", func(f *Fmt) { f.Bool(true, 0); f.Bool(false, -6) }, "truefalse "},
		{"int", func(f *Fmt) { f.Int(-42, 0, false) }, "-42"},
		{"int-zero", func(f *Fmt) { f.Int(-42, 6, true) }, "-00042"},
		{"int-space", func(f *Fmt) { f.Int(-42, 6, false) }, "   -42"},
		{"int-left-zero", func(f *Fmt) { f.Int(7, -4, true) }, "7   "},
		{"int-min", func(f *Fmt) { f.Int(math.MinInt64, 0, false) }, "-9223372036854775808"},
		{"uint-max", func(f *Fmt) { f.Uint(math.MaxUint64, 0, false) }, "18446744073709551615"},
		{"uint-zero", func(f *Fmt) { f.Uint(0, 3, true) }, "000"},
		{"hex", func(f *Fmt) { f.Hex(0xdeadBEEF, 0, false) }, "deadbeef"},
		{"hex-zero", func(f *Fmt) { f.Hex(255, 8, true) }, "000000ff"},
		{"hexint-neg", func(f *Fmt) { f.HexInt(-255, 0, false) }, "-ff"},
		{"hexint-min", func(f *Fmt) { f.HexInt(math.MinInt64, 0, false) }, "-8000000000000000"},
		{"quote", func(f *Fmt) { f.Quote("a\"b\\c\n\r\t\x00\x7f\xff\xe2\x82\xac", 0) }, `"a\"b\\c\n\r\t\x00\x7f\xff\xe2\x82\xac"`},
		{"quote-pad", func(f *Fmt) { f.Quote("a", 5) }, `  "a"`},
		{"quote-bytes", func(f *Fmt) { f.QuoteBytes([]byte{1, 'x'}, -9) }, `"\x01x"  `},
	}
}

func render(c fmtCase, size int) (string, uint) {
	buf := make([]byte, size)
	f := NewFmt(buf)
	c.run(&f)
	return string(buf[:f.N()]), f.N()
}

func TestFmtItems(t *testing.T) {
	for _, c := range fmtCases() {
		got, _ := render(c, 256)
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
		// Truncation: every smaller buffer receives a prefix.
		for size := 0; size <= len(c.want); size++ {
			got, n := render(c, size)
			if int(n) != size || got != c.want[:size] {
				t.Errorf("%s (size %d): got %q (n=%d), want %q", c.name, size, got, n, c.want[:size])
			}
		}
	}
}

func TestFmtIntsMatchStrconv(t *testing.T) {
	vals := []int64{0, 1, -1, 9, 10, -10, 12345, -98765, math.MaxInt64, math.MinInt64, math.MinInt64 + 1}
	for _, v := range vals {
		buf := make([]byte, 128)
		f := NewFmt(buf)
		f.Int(v, 0, false)
		f.Lit(" ")
		f.HexInt(v, 0, false)
		f.Lit(" ")
		f.Uint(uint64(v), 0, false)
		f.Lit(" ")
		f.Hex(uint64(v), 0, false)
		want := strconv.FormatInt(v, 10) + " " + strconv.FormatInt(v, 16) + " " +
			strconv.FormatUint(uint64(v), 10) + " " + strconv.FormatUint(uint64(v), 16)
		if got := string(buf[:f.N()]); got != want {
			t.Errorf("%d: got %q, want %q", v, got, want)
		}
		for _, w := range []int{-25, -3, 3, 25} {
			for _, zero := range []bool{false, true} {
				f := NewFmt(buf)
				f.Int(v, w, zero)
				var spec string
				switch {
				case w < 0:
					spec = fmt.Sprintf("%%-%dd", -w)
				case zero:
					spec = fmt.Sprintf("%%0%dd", w)
				default:
					spec = fmt.Sprintf("%%%dd", w)
				}
				if got, want := string(buf[:f.N()]), fmt.Sprintf(spec, v); got != want {
					t.Errorf("%d %s: got %q, want %q", v, spec, got, want)
				}
			}
		}
	}
}

// fakeFormat imitates a generated Format method: it writes "<v>" with
// truncation and returns the count.
func fakeFormat(v string, dst []byte) uint {
	return uint(copy(dst, "<"+v+">"))
}

func TestFmtAdvance(t *testing.T) {
	cases := []struct {
		width int
		want  string
	}{
		{0, "[<ab>]"},
		{6, "[  <ab>]"},
		{-6, "[<ab>  ]"},
		{2, "[<ab>]"},
		{-2, "[<ab>]"},
		{64, "[" + strings.Repeat(" ", 60) + "<ab>]"},
	}
	for _, c := range cases {
		run := func(size int) (string, uint) {
			buf := make([]byte, size)
			f := NewFmt(buf)
			f.Lit("[")
			f.Advance(fakeFormat("ab", f.Dst(c.width)), c.width)
			f.Lit("]")
			return string(buf[:f.N()]), f.N()
		}
		if got, _ := run(128); got != c.want {
			t.Errorf("width %d: got %q, want %q", c.width, got, c.want)
		}
		for size := 0; size <= len(c.want); size++ {
			got, n := run(size)
			if int(n) != size || got != c.want[:size] {
				t.Errorf("width %d size %d: got %q (n=%d), want %q", c.width, size, got, n, c.want[:size])
			}
		}
	}
	// A misbehaving method that claims more than the room is clamped.
	buf := make([]byte, 3)
	f := NewFmt(buf)
	f.Lit("a")
	f.Advance(100, 0)
	if f.N() != 3 {
		t.Errorf("clamp: n=%d, want 3", f.N())
	}
	f.Advance(5, 10)
	if f.N() != 3 {
		t.Errorf("clamp at end: n=%d, want 3", f.N())
	}
	if r := f.Dst(0); len(r) != 0 || cap(r) != 0 {
		t.Errorf("rest at end: len %d cap %d", len(r), cap(r))
	}
}

func TestFmtNoAlloc(t *testing.T) {
	buf := make([]byte, 64)
	s := "hello"
	b := []byte("xyz")
	allocs := testing.AllocsPerRun(100, func() {
		f := NewFmt(buf)
		f.Lit("x=")
		f.Int(-12345, 8, true)
		f.Str(s, -7)
		f.QuoteBytes(b, 0)
		f.Hex(0xabc, 0, false)
		f.Advance(fakeFormat2(f.Dst(4)), 4)
		_ = f.N()
	})
	if allocs != 0 {
		t.Errorf("Fmt allocates: %v allocs per run", allocs)
	}
}

func fakeFormat2(dst []byte) uint {
	n := 0
	for _, c := range []byte("ok") {
		if n < len(dst) {
			dst[n] = c
			n++
		}
	}
	return uint(n)
}

// TestFmtConcurrent runs independent writers in parallel (run under -race).
func TestFmtConcurrent(t *testing.T) {
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			buf := make([]byte, 32)
			for i := 0; i < 1000; i++ {
				f := NewFmt(buf)
				f.Int(int64(g*i), 6, true)
				f.Quote("x", 0)
				if f.N() != 9 {
					t.Errorf("n=%d", f.N())
					return
				}
			}
		}(g)
	}
	wg.Wait()
}
