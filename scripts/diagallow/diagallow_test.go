package diagallow

import "testing"

func TestParseRule(t *testing.T) {
	r, err := ParseRule(`E1001 "expected expression, found '='" => "compound assignment operator" # v0.3: clear message # really`)
	if err != nil {
		t.Fatal(err)
	}
	if r.Code != "E1001" || r.Old != "expected expression, found '='" || r.New != "compound assignment operator" || r.Reason != "v0.3: clear message # really" {
		t.Fatalf("%+v", r)
	}
	if r, err := ParseRule(`E3002 "a \"q\" \\ b" => "c" # why`); err != nil || r.Old != `a "q" \ b` {
		t.Fatalf("%+v %v", r, err)
	}
	for _, bad := range []string{
		`E1001 "a" => "b"`,
		`E1001 "a" => "b" #`,
		`E1001 "a" "b" # r`,
		`E1001 "a => "b" # r`,
		`E1001 "" => "b" # r`,
		`E101 "a" => "b" # r`,
		`testdata/fail/x.fc # reason`,
	} {
		if _, err := ParseRule(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if IsRule("testdata/fail/x.fc # reason") || !IsRule(`E1001 "x" => "y" # r`) || !IsRule(`driver "x" => "y" # r`) {
		t.Error("IsRule")
	}
	if r, err := ParseRule(`driver "finc1: " => "finc: " # program name`); err != nil || r.Code != "driver" || r.Old != "finc1: " || r.New != "finc: " {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestExplainDriver(t *testing.T) {
	r, err := ParseRule(`driver "finc1: " => "finc: " # program name`)
	if err != nil {
		t.Fatal(err)
	}
	rules := []*Rule{r}
	for _, tc := range []struct {
		old, cur string
		want     bool
	}{
		{"finc1: codegen: no main\n", "finc: codegen: no main\n", true},
		{"finc1: codegen: no main\n", "finc: codegen: other\n", false},
		{"a.fc:1:1: error[E1001]: finc1: x\n", "a.fc:1:1: error[E1001]: finc: x\n", false},
		{"finc1: x\n", "a.fc:1:1: error[E1001]: x\n", false},
	} {
		if got := Explain(rules, tc.old, tc.cur); got != tc.want {
			t.Errorf("%q -> %q: %v, want %v", tc.old, tc.cur, got, tc.want)
		}
	}
}

func TestExplain(t *testing.T) {
	rule := func() []*Rule {
		r, err := ParseRule(`E1001 "expected expression, found '='" => "compound assignment operator" # r`)
		if err != nil {
			t.Fatal(err)
		}
		return []*Rule{r}
	}
	old := "a.fc:4:6: error[E1001]: expected expression, found '='\n"
	for _, tc := range []struct {
		name, old, cur string
		want           bool
	}{
		{"identical", old, old, true},
		{"reworded", old, "a.fc:4:6: error[E1001]: compound assignment operator >>= is not supported; write c = c >> 1\n", true},
		{"column changed", old, "a.fc:4:7: error[E1001]: compound assignment operator >>= is not supported\n", false},
		{"code changed", old, "a.fc:4:6: error[E1002]: compound assignment operator >>= is not supported\n", false},
		{"other text", old, "a.fc:4:6: error[E1001]: something else\n", false},
		{"other old text", "a.fc:4:6: error[E1001]: expected type, found ','\n", "a.fc:4:6: error[E1001]: compound assignment operator\n", false},
		{"extra line", old, "a.fc:4:6: error[E1001]: compound assignment operator\nerror: fincore: invalid number\n", false},
		{"not a diagnostic", "usage: finc0 x\n", "usage: finc x\n", false},
	} {
		rs := rule()
		if got := Explain(rs, tc.old, tc.cur); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
		if want := 0; tc.want && tc.old != tc.cur {
			want = 1
			if rs[0].Used != want {
				t.Errorf("%s: used %d, want %d", tc.name, rs[0].Used, want)
			}
		} else if rs[0].Used != want {
			t.Errorf("%s: used %d, want %d", tc.name, rs[0].Used, want)
		}
	}
}
