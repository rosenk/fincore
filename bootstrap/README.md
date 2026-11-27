# bootstrap/ — the Stage-0 compiler `finc0` (Go), frozen at v0.2

`finc0` is the FinCore compiler written in Go. It exists to start the
bootstrap chain and nothing else:

```text
bootstrap/ (Go) ──go build──► finc0                     (frozen: language v0.2)
finc0  build compiler/ @ <bootstrap/SEED> ──► finc-seed  (seed release, its own runtime/)
finc-seed build compiler/ (current) ──► finc1
finc1  build compiler/ ──► finc2 ──► finc3
fixed point: sha256(stage2.go) == sha256(stage3.go)
```

`make seed` builds `.stage/finc-seed`; `make bootstrap`
(`scripts/bootstrap_verify.go`) runs the whole chain.

## finc0 is frozen

Up to the release tag `v0.2.0` every language feature was implemented
twice, in `bootstrap/` and in `compiler/`, and `make diff-*` checked that
both compilers agree byte for byte. From v0.2 on, new language versions are
implemented **only in `compiler/`**, the way Go and Rust bootstrap from a
previous release of themselves instead of from a second implementation:

- Implementing every feature twice doubled the cost of each language change
  and made the Go compiler, not the language, the bottleneck.
- `compiler/` is self-hosted and has passed the fixed point since M8, so it
  can carry the language forward. `finc0` only has to build one fixed old
  version of it, the **seed**.

**Allowed** in `bootstrap/`:

- bug fixes that keep `finc0` able to build `compiler/` at the seed tag
  (for example a newer Go toolchain or OS that breaks it, or a miscompilation
  of the seed);
- fixes to its tests and tooling, and Go-version upkeep.

**Not allowed:** language features, new diagnostics, or any change whose
purpose is to accept programs that v0.2 rejects. A feature goes into
`compiler/` (and docs/semantics_decisions.md) only.

The `finc0 vs finc1` differential targets (`make diff-parse`, `diff-check*`,
`diff-build`, `diff-build-vet`) remain as **regression checks of the frozen
v0.2 subset**: finc1 must still treat v0.2 programs exactly like finc0.
Once `compiler/` implements v0.3 features, these targets run only on
v0.2 inputs: a corpus file (or any .fc file of a package directory) that
needs a newer version says so in its leading comment block,

```go
// SINCE: v0.3
// pass_foo: what this test checks
package main
```

and scripts/diffcheck, scripts/diffbuild and `make diff-parse`
(scripts/sincefilter) skip it. The parser of the directive is
`scripts/langver` (`langver.Finc0` is `v0.2`). When `compiler/` itself
starts *using* a v0.3 feature (after a seed bump, below), put `// SINCE:
v0.3` at the top of the file that uses it, so the differential skips
compiler/ and its mutants.

## The seed (`bootstrap/SEED`)

`bootstrap/SEED` names the seed: an annotated git tag, the release of
`compiler/` that the chain builds before the current one. It is
`v0.2.0`, the last commit at which `finc0` and `compiler/` implement the
same language. The seed is extracted with `git archive <seed> go.mod
compiler std runtime` into `.stage/seed/<seed>` (the working tree is not
touched) and built there, against the seed's own `runtime/`; the later
stages build the current tree against the current `runtime/`.

The bootstrap therefore needs a git checkout with tags (`git fetch --tags`;
in CI `actions/checkout` with `fetch-depth: 0`). A tarball without `.git`
cannot bootstrap; `make seed`/`make bootstrap` stop with a message saying so.

### The rule: compiler/ may use a feature once a seed tag has it

`compiler/` is compiled by the seed compiler, so it may use only the
language the seed implements. Introducing and using a feature takes two
steps, as in Go and Rust:

1. Implement the feature in `compiler/` **without using it** in the
   compiler's own sources (tests, testdata with `// SINCE:`, examples may
   use it; so may std/ packages that compiler/ does not import). Merge,
   then cut an annotated release tag that contains it
   (`git tag -a v0.3.0 -m "..."`) and push the tag.
2. Bump the seed in `bootstrap/SEED` to that tag. From then on `compiler/`
   may use the feature; `make bootstrap` must pass.

The seed's own `compiler/` must be buildable by the compiler before it in
the chain. For the first bump that is finc0: v0.3.0 is fine as long as
compiler/ at v0.3.0 used only v0.2 (step 1 guarantees that). After that,
a newer seed whose compiler/ uses v0.3 features cannot be built by finc0
any more, so bootstrap/SEED becomes a list, one tag per line, oldest
first, and the chain goes through every one of them:

```text
v0.3.0      # built by finc0 (its compiler/ uses only v0.2)
v0.4.0      # built by finc-v0.3.0 (its compiler/ uses up to v0.3)
```

gives `finc0 → finc-v0.3.0 → finc-seed (v0.4.0) → finc1 → ...` (`#`
starts a comment). A bump appends the new tag; a leading tag may be
dropped only when finc0 can build the next one directly. The last line is
always the seed (`.stage/finc-seed`). The chain is
implemented in `scripts/bootstrap_verify.go` (`readSeeds`, `buildSeed`);
it is never a reason to add a feature to finc0.

Runtime caveat: stage 1 is Go generated by the *seed's* code generator,
built against the *current* `runtime/fincrt`. A runtime change that breaks
the seed's generated Go must stay backward compatible, or wait for a seed
whose code generator matches it.
