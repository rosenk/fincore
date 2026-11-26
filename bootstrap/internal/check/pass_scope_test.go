package check

import (
	"os"
	"strings"
	"testing"
)

// The scope checker's fail tests report exactly their marked diagnostic.
func TestScopeFailCorpus(t *testing.T) {
	moduleRoot(t)
	for _, name := range []string{
		"fail_conn_leak", "fail_scope_escape_return", "fail_scope_escape_var",
		"fail_slot_leak", "fail_use_after_move",
	} {
		t.Run(name, func(t *testing.T) {
			path := "testdata/fail/" + name + ".fc"
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var want []string
			for i, line := range strings.Split(string(src), "\n") {
				if m := markerRE.FindStringSubmatch(line); m != nil {
					want = append(want, m[1]+"@"+itoa(i+1))
				}
			}
			res, err := Run(path, Options{})
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, d := range res.Diags {
				got = append(got, d.Code.String()+"@"+itoa(d.Span.Start.Line))
			}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("got %v, want %v\n%v", got, want, res.Diags)
			}
		})
	}
}
