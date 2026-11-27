SHELL := /bin/bash
GO      ?= go
BIN     := bin
FINC0   := $(BIN)/finc0
STAGE   := .stage
FUEL    ?= 1000000
SEED    ?= 1

export CGO_ENABLED ?= 0

.PHONY: all build finc0 test race vet fmt fmt-check lint parse-corpus check-corpus \
        integration finc1 diff-parse diff-check diff-check-scope diff-check-totality diff-check-all \
        diff-check-fuzz diff-build diff-build-vet finc2 bootstrap ci clean help dist dist-test

all: vet test build ## vet, test and build finc0

help: ## list targets
	@grep -E '^[a-zA-Z0-9_-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-14s %s\n", $$1, $$2}'

build: finc0 ## build the Stage 0 compiler

finc0: ## build bin/finc0 (static, trimpath)
	@mkdir -p $(BIN)
	$(GO) build -trimpath -o $(FINC0) ./bootstrap/cmd/finc0

test: ## unit tests
	$(GO) test -count=1 ./...

race: ## unit tests with the race detector (needs cgo)
	CGO_ENABLED=1 $(GO) test -race -count=1 ./...

vet: ## go vet
	$(GO) vet ./...

fmt: ## gofmt all Go files
	gofmt -w $$(git ls-files '*.go')

fmt-check: ## fail if any Go file is not gofmt-ed
	@out=$$(gofmt -l $$(git ls-files '*.go')); test -z "$$out" || { echo "$$out"; exit 1; }

lint: fmt-check vet ## fmt-check + vet

parse-corpus: finc0 ## finc0 parse on examples, testdata, std/ and compiler/ (v0.2 inputs: no newer // SINCE:)
	@set -e; fs=$$($(GO) run ./scripts/sincefilter examples/*.fc testdata/pass/*.fc testdata/fail/*.fc std/*/ compiler/); \
	for f in $$fs; do $(FINC0) parse $$f >/dev/null; done
	@echo "parse-corpus: ok"

check-corpus: finc0 ## finc0 check on examples, pass tests, std/ and compiler/ (v0.2 inputs: no newer // SINCE:)
	@set -e; fs=$$($(GO) run ./scripts/sincefilter examples/*.fc testdata/pass/*.fc std/*/ compiler/); \
	for f in $$fs; do $(FINC0) check --comptime-fuel=$(FUEL) $$f; done
	@echo "check-corpus: ok"

