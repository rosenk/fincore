package diag

import "testing"

func TestFormat(t *testing.T) {
	d := New(E1001, SourceSpan{File: "a.fc", Start: Pos{Offset: 10, Line: 3, Col: 7}}, "expected %s", "'}'")
	want := "a.fc:3:7: error[E1001]: expected '}'"
	if d.String() != want {
		t.Fatalf("got %q want %q", d.String(), want)
	}
}

func TestCodes(t *testing.T) {
	all := AllCodes()
	if len(all) != 22 {
		t.Fatalf("want 22 codes, got %d", len(all))
	}
	for _, c := range all {
		if c.Name() == "" {
			t.Errorf("%s has no name", c)
		}
		p, ok := ParseCode(c.String())
		if !ok || p != c {
			t.Errorf("ParseCode(%s) = %v,%v", c, p, ok)
		}
	}
	if E3002.Name() != "SCOPE_ESCAPE" || E5002.String() != "E5002" {
		t.Fatal("bad names")
	}
	if _, ok := ParseCode("E9999"); ok {
		t.Fatal("E9999 should be unknown")
	}
}

func TestSort(t *testing.T) {
	sp := func(f string, off int) SourceSpan {
		return SourceSpan{File: f, Start: Pos{Offset: off, Line: 1, Col: off + 1}}
	}
	l := List{
		New(E2001, sp("b.fc", 0), "x"),
		New(E1001, sp("a.fc", 5), "y"),
		New(E1001, sp("a.fc", 1), "z"),
		New(E1001, sp("a.fc", 1), "z"),
		New(E1002, sp("a.fc", 1), "a"),
	}
	l.Sort()
	got := l.Error()
	want := "a.fc:1:2: error[E1001]: z\na.fc:1:2: error[E1002]: a\na.fc:1:6: error[E1001]: y\nb.fc:1:1: error[E2001]: x"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if (List{}).Err() != nil {
		t.Fatal("empty list should be nil error")
	}
}
