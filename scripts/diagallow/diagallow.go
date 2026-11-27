// Package diagallow matches intentional wording changes of diagnostics,
// for the allowlists of scripts/diffcheck (finc0 vs finc1) and
// scripts/diffprev (previous release vs current compiler).
//
// A rule says that a diagnostic of one code was reworded:
//
//	E3002 "(depth 0), which outlives it" => "each parameter is its own region" # reason
//
// The code is E followed by four digits; the two texts are Go string
// literals (strconv.Unquote); the reason after ` # ` is required. Two
// outputs (stderr of the old and the new compiler) are explained by a set
// of rules when they have the same lines except for pairs of diagnostic
// lines `<file>:<line>:<col>: error[<code>]: <message>` with the same
// position and code whose old message contains the old text and whose new
// message contains the new text of a rule for that code. Positions and
// codes can therefore never differ through a rule; only message text can.
//
// A driver rule rewords the compiler's own messages, the lines that are
// not diagnostics (usage, I/O errors, "finc1: codegen: ..."):
//
//	driver "finc1: " => "finc: " # reason
//
// It explains a pair of differing lines, neither of them a diagnostic,
// when replacing the first occurrence of the old text in the old line
// gives the new line.
package diagallow

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Rule is one wording change.
type Rule struct {
	Code   string // E####, or "driver"
	Old    string // substring of the old message
	New    string // substring of the new message
	Reason string
	Used   int // number of explained line pairs
}

func (r *Rule) String() string {
	return fmt.Sprintf("%s %q => %q", r.Code, r.Old, r.New)
}

var ruleHead = regexp.MustCompile(`^(E[0-9]{4}|driver) "`)

// IsRule reports whether an allowlist line (trimmed) has the form of a rule.
func IsRule(line string) bool {
	return ruleHead.MatchString(line)
}

// ParseRule parses `E#### "old" => "new" # reason` or
// `driver "old" => "new" # reason`.
func ParseRule(line string) (*Rule, error) {
	line = strings.TrimSpace(line)
	if !IsRule(line) {
		return nil, errors.New("want `E#### \"old text\" => \"new text\" # reason` or `driver \"old text\" => ...`")
	}
	code, rest, _ := strings.Cut(line, " ")
	r := &Rule{Code: code}
	rest = strings.TrimSpace(rest)
	old, rest, err := quoted(rest)
	if err != nil {
		return nil, fmt.Errorf("old text: %v", err)
	}
	rest, ok := strings.CutPrefix(strings.TrimSpace(rest), "=>")
	if !ok {
		return nil, errors.New("want `=>` after the old text")
	}
	nw, rest, err := quoted(strings.TrimSpace(rest))
	if err != nil {
		return nil, fmt.Errorf("new text: %v", err)
	}
	reason, ok := strings.CutPrefix(strings.TrimSpace(rest), "#")
	reason = strings.TrimSpace(reason)
	if !ok || reason == "" {
		return nil, errors.New("want ` # <reason>` after the new text")
	}
	if old == "" || nw == "" {
		return nil, errors.New("empty text")
	}
	r.Old, r.New, r.Reason = old, nw, reason
	return r, nil
}

// quoted splits a leading Go string literal off s.
func quoted(s string) (string, string, error) {
	if !strings.HasPrefix(s, `"`) {
		return "", "", errors.New("want a quoted string")
	}
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			v, err := strconv.Unquote(s[:i+1])
			return v, s[i+1:], err
		}
	}
	return "", "", errors.New("unterminated string")
}

var diagLine = regexp.MustCompile(`^(\S+:[0-9]+:[0-9]+: error\[(E[0-9]{4})\]: )(.*)$`)

// Explain reports whether the rules explain every difference between the
// outputs old and cur (see the package comment). It counts the uses of
// the rules only when they explain everything.
func Explain(rules []*Rule, old, cur string) bool {
	if old == cur || len(rules) == 0 {
		return old == cur
	}
	a := strings.Split(old, "\n")
	b := strings.Split(cur, "\n")
	if len(a) != len(b) {
		return false
	}
	var used []*Rule
	for i := range a {
		if a[i] == b[i] {
			continue
		}
		r := explainLine(rules, a[i], b[i])
		if r == nil {
			return false
		}
		used = append(used, r)
	}
	for _, r := range used {
		r.Used++
	}
	return true
}

// explainLine returns the rule explaining the pair of lines, or nil.
func explainLine(rules []*Rule, old, cur string) *Rule {
	ma := diagLine.FindStringSubmatch(old)
	mb := diagLine.FindStringSubmatch(cur)
	if ma == nil && mb == nil {
		for _, r := range rules {
			if r.Code == "driver" && strings.Contains(old, r.Old) && strings.Replace(old, r.Old, r.New, 1) == cur {
				return r
			}
		}
		return nil
	}
	if ma == nil || mb == nil || ma[1] != mb[1] {
		return nil // a diagnostic appeared or vanished, or its position or code changed
	}
	for _, r := range rules {
		if r.Code == ma[2] && strings.Contains(ma[3], r.Old) && strings.Contains(mb[3], r.New) {
			return r
		}
	}
	return nil
}
