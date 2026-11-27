# Testing FinCore after the finc0 freeze

## What changed

Up to language v0.2 (tag `v0.2.0`) every feature existed twice: in the
Go compiler `finc0` (`bootstrap/`) and in the self-hosted compiler
(`compiler/`). `make diff-parse`, `diff-check*` and `diff-build*` compared the
two byte for byte on the corpus, the unit-test snippets and thousands of
mutants. finc0 was the oracle: any difference was a bug in one of them, and
two independent implementations seldom make the same mistake.

finc0 is now frozen at v0.2 (`bootstrap/README.md`). New language versions
are implemented only in `compiler/`, which bootstraps from its own previous
release (`finc0 → finc-seed → finc1 → finc2 → finc3`). The finc0
differential keeps running, but only on the v0.2 subset: inputs marked
`// SINCE: v0.3` or later are skipped. **A new feature has no second
implementation to be compared with.** The checks below replace it. None of
them is as strong as an independent oracle on its own, so they are used
together.

## The layers

### 1. Exhaustive pass/fail conformance tests

`testdata/pass` and `testdata/fail`, with the conventions of
`testdata/README.md`. Without an oracle the expected behaviour has to be
written down, so a feature is not done until the tests cover it:

- a pass test for every syntactic form and every interaction with the
  existing features, with its expected output (`.out`). Run output catches
  wrong code that still compiles;
- a fail test for every diagnostic the feature adds, with an exact
  `// ERROR E####` marker (file, line, code; no other diagnostic allowed);
- a `// SINCE: vX.Y` header on each of these tests.

Each runner (`finc0 test`, `finc1 test`, the Go runner in
`testdata/integration`) knows the language version of its compiler
(`finc version`) and skips newer tests, reporting
`N skipped (needs vX.Y)`. So finc0 keeps passing the corpus, and the
current compiler must pass all of it (`finc2 test testdata` in
`make bootstrap`).

A test needs the highest `SINCE` of its import closure: every `.fc` file
of its package and, transitively, every local or std package it imports
(resolved from the module root, std through `$FINCROOT` when set). The
directive may therefore sit in any file of a directory test or in an
imported package, and a test that only imports a std package using v0.3
needs no marker of its own. The three runners and the diff tools
(`scripts/langver`) implement this one rule identically; the exact rule
and the textual import scan are in testdata/README.md ("SINCE").

### 2. Regression comparison with the previous release (`make diff-prev`)

`scripts/diffprev` runs the current compiler (`bin/finc1`) and the previous
release (`.stage/finc-seed`, compiler/ at the last tag of `bootstrap/SEED`,
built by `make seed`) on everything the finc0 differential uses: examples,
testdata, the parser fixtures, `scripts/diff*/cases`, std/, compiler/, the
FINCROOT cases, the compiler/ and std/ mutants and the FinCore snippets of
finc0's unit tests (848 cases today). It compares:

- `parse --dump-ast`;
- `check --passes=types|scope|totality|all`;
- `build --emit-go` (the emitted Go, byte for byte);
- for programs both compilers build: `go vet` on the emitted Go, and for
  cases with a `.out`, the run output (stdout and exit status) of both
  binaries and the `.out` file.

Every difference is classified; the worst step decides a case:

| Class | Meaning | Result |
| :--- | :--- | :--- |
| REGRESSION | the previous release accepted the program and the current compiler rejects it; or both accept and the AST dump, emitted Go or run output differs; or a `// ERROR` fail test is accepted now; or go build / go vet rejects the emitted Go | fails |
| DIAG-CHANGED | both reject the program with different diagnostics | fails unless allowlisted |
| EXPECTED | the previous release rejected the program and the current compiler accepts it (a new feature), or the case's `// SINCE:` is newer than the previous release and both reject it. A program only the current compiler builds must still pass `go vet` and match its `.out` file, if any (else REGRESSION) | ok, counted |

