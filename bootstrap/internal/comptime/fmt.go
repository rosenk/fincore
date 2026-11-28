package comptime

// Fmt is the writer behind the FinCore builtin formatTo (v0.2, see
// docs/semantics_decisions.md, "v0.2 additions"). The compilers expand
// every formatTo call at compile time into a sequence of Fmt method calls:
//
//	formatTo(dst, "n=%5d %q", n, s)
//	  ->
//	func(_fc_d []byte, _fc_a0 int64, _fc_a1 string) uint {
//		_fc_w := fincrt.NewFmt(_fc_d)
//		_fc_w.Lit("n=")
//		_fc_w.Int(_fc_a0, 5, false)
//		_fc_w.Lit(" ")
//		_fc_w.Quote(_fc_a1, 0)
//		return _fc_w.N()
//	}(dst, int64(n), s)
//
// Every method writes into the destination starting at the current
// position and silently drops bytes beyond len(dst), so the result is
// always a prefix of the untruncated output. No method allocates or
// panics.
//
// width: 0 = no padding, w > 0 = right-justify in w bytes, w < 0 =
// left-justify in -w bytes (flag '-'). zero: pad numbers with '0' after the
// sign instead of spaces (flag '0'; ignored when left-justifying).
type Fmt struct {
	buf []byte
	n   int
	// scratch receives the output of a right-justified Format method when
	// the remaining room is smaller than the width (see Dst).
	scratch   [MaxFmtWidth]byte
	inScratch bool
}

// MaxFmtWidth is the largest width a formatTo directive may specify.
const MaxFmtWidth = 64

// NewFmt returns a writer that fills dst from index 0.
func NewFmt(dst []byte) Fmt { return Fmt{buf: dst} }

// N returns the number of bytes written so far (always <= len(dst)).
func (f *Fmt) N() uint { return uint(f.n) }

func (f *Fmt) put(c byte) {
	if f.n < len(f.buf) {
		f.buf[f.n] = c
		f.n++
	}
}

func (f *Fmt) putStr(s string) {
	for i := 0; i < len(s); i++ {
		f.put(s[i])
	}
}

func (f *Fmt) fill(c byte, k int) {
	for ; k > 0; k-- {
		f.put(c)
	}
}

// padLeft writes the padding that precedes an item of l bytes.
func (f *Fmt) padLeft(l, width int) {
	if width > l {
		f.fill(' ', width-l)
	}
}

// padRight writes the padding that follows an item of l bytes.
func (f *Fmt) padRight(l, width int) {
	if width < 0 && -width > l {
		f.fill(' ', -width-l)
	}
}

// Lit writes literal format text (no padding).
func (f *Fmt) Lit(s string) { f.putStr(s) }

// Str writes s (verbs %s and %v).
func (f *Fmt) Str(s string, width int) {
	f.padLeft(len(s), width)
	f.putStr(s)
	f.padRight(len(s), width)
}

// Bytes writes b (verbs %s and %v).
func (f *Fmt) Bytes(b []byte, width int) {
	f.padLeft(len(b), width)
	for _, c := range b {
		f.put(c)
	}
	f.padRight(len(b), width)
}

// Byte writes the single byte c (verb %c).
func (f *Fmt) Byte(c byte, width int) {
	f.padLeft(1, width)
	f.put(c)
	f.padRight(1, width)
}

// Bool writes true or false (verb %v).
func (f *Fmt) Bool(v bool, width int) {
	if v {
		f.Str("true", width)
	} else {
		f.Str("false", width)
	}
}

// quotedLen is the length of the escape sequence of byte c inside a %q
// literal.
func quotedLen(c byte) int {
	switch {
	case c == '\n' || c == '\r' || c == '\t' || c == '\\' || c == '"':
		return 2
	case c < 0x20 || c >= 0x7f:
		return 4
	}
	return 1
}

const hexDigits = "0123456789abcdef"

func (f *Fmt) putQuoted(c byte) {
	switch c {
	case '\n':
		f.putStr(`\n`)
	case '\r':
		f.putStr(`\r`)
	case '\t':
		f.putStr(`\t`)
	case '\\':
		f.putStr(`\\`)
	case '"':
		f.putStr(`\"`)
	default:
		if c < 0x20 || c >= 0x7f {
			f.put('\\')
			f.put('x')
			f.put(hexDigits[c>>4])
			f.put(hexDigits[c&15])
		} else {
			f.put(c)
		}
	}
}

