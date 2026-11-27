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
        diff-check-fuzz diff-build diff-build-vet finc2 bootstrap ci clean help

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

ci: lint test build integration ## what CI runs

clean: ## remove build outputs
	rm -rf $(BIN) $(STAGE)

# --- finc0 vs finc1 differential: regression checks of the FROZEN v0.2 subset.
# finc0 implements only v0.2. Inputs that need a newer version say
# `// SINCE: vX.Y` in their leading comment block and are skipped
# (scripts/langver; diff-parse via scripts/sincefilter).
diff-parse: finc1 ## [v0.2 regression] finc0 vs finc1: byte-identical `parse --dump-ast` on the corpus
	@set -e; ts=$$($(GO) run ./scripts/sincefilter examples/tcp_echo.fc compiler/ std/*/ testdata/pass/*.fc testdata/fail/*.fc); \
	for t in $$ts; do \
	  cmp -s <($(FINC0) parse --dump-ast $$t 2>&1) <($(BIN)/finc1 parse --dump-ast $$t 2>&1) || { echo "DIFF: $$t"; exit 1; }; \
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

finc2: finc1 ## fixed-point smoke: finc1 -> finc2 -> finc3, cmp stage2.go stage3.go
	@mkdir -p $(STAGE)
	$(BIN)/finc1 build --emit-go=$(STAGE)/stage2.go -o $(BIN)/finc2 ./compiler
	$(BIN)/finc2 build --emit-go=$(STAGE)/stage3.go -o $(BIN)/finc3 ./compiler
	cmp $(STAGE)/stage2.go $(STAGE)/stage3.go && echo "finc2: stage2.go == stage3.go"