Intentional changes (a reworded message, a codegen improvement) go into
`scripts/diffprev/allow.txt` as `<case name> # <reason>`. A reworded
diagnostic is better allowlisted by a rule,
`E#### "old text" => "new text" # <reason>` (scripts/diagallow): it
covers every DIAG-CHANGED case whose only differences are diagnostics at
the same position with the same code, the old message containing the old
text and the new one the new text (`driver "old" => "new"` does the same
for the compiler's own non-diagnostic lines). `make diff-check*` reads
the same rules from `scripts/diffcheck/allow.txt`. Unused entries
are reported, and the list empties when the seed moves past the change. The
summary looks like this:

```
diff-prev (current v0.2 vs previous release v0.2.0, v0.2): 848 cases, 848 identical, 0 expected (new features), 0 regressions, 0 diag-changed, 0 allowlisted (33s)
```

`-v` prints every case and the details of EXPECTED and allowlisted ones.
This is the property the finc0 differential gave for old programs: **a new
version must not change the meaning of any v0.2 program.** It does not
check the new feature itself. Layer 1 does that.

### 3. Mutation fuzzing against the seed (`make diff-prev-fuzz`)

The mutators of `diff-check-fuzz` (identifier swaps, literal and operator
changes, deleted, duplicated and moved lines, and the loop and measure edits
aimed at the totality checker) produce N mutants of every corpus file and
snippet (`-mutate 20 -seed $(FUZZ_SEED)`). They are compared with the same
classification; a mutant needs the language version of its source (its
`// SINCE:`), so mutants of new-version tests rejected by both are EXPECTED. Most mutants are invalid programs, so this mostly exercises
the diagnostics of the error paths, the code that hand-written tests cover
least. A mutant that the seed accepted and the current compiler rejects, or
that gets different diagnostics, is a regression. Accepted mutants are also
built, so their emitted Go is compared too.

### 4. Go compile and vet of all emitted code

finc's output is Go. A program finc accepts but `go build` rejects means
the type checker is unsound, and `go vet` findings point at codegen bugs.
`finc build` compiles every pass test. diff-prev fails on a go build
failure and vets every program the current compiler builds (also new
programs the previous release rejects, whose run output it checks against
their `.out` file). `make diff-build-vet`
does the same for the v0.2 subset, and the bootstrap vets stage1.go.

### 5. The fixed point (`make bootstrap`)

`finc0 → finc-seed → finc1 → finc2 → finc3` with
`sha256(stage2.go) == sha256(stage3.go)`. compiler/ is the largest FinCore
program. Building itself twice to identical Go checks that the new compiler
compiles a real program the same way the seed did. finc2 then has to pass
`testdata` and the integration suite.

## Why this is enough, and where it is weaker

- **Old behaviour** is covered as strongly as before. diff-prev compares
  against the previous release on the same inputs and mutants as the finc0
  differential. The seed is the v0.2 compiler that agreed with finc0, so
  it inherits that oracle.
- **New behaviour** has no differential oracle. It is specified by the
  conformance tests, and only as well as they cover it. Reviews of a new
  feature should treat missing pass or fail tests as missing code.
- **Codegen soundness** of new constructs rests on run output (`.out`), Go
  compilation and vet, and the fixed point once compiler/ uses the feature.
- When a release is tagged and becomes the new seed, its behaviour turns
  into the regression baseline for the next version. Then the allowlist
  and the EXPECTED cases of that version go back to zero.

## Gates

| Target | What |
| :--- | :--- |
| `make test race lint` | Go unit tests (finc0, runners, scripts) |
| `make diff-parse diff-check* diff-build*` | finc0 vs finc1 on the v0.2 subset |
| `make diff-prev` | current compiler vs the previous release |
| `make diff-prev-fuzz` | the same plus mutants (`FUZZ_SEED=n`) |
| `make bootstrap` | seed chain and fixed point, `finc2 test testdata`, integration with finc2 |
| `make integration` | corpus runner and TCP echo E2E tests |
| `make fmt-fc-check` | `finc fmt` on every .fc file: idempotent and AST-preserving (docs/fmt.md) |
| `make context-test` | golden `check --context` output of `testdata/context` (docs/fmt.md) |