// Quote writes s as a double-quoted FinCore/Go string literal (verb %q):
// \n \r \t \\ \" and \xHH (lowercase) for every other byte < 0x20, 0x7f and
// every byte >= 0x80.
func (f *Fmt) Quote(s string, width int) {
	l := 2
	for i := 0; i < len(s); i++ {
		l += quotedLen(s[i])
	}
	f.padLeft(l, width)
	f.put('"')
	for i := 0; i < len(s); i++ {
		f.putQuoted(s[i])
	}
	f.put('"')
	f.padRight(l, width)
}

// QuoteBytes is Quote for a byte slice.
func (f *Fmt) QuoteBytes(b []byte, width int) {
	l := 2
	for _, c := range b {
		l += quotedLen(c)
	}
	f.padLeft(l, width)
	f.put('"')
	for _, c := range b {
		f.putQuoted(c)
	}
	f.put('"')
	f.padRight(l, width)
}

// number writes an optional '-' and the digits of mag in base 10 or 16.
func (f *Fmt) number(neg bool, mag uint64, base uint64, width int, zero bool) {
	var tmp [20]byte
	i := len(tmp)
	for {
		i--
		tmp[i] = hexDigits[mag%base]
		mag /= base
		if mag == 0 {
			break
		}
	}
	digits := tmp[i:]
	l := len(digits)
	if neg {
		l++
	}
	if zero && width > 0 {
		if neg {
			f.put('-')
		}
		if width > l {
			f.fill('0', width-l)
		}
	} else {
		f.padLeft(l, width)
		if neg {
			f.put('-')
		}
	}
	for _, c := range digits {
		f.put(c)
	}
	f.padRight(l, width)
}

func signMag(v int64) (bool, uint64) {
	if v < 0 {
		return true, -uint64(v)
	}
	return false, uint64(v)
}

// Int writes a signed integer in decimal (verbs %d and %v).
func (f *Fmt) Int(v int64, width int, zero bool) {
	neg, mag := signMag(v)
	f.number(neg, mag, 10, width, zero)
}

// Uint writes an unsigned integer in decimal (verbs %d and %v).
func (f *Fmt) Uint(v uint64, width int, zero bool) { f.number(false, v, 10, width, zero) }

// HexInt writes a signed integer in lowercase hex without prefix; negative
// values get a leading '-' (verb %x).
func (f *Fmt) HexInt(v int64, width int, zero bool) {
	neg, mag := signMag(v)
	f.number(neg, mag, 16, width, zero)
}

// Hex writes an unsigned integer in lowercase hex without prefix (verb %x).
func (f *Fmt) Hex(v uint64, width int, zero bool) { f.number(false, v, 16, width, zero) }

// Dst returns the buffer a Format method writes into for a %v directive
// with the given width; the generated code is
// `w.Advance(arg.Format(w.Dst(width)), width)`. Normally this is the
// unwritten remainder of the destination. When right-justifying (width >
// 0) and the remainder is shorter than the width, it is a width-byte
// scratch buffer instead, so that Advance can compute the padding from the
// untruncated length (the method is still called exactly once).
func (f *Fmt) Dst(width int) []byte {
	if width > MaxFmtWidth {
		width = MaxFmtWidth
	}
	if width > len(f.buf)-f.n {
		f.inScratch = true
		return f.scratch[:width:width]
	}
	f.inScratch = false
	return f.buf[f.n:len(f.buf):len(f.buf)]
}

// Advance accounts for the n bytes a Format method wrote into Dst(width)
// (clamped to its length) and applies the width: right-justifying puts the
// spaces before them, left-justifying after them.
func (f *Fmt) Advance(n uint, width int) {
	if f.inScratch {
		f.inScratch = false
		if w := uint(min(width, MaxFmtWidth)); n > w {
			n = w
		}
		f.Bytes(f.scratch[:n], width)
		return
	}
	if room := uint(len(f.buf) - f.n); n > room {
		n = room
	}
	l := int(n)
	if width > l {
		// room >= width: shift the l bytes right by pad and fill the gap
		pad := width - l
		start := f.n
		for i := l - 1; i >= 0; i-- {
			if j := start + pad + i; j < len(f.buf) {
				f.buf[j] = f.buf[start+i]
			}
		}
		f.fill(' ', pad)
		f.n = min(start+pad+l, len(f.buf))
		return
	}
	f.n += l
	f.padRight(l, width)
}