.PHONY: examples-test tutorial-check
examples-test: finc1 ## bin/finc1 builds examples/*.fc; runs those with a .out and compares stdout
	@set -eo pipefail; mkdir -p $(STAGE); for f in examples/*.fc; do o=$${f%.fc}.out; \
	  $(BIN)/finc1 build -o $(STAGE)/example $$f; \
	  if [ -f $$o ]; then $(STAGE)/example | cmp -s - $$o || { echo "examples-test: $$f output differs from $$o"; exit 1; }; fi; \
	done; echo "examples-test: ok"

tutorial-check: finc1 ## complete snippets of docs/tutorial.md compile; shown compiler output matches
	$(GO) run ./scripts/tutorialcheck -finc $(BIN)/finc1 docs/tutorial.md

integration: ## end-to-end TCP echo + corpus tests (skips until finc0 can build)
	$(GO) test -count=1 -v ./testdata/integration/

# --- bootstrap chain: finc0 (frozen at v0.2) -> finc-seed (compiler/ at the
# tag in bootstrap/SEED) -> finc1 (current compiler/). See bootstrap/README.md.
.PHONY: seed
seed: ## build .stage/finc-seed: finc0 builds compiler/ at the bootstrap/SEED tag (needs git tags)
	$(GO) run ./scripts/bootstrap_verify.go -seed-only

finc1: finc0 seed ## build bin/finc1: finc-seed builds the current compiler/ (Stage 1)
	$(STAGE)/finc-seed build --emit-go=$(STAGE)/stage1.go -o $(BIN)/finc1 ./compiler

bootstrap: finc0 ## fixed-point gate: finc0 -> finc-seed -> finc1 -> finc2 -> finc3, stage2.go == stage3.go
	$(GO) run ./scripts/bootstrap_verify.go
# --- end bootstrap chain

# --- release archive (docs/release.md): dist/fincore-<version>-<os>-<arch>.tar.gz
# with the finc2 of a fresh bootstrap. VERSION defaults to the exact tag of
# HEAD, else `git describe`. The archive is reproducible (SOURCE_DATE_EPOCH,
# default: commit time of HEAD).
DIST    := dist
VERSION ?= $(shell git describe --tags --exact-match HEAD 2>/dev/null || git describe --tags --always --dirty 2>/dev/null || echo dev)
dist: ## build $(DIST)/fincore-<version>-<os>-<arch>.tar.gz from a fresh bootstrap (finc2)
	$(GO) run ./scripts/bootstrap_verify.go -skip-integration
	$(GO) run ./scripts/dist -version $(VERSION) -finc $(STAGE)/finc2 -out $(DIST)

dist-test: ## smoke-test the archive: unpack, then build/run/test from another directory with no env vars
	$(GO) run ./scripts/dist -test -version $(VERSION) -out $(DIST)
# --- end release archive

ci: lint test build integration ## what CI runs

clean: ## remove build outputs
	rm -rf $(BIN) $(STAGE) $(DIST)

# --- finc0 vs finc1 differential: regression checks of the FROZEN v0.2 subset.
# finc0 implements only v0.2. Inputs that need a newer version say
# `// SINCE: vX.Y` in their leading comment block and are skipped
# (scripts/langver; diff-parse via scripts/sincefilter).
diff-parse: finc1 ## [v0.2 regression] finc0 vs finc1: byte-identical `parse --dump-ast` on the corpus
	@set -e; ts=$$($(GO) run ./scripts/sincefilter examples/tcp_echo.fc compiler/ std/*/ testdata/pass/*.fc testdata/fail/*.fc); \
	for t in $$ts; do \
	  cmp -s <($(FINC0) parse --dump-ast $$t 2>&1) <($(BIN)/finc1 parse --dump-ast $$t 2>&1 | grep -v '^error: fincore:') || { echo "DIFF: $$t"; exit 1; }; \
	done; echo "diff-parse: ok"

diff-check: finc1 ## [v0.2 regression] finc0 vs finc1 check --passes=types: identical diagnostics (corpus + snippets)
	$(GO) run ./scripts/diffcheck

diff-check-scope: finc1 ## [v0.2 regression] finc0 vs finc1 check --passes=scope (corpus + snippets incl. scope tests)
	$(GO) run ./scripts/diffcheck -passes scope

diff-check-totality: finc1 ## [v0.2 regression] finc0 vs finc1 check --passes=totality (corpus, snippets, compiler mutants)
	$(GO) run ./scripts/diffcheck -passes=totality

diff-check-all: finc1 ## [v0.2 regression] finc0 vs finc1 check (all passes)
	$(GO) run ./scripts/diffcheck -passes=all

diff-check-fuzz: finc1 ## [v0.2 regression] diff-check plus 20 random mutants per corpus file (SEED=n)
	$(GO) run ./scripts/diffcheck -mutate 20 -seed $(SEED)

diff-build: finc1 ## [v0.2 regression] finc0 vs finc1: byte-identical `build --emit-go` (corpus, edge cases, snippets, compiler/)
	$(GO) run ./scripts/diffbuild

diff-build-vet: finc1 ## [v0.2 regression] diff-build plus `go vet` on the emitted Go of every built case
	$(GO) run ./scripts/diffbuild -govet
# --- end finc0 vs finc1 differential

# --- diff-prev: current compiler/ (bin/finc1) vs the previous release
# (.stage/finc-seed), the oracle for new language versions. docs/testing.md.
.PHONY: diff-prev diff-prev-fuzz
FUZZ_SEED ?= 1
diff-prev: finc1 seed ## current compiler vs previous release (seed): parse/check/emitted Go/vet/run; REGRESSION + DIAG-CHANGED fail
	$(GO) run ./scripts/diffprev

diff-prev-fuzz: finc1 seed ## diff-prev plus 20 random mutants per corpus file and snippet (FUZZ_SEED=n)
	$(GO) run ./scripts/diffprev -mutate 20 -seed $(FUZZ_SEED)
# --- end diff-prev

.PHONY: spec-check
spec-check: finc1 ## finc1 check on every complete FinCore program of the v0.2 spec
	$(GO) run ./scripts/speccheck -finc $(BIN)/finc1 fincore_language_specification_v0_2.md

finc2: finc1 ## fixed-point smoke: finc1 -> finc2 -> finc3, cmp stage2.go stage3.go
	@mkdir -p $(STAGE)
	$(BIN)/finc1 build --emit-go=$(STAGE)/stage2.go -o $(BIN)/finc2 ./compiler
	$(BIN)/finc2 build --emit-go=$(STAGE)/stage3.go -o $(BIN)/finc3 ./compiler
	cmp $(STAGE)/stage2.go $(STAGE)/stage3.go && echo "finc2: stage2.go == stage3.go"

# --- tooling of the current compiler (bin/finc1): fmt and --context (docs/fmt.md)
.PHONY: fmt-fc fmt-fc-check context-test
fmt-fc: finc1 ## list the .fc files of compiler/ std/ examples/ whose formatting differs from `finc fmt`
	@$(BIN)/finc1 fmt -l compiler std examples

fmt-fc-check: finc1 ## finc fmt on every .fc file of the repo: idempotent and AST-preserving (scripts/fmtcheck)
	$(GO) run ./scripts/fmtcheck

context-test: finc1 ## golden `check --context` output of testdata/context (UPDATE=1 rewrites the .out files)
	@set -e; for f in testdata/context/*.fc; do o=$${f%.fc}.out; \
	  got=$$($(BIN)/finc1 check --context $$f 2>&1 | grep -v '^error: fincore:' || true); \
	  if [ -n "$(UPDATE)" ]; then printf '%s\n' "$$got" >$$o; fi; \
	  diff -u $$o <(printf '%s\n' "$$got") || { echo "context-test: $$f differs from $$o"; exit 1; }; \
	  cmp -s <(FINC_DIAG=context $(BIN)/finc1 check $$f 2>&1) <($(BIN)/finc1 check --context $$f 2>&1) || { echo "context-test: FINC_DIAG=context differs from --context on $$f"; exit 1; }; \
	  cmp -s <($(BIN)/finc1 check $$f 2>&1 | grep -v '^error: fincore:') <(grep -E '^[^ ]+\.fc:[0-9]+:[0-9]+: error\[' $$o) || { echo "context-test: default output of $$f is not the plain diagnostics"; exit 1; }; \
	done; echo "context-test: ok ($$(ls testdata/context/*.fc | wc -l) files)"
